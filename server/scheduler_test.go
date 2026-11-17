package server

import (
	"fmt"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestBoundTriggerAndSettingsWorkWithoutGlobalModel(t *testing.T) {
	m := newTestManager(t)
	s := newIdleAPIServer(t, m)
	s.chatAgent, s.llmOn = nil, false
	const key = "bound_trigger_regression"
	if _, err := m.pg.CreateAgent(key, "bound trigger", ""); err != nil {
		t.Fatal(err)
	}
	defer m.pg.DeleteAgent(key)
	id, err := m.pg.SaveProfile(&db.LLMProfile{Name: key, Format: "anthropic", Model: "test", APIKey: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer m.pg.DeleteProfile(id)
	previous, _ := m.pg.GetSchedState(schedKeyLastTaskCreate)
	defer func() {
		if previous == "" {
			m.pg.Exec(`DELETE FROM scheduler_state WHERE key=$1`, schedKeyLastTaskCreate)
		} else {
			m.pg.SetSchedState(schedKeyLastTaskCreate, previous)
		}
	}()
	var last int64
	if err := m.pg.QueryRow(`SELECT COALESCE(MAX(id),0) FROM tasks`).Scan(&last); err != nil {
		t.Fatal(err)
	}
	m.pg.SetSchedState(schedKeyLastTaskCreate, i64s(last))
	task, err := m.pg.CreateTask("trigger regression", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer m.pg.DeleteTask(task.ID)
	sc := newScheduler(s.Runtime)
	triggers := []*db.AgentTrigger{{AgentKey: key, OnTaskCreate: true}}
	sc.fireTaskCreates(triggers)
	if sc.mustState(schedKeyLastTaskCreate) != i64s(last) {
		t.Fatal("unavailable model consumed event")
	}
	if err := m.pg.SetAgentLLMProfile(key, &id); err != nil {
		t.Fatal(err)
	}
	s.triggerActive[key] = 1 // Keep the queued fire offline; no model request is needed.
	sc.fireTaskCreates(triggers)
	if len(s.triggerQ[key]) != 1 || sc.mustState(schedKeyLastTaskCreate) != i64s(task.ID) {
		t.Fatal("bound model did not admit event without a global model")
	}
	oldAgent := s.profChatAgents[id]
	s.taskAgents["cached"] = &taskAgentBundle{}
	oldOn, oldBackend, _, _, oldProxy := m.WebSearch()
	defer m.SetWebSearch(oldOn, oldBackend, nil, nil, &oldProxy)
	w := httptest.NewRecorder()
	s.putSettings(w, httptest.NewRequest("PUT", "/api/settings", strings.NewReader(fmt.Sprintf(`{"web_search_enabled":%t}`, !oldOn))))
	if w.Code != 200 {
		t.Fatalf("settings: %d %s", w.Code, w.Body.String())
	}
	if len(s.taskAgents) != 0 || s.chatAgentForProfile(id) == oldAgent {
		t.Fatal("settings left bound agents cached while global model was off")
	}
}

func TestSchedulerAdvancesWhileTriggersDisabled(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	keys := []string{schedKeyLastFinding, schedKeyFiredGoals, schedKeyLastTimeout, schedKeyLastToolCall, schedKeyLastTaskCreate}
	previous := map[string]string{}
	for _, key := range keys {
		previous[key], err = m.pg.GetSchedState(key)
		if err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		for _, key := range keys {
			if previous[key] == "" {
				m.pg.Exec(`DELETE FROM scheduler_state WHERE key=$1`, key)
			} else {
				m.pg.SetSchedState(key, previous[key])
			}
		}
	}()
	triggers, err := m.pg.ListEnabledTriggers()
	if err != nil {
		t.Fatal(err)
	}
	for _, trigger := range triggers {
		if _, err := m.pg.Exec(`UPDATE agent_triggers SET enabled=false WHERE id=$1`, trigger.ID); err != nil {
			t.Fatal(err)
		}
		defer m.pg.Exec(`UPDATE agent_triggers SET enabled=true WHERE id=$1`, trigger.ID)
	}
	sc := newScheduler((&Server{Runtime: &Runtime{m: m, engine: NewEngine(m)}}).Runtime)
	if err := sc.init(); err != nil {
		t.Fatal(err)
	}
	task, err := m.pg.CreateTask("disabled trigger interval", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer m.pg.DeleteTask(task.ID)
	sc.step()
	last, err := strconv.ParseInt(sc.mustState(schedKeyLastTaskCreate), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	events, err := m.pg.NewTasksSince(last)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.TaskID == task.ID {
			t.Fatal("disabled trigger interval left an event queued for historical replay")
		}
	}
}
