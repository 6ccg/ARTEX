package llmhttp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Autumn-27/artex/egress"
)

func TestNewClientUsesNativeSOCKS5Proxy(t *testing.T) {
	t.Setenv("ARTEX_EGRESS_URL", "")
	t.Setenv("HTTPS_PROXY", "http://environment.invalid:8080")
	if err := egress.SetLLMProxy("socks5://user:pass@127.0.0.1:1080"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = egress.SetLLMProxy("") })
	client, err := NewClient()
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type=%T", client.Transport)
	}
	proxyURL, err := transport.Proxy(httptest.NewRequest(http.MethodGet, "https://target.invalid", nil))
	if err != nil {
		t.Fatal(err)
	}
	if proxyURL == nil || proxyURL.Scheme != "socks5" || proxyURL.Host != "127.0.0.1:1080" || proxyURL.User.String() != "user:pass" {
		t.Fatalf("proxy URL=%v", proxyURL)
	}
}
