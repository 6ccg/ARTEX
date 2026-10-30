package egress

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
)

// Gateway-owned sockets only: administrative probes and the configured uplink.
const llmDirectMark = 0x8791
const executionMark = 0x8793

type ProxySettings struct {
	Execution string `json:"global_proxy"`
	LLM       string `json:"llm_proxy"`
	Search    string `json:"web_search_proxy"`
	AllowIPv6 bool   `json:"egress_allow_ipv6"`
}

type proxyPatch struct {
	Execution *string `json:"global_proxy,omitempty"`
	LLM       *string `json:"llm_proxy,omitempty"`
	Search    *string `json:"web_search_proxy,omitempty"`
	AllowIPv6 *bool   `json:"egress_allow_ipv6,omitempty"`
}

func ParseProxy(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return nil, fmt.Errorf("invalid proxy URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5" {
		return nil, fmt.Errorf("proxy must use http, https or socks5")
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("invalid proxy port")
		}
	} else {
		port := map[string]string{"http": "80", "https": "443", "socks5": "1080"}[u.Scheme]
		u.Host = net.JoinHostPort(u.Hostname(), port)
	}
	return u, nil
}

type proxyStore struct{ value atomic.Pointer[url.URL] }

func (s *proxyStore) set(raw string) error {
	u, err := ParseProxy(raw)
	if err == nil {
		s.value.Store(u)
	}
	return err
}

func (s *proxyStore) proxy(*http.Request) (*url.URL, error) { return s.value.Load(), nil }

var llmProxy proxyStore
var searchProxy proxyStore

func SetLLMProxy(raw string) error    { return llmProxy.set(raw) }
func SetSearchProxy(raw string) error { return searchProxy.set(raw) }
