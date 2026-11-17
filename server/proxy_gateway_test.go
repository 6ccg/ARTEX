package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Autumn-27/artex/egress"
)

func TestProxyPoolSwitchUpdatesGateway(t *testing.T) {
	var requested string
	reject := false
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/proxy" {
			t.Errorf("unexpected gateway request: %s %s", r.Method, r.URL.Path)
		}
		var patch map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			t.Error(err)
		}
		if len(patch) != 1 {
			t.Error("pool switch changed AI, search or IPv6 settings")
		}
		if err := json.Unmarshal(patch["global_proxy"], &requested); err != nil {
			t.Error(err)
		}
		if reject {
			http.Error(w, "gateway rejected configuration", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer gateway.Close()
	t.Setenv("ARTEX_EGRESS_URL", gateway.URL)
	m := &Manager{network: egress.FromEnvironment(), proxyPool: &proxyPoolState{entries: []proxyPoolEntry{
		{ProxyPoolEntry: ProxyPoolEntry{ID: "a", Enabled: true}, RawURL: "http://a:8080"},
		{ProxyPoolEntry: ProxyPoolEntry{ID: "b", Enabled: true}, RawURL: "socks5://b:1080"},
	}}}
	for _, id := range []string{"a", "b", ""} {
		if err := m.applyActiveProxyLocked(id, "manual"); err != nil {
			t.Fatal(err)
		}
		if requested != m.GlobalProxy() || m.proxyPool.active != id {
			t.Fatalf("gateway=%q manager=%q active=%q", requested, m.GlobalProxy(), m.proxyPool.active)
		}
		if m.ApplicationProxy() != "" || m.ProxyAddr() != "" {
			t.Fatal("Docker added a second application proxy hop")
		}
	}
	if err := m.applyActiveProxyLocked("a", "manual"); err != nil {
		t.Fatal(err)
	}
	reject = true
	if err := m.applyActiveProxyLocked("b", "manual"); err == nil {
		t.Fatal("gateway failure was ignored")
	}
	if m.GlobalProxy() != "http://a:8080" || m.proxyPool.active != "a" {
		t.Fatal("failed gateway update changed the active proxy")
	}
}

func TestDockerEmptyProxyDoesNotResumeTasks(t *testing.T) {
	m := newTestManager(t)
	task, err := m.CreateTask("proxy pause", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) }()
	if err := m.ApplyProxyPause(task.ID); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARTEX_EGRESS_URL", "http://gateway.invalid:8790")
	m.network = egress.FromEnvironment()
	m.globalProxy = ""
	s := &Server{Runtime: &Runtime{m: m}}
	s.proxyReconnectSuccess()
	if task.lifecycleSnapshot().PauseReason != "paused_by_proxy" {
		t.Fatal("clearing the Docker proxy resumed a blocked task")
	}
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+task.ID+"/proxy/test", nil)
	req.SetPathValue("id", task.ID)
	res := httptest.NewRecorder()
	s.testTaskProxy(res, req)
	var payload struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.OK || task.lifecycleSnapshot().PauseReason != "paused_by_proxy" {
		t.Fatal("manual direct test bypassed Docker's empty-proxy block")
	}
}
