package egress

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
)

const llmUpstreamHeader = "X-Artex-LLM-Upstream"

// NewLLMTransport uses only the AI-request proxy, independently of task traffic.
// Docker model calls use the gateway's marked sockets to bypass the execution TUN.
func NewLLMTransport() (http.RoundTripper, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = llmProxy.proxy
	base := strings.TrimRight(os.Getenv("ARTEX_EGRESS_URL"), "/")
	if base == "" {
		return transport, nil
	}
	transport.Proxy = nil // Internal carrier never uses an upstream/environment proxy.
	endpoint, err := url.Parse(base + "/llm")
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return nil, fmt.Errorf("invalid AI request gateway URL")
	}
	return &llmTransport{base: transport, endpoint: endpoint}, nil
}

type llmTransport struct {
	base     http.RoundTripper
	endpoint *url.URL
}

func (t *llmTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	out := req.Clone(req.Context())
	u := *t.endpoint
	out.URL, out.Host = &u, u.Host
	out.Header.Set(llmUpstreamHeader, req.URL.String())
	resp, err := t.base.RoundTrip(out)
	if resp != nil {
		resp.Request = req
	}
	return resp, err
}

func llmHandler(transport http.RoundTripper) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.Header.Get(llmUpstreamHeader)
		upstream, err := url.Parse(raw)
		if err != nil || upstream.Host == "" || (upstream.Scheme != "http" && upstream.Scheme != "https") {
			http.Error(w, "invalid LLM upstream URL", http.StatusBadRequest)
			return
		}
		proxy := httputil.ReverseProxy{
			Transport: transport,
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.Out.URL = upstream
				pr.Out.Host = upstream.Host
				pr.Out.Header.Del(llmUpstreamHeader)
			},
			FlushInterval: -1,
		}
		proxy.ServeHTTP(w, r)
	})
}

func channelTransport(store *proxyStore) *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = store.proxy
	transport.DialContext = channelDialer().DialContext
	return transport
}
