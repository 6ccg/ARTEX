package egress

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLLMProxyIgnoresEnvironment(t *testing.T) {
	t.Setenv("ARTEX_EGRESS_URL", "")
	t.Setenv("HTTP_PROXY", "http://environment.invalid:8080")
	t.Setenv("HTTPS_PROXY", "http://environment.invalid:8080")
	previous := llmProxy.value.Load()
	t.Cleanup(func() { llmProxy.value.Store(previous) })
	for _, raw := range []string{"", "http://configured.invalid:8080"} {
		if err := SetLLMProxy(raw); err != nil {
			t.Fatal(err)
		}
		transport, err := NewLLMTransport()
		if err != nil {
			t.Fatal(err)
		}
		got, err := transport.(*http.Transport).Proxy(&http.Request{})
		if err != nil || (raw == "" && got != nil) || (raw != "" && (got == nil || got.String() != raw)) {
			t.Fatalf("proxy=%v err=%v, want %q", got, err, raw)
		}
	}
}

func TestLLMGatewayPreservesRequestAndStreamsResponse(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.RequestURI() != "/v1/messages?stream=1" || r.Header.Get("Authorization") != "Bearer test-key" || string(body) != `{}` || r.Header.Get(llmUpstreamHeader) != "" {
			t.Error("gateway changed the upstream request")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-release
	}))
	t.Cleanup(upstream.Close)
	var store proxyStore
	transport := testChannelTransport(&store)
	t.Cleanup(transport.CloseIdleConnections)
	gateway := httptest.NewServer(llmHandler(transport))
	t.Cleanup(gateway.Close)
	t.Cleanup(func() { close(release) })
	t.Setenv("ARTEX_EGRESS_URL", gateway.URL)
	modelTransport, err := NewLLMTransport()
	if err != nil {
		t.Fatal(err)
	}
	unmarkTestCarrier(modelTransport)
	client := http.Client{Transport: modelTransport, Timeout: 3 * time.Second}
	req, err := http.NewRequest(http.MethodPost, upstream.URL+"/v1/messages?stream=1", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer test-key")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || line != "data: first\n" || resp.Request.URL.String() != req.URL.String() {
		t.Fatalf("stream event=%q upstream=%s err=%v", line, resp.Request.URL, err)
	}
}

func TestIndependentGatewayUsesRequestedProxy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(independentProxyHeader) != "" || r.Header.Get(llmUpstreamHeader) != "" {
			t.Error("gateway headers reached upstream")
		}
		io.WriteString(w, "direct")
	}))
	defer upstream.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.String() != upstream.URL+"/ip" {
			t.Errorf("proxy target=%s", r.URL)
		}
		io.WriteString(w, "proxy")
	}))
	defer proxy.Close()
	gateway := httptest.NewServer(independentHandler(false))
	defer gateway.Close()
	t.Setenv("ARTEX_EGRESS_URL", gateway.URL)
	t.Setenv("HTTP_PROXY", "http://environment.invalid:8080")
	for _, test := range []struct{ raw, want string }{{"", "direct"}, {proxy.URL, "proxy"}} {
		transport, err := NewIndependentTransport(test.raw)
		if err != nil {
			t.Fatal(err)
		}
		unmarkTestCarrier(transport)
		client := &http.Client{Transport: transport}
		resp, err := client.Get(upstream.URL + "/ip")
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		client.CloseIdleConnections()
		if err != nil || string(body) != test.want {
			t.Fatalf("proxy=%q body=%q err=%v", test.raw, body, err)
		}
	}
	proxy.Close()
	transport, err := NewIndependentTransport(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	unmarkTestCarrier(transport)
	client := &http.Client{Transport: transport}
	defer client.CloseIdleConnections()
	resp, err := client.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("failed proxy fell back to direct: status=%d", resp.StatusCode)
	}
}

func TestDirectGatewayProbeBlocksLoopback(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	gateway := httptest.NewServer(independentHandler(true))
	defer gateway.Close()
	t.Setenv("ARTEX_EGRESS_URL", gateway.URL)
	transport, err := NewProbeTransport("")
	if err != nil {
		t.Fatal(err)
	}
	unmarkTestCarrier(transport)
	client := &http.Client{Transport: transport}
	defer client.CloseIdleConnections()
	resp, err := client.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("loopback probe status=%d", resp.StatusCode)
	}
}

func TestSearchHTTPSProxyChain(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Errorf("path=%q", r.URL.Path)
		}
		io.WriteString(w, "ok")
	}))
	t.Cleanup(upstream.Close)
	var direct proxyStore
	proxy := newSearchTestGateway(t, &direct)
	var configured proxyStore
	if err := configured.set(proxy.URL); err != nil {
		t.Fatal(err)
	}
	gateway := newSearchTestGateway(t, &configured)
	t.Setenv("ARTEX_EGRESS_URL", gateway.URL)
	u, err := url.Parse(gateway.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := upstream.Client().Transport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(u)
	t.Cleanup(transport.CloseIdleConnections)
	assertResponse(t, &http.Client{Transport: transport, Timeout: 3 * time.Second}, upstream.URL+"/search", http.StatusOK, "ok")
}

func TestSearchUsesDataListener(t *testing.T) {
	t.Setenv("ARTEX_EGRESS_URL", "http://172.30.254.2:8790")
	if got := SearchProxyURL(""); got != "http://172.30.254.2:8791" {
		t.Fatalf("search must not expose the backend control listener: %s", got)
	}
}

func TestProxyChannelsAreIndependentAndFailWithoutFallback(t *testing.T) {
	var directCalls atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { directCalls.Add(1); io.WriteString(w, "direct") }))
	t.Cleanup(origin.Close)
	var ai, search proxyStore
	aiTransport := testChannelTransport(&ai)
	t.Cleanup(aiTransport.CloseIdleConnections)
	gateway := httptest.NewServer(llmHandler(aiTransport))
	t.Cleanup(gateway.Close)
	t.Setenv("ARTEX_EGRESS_URL", gateway.URL)
	modelTransport, err := NewLLMTransport()
	if err != nil {
		t.Fatal(err)
	}
	unmarkTestCarrier(modelTransport)
	model := &http.Client{Transport: modelTransport, Timeout: 3 * time.Second}
	searchGateway := newSearchTestGateway(t, &search)
	searchURL, err := url.Parse(searchGateway.URL)
	if err != nil {
		t.Fatal(err)
	}
	searchTransport := http.DefaultTransport.(*http.Transport).Clone()
	searchTransport.Proxy = http.ProxyURL(searchURL)
	t.Cleanup(searchTransport.CloseIdleConnections)
	searchClient := &http.Client{Transport: searchTransport, Timeout: 3 * time.Second}
	channels := []struct {
		name   string
		client *http.Client
		store  *proxyStore
		proxy  *httptest.Server
	}{
		{"AI", model, &ai, nil}, {"search", searchClient, &search, nil},
	}
	for i := range channels {
		channel := &channels[i]
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			io.WriteString(w, channel.name)
		}))
		t.Cleanup(proxy.Close)
		channel.proxy = proxy
		if err := channel.store.set(proxy.URL); err != nil {
			t.Fatal(err)
		}
	}
	for _, channel := range channels {
		t.Run(channel.name, func(t *testing.T) {
			assertResponse(t, channel.client, origin.URL, http.StatusOK, channel.name)
			if err := channel.store.set(""); err != nil {
				t.Fatal(err)
			}
			assertResponse(t, channel.client, origin.URL, http.StatusOK, "direct")
			for _, other := range channels {
				if other.name != channel.name {
					assertResponse(t, other.client, origin.URL, http.StatusOK, other.name)
				}
			}
			if err := channel.store.set(channel.proxy.URL); err != nil {
				t.Fatal(err)
			}
		})
	}
	if directCalls.Load() != 2 {
		t.Fatalf("direct calls=%d", directCalls.Load())
	}
	for _, channel := range channels {
		channel.proxy.Close()
		assertResponse(t, channel.client, origin.URL, http.StatusBadGateway, "")
	}
	if directCalls.Load() != 2 {
		t.Fatal("failed proxy fell back to the origin")
	}
}

// HTTP forwarding tests run without Linux privileges. The socket mark and
// capability boundary have a separate real-Linux subprocess test.
func unmarkTestCarrier(transport http.RoundTripper) {
	transport.(*llmTransport).base.(*http.Transport).DialContext = (&net.Dialer{}).DialContext
}

func testChannelTransport(store *proxyStore) *http.Transport {
	transport := channelTransport(store)
	transport.DialContext = (&net.Dialer{}).DialContext
	return transport
}

func newSearchTestGateway(t *testing.T, store *proxyStore) *httptest.Server {
	t.Helper()
	transport := testChannelTransport(store)
	t.Cleanup(transport.CloseIdleConnections)
	ctx, cancel := context.WithCancel(context.Background())
	server := httptest.NewServer(searchProxyHandler(transport, store, ctx, &net.Dialer{}))
	t.Cleanup(server.Close)
	t.Cleanup(cancel)
	return server
}

func assertResponse(t *testing.T, client *http.Client, target string, status int, body string) {
	t.Helper()
	resp, err := client.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != status || string(got) != body {
		t.Fatalf("response=(%d, %q), want (%d, %q), err=%v", resp.StatusCode, got, status, body, err)
	}
}
