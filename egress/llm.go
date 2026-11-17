package egress

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"syscall"
)

const llmUpstreamHeader = "X-Artex-LLM-Upstream"
const independentProxyHeader = "X-Artex-Independent-Proxy"

// NewIndependentTransport keeps update downloads and IP probes independent of
// the execution gateway, including when testing an unsaved proxy address.
func NewIndependentTransport(raw string) (http.RoundTripper, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "socks5h://") {
		raw = "socks5://" + strings.TrimPrefix(raw, "socks5h://")
	}
	proxy, err := ParseProxy(raw)
	if err != nil {
		return nil, err
	}
	transport := backendTransport()
	base := strings.TrimRight(os.Getenv("ARTEX_EGRESS_URL"), "/")
	if base == "" {
		if proxy != nil {
			transport.Proxy = http.ProxyURL(proxy)
		}
		return transport, nil
	}
	endpoint, err := url.Parse(base + "/independent")
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return nil, fmt.Errorf("invalid independent request gateway URL")
	}
	return &llmTransport{base: transport, endpoint: endpoint, independentProxy: &raw}, nil
}

func NewProbeTransport(raw string) (http.RoundTripper, error) {
	transport, err := NewIndependentTransport(raw)
	if gateway, ok := transport.(*llmTransport); ok {
		gateway.endpoint.Path = strings.TrimSuffix(gateway.endpoint.Path, "/independent") + "/probe"
	}
	return transport, err
}

func independentHandler(probe bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var store proxyStore
		if err := store.set(r.Header.Get(independentProxyHeader)); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		transport := independentTransport(&store)
		if probe && store.value.Load() == nil {
			dialer := channelDialer()
			control := dialer.Control
			dialer.Control = func(network, address string, raw syscall.RawConn) error {
				host, _, err := net.SplitHostPort(address)
				if err != nil {
					return err
				}
				ip := net.ParseIP(host)
				if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
					return fmt.Errorf("拒绝访问本机/链路本地地址 %s", host)
				}
				if control != nil {
					return control(network, address, raw)
				}
				return nil
			}
			transport.DialContext = dialer.DialContext
		}
		defer transport.CloseIdleConnections()
		llmHandler(transport).ServeHTTP(w, r)
	})
}

// NewLLMTransport uses the AI-request proxy as an additional hop in Docker.
// Docker model calls use the protected backend carrier, then follow the TUN.
func NewLLMTransport() (http.RoundTripper, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = llmProxy.proxy
	base := strings.TrimRight(os.Getenv("ARTEX_EGRESS_URL"), "/")
	if base == "" {
		return transport, nil
	}
	transport = backendTransport() // Only the backend may reach the control listener.
	endpoint, err := url.Parse(base + "/llm")
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return nil, fmt.Errorf("invalid AI request gateway URL")
	}
	return &llmTransport{base: transport, endpoint: endpoint}, nil
}

type llmTransport struct {
	base             http.RoundTripper
	endpoint         *url.URL
	independentProxy *string
}

func (t *llmTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func (t *llmTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	out := req.Clone(req.Context())
	u := *t.endpoint
	out.URL, out.Host = &u, u.Host
	out.Header.Set(llmUpstreamHeader, req.URL.String())
	if t.independentProxy != nil {
		out.Header.Set(independentProxyHeader, *t.independentProxy)
	}
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
				pr.Out.Header.Del(independentProxyHeader)
			},
			FlushInterval: -1,
		}
		proxy.ServeHTTP(w, r)
	})
}

func channelTransport(store *proxyStore) *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = store.proxy
	// Runtime AI/search traffic follows the same mandatory TUN as commands.
	transport.DialContext = executionDialer().DialContext
	return transport
}

func independentTransport(store *proxyStore) *http.Transport {
	transport := channelTransport(store)
	transport.DialContext = channelDialer().DialContext
	return transport
}
