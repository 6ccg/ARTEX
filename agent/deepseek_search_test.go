package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type deepSeekRoundTripper func(*http.Request) (*http.Response, error)

func (f deepSeekRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDeepSeekSearchToolUsesConfiguredKeyAndQuery(t *testing.T) {
	client := &http.Client{Transport: deepSeekRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != deepSeekToolsURL+"search" {
			t.Errorf("request URL = %q", r.URL)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authorization = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"count":3,"query":"ARTEX"}` {
			t.Errorf("request body = %s", body)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"results":[{"title":"ARTEX","url":"https://example.test"}]}`)), Header: make(http.Header)}, nil
	})}
	tool := deepSeekTool("deepseek_search", "", "search", "test-key", client, map[string]any{
		"query": map[string]any{"type": "string"}, "count": map[string]any{"type": "integer"},
	}, "query")
	result, err := tool.Call(context.Background(), []byte(`{"query":"ARTEX","count":3}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || !strings.Contains(result.Flatten(), `"results"`) {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestDeepSeekToolsRespectGlobalAndBackendSettings(t *testing.T) {
	for _, opts := range []WebSearchOpts{
		{Enabled: false, Backend: "deepseek", DeepSeekAPIKey: "key"},
		{Enabled: true, Backend: "ddgs", DeepSeekAPIKey: "key"},
		{Enabled: true, Backend: "deepseek"},
	} {
		if got := deepSeekTools(opts); len(got) != 0 {
			t.Fatalf("deepseek tools enabled for opts %+v", opts)
		}
	}
}

func TestDeepSeekSearchProbeUsesGatewayAndUnsavedProxy(t *testing.T) {
	const proxy = "http://proxy.invalid:8080"
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/independent" ||
			r.Header.Get("X-Artex-LLM-Upstream") != deepSeekToolsURL+"search" ||
			r.Header.Get("X-Artex-Independent-Proxy") != proxy ||
			r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("unexpected probe: %s %s headers=%v", r.Method, r.URL, r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"count":3,"query":"ARTEX"}` {
			t.Errorf("body=%s", body)
		}
		_, _ = io.WriteString(w, `{"results":[{"title":"ARTEX"}]}`)
	}))
	defer gateway.Close()
	t.Setenv("ARTEX_EGRESS_URL", gateway.URL)
	count, err := DeepSeekSearchProbe(context.Background(), "test-key", proxy, "ARTEX", 3)
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}
