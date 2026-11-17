package egress

import (
	"context"
	"net"
	"net/http"
	"os"
	"time"
)

// BackendDialContext is used only for backend-to-gateway and database control
// connections. Ordinary tools must keep their unmarked dialers.
func BackendDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	if os.Getenv("ARTEX_EGRESS_URL") != "" {
		dialer.Control = backendSocketControl
	}
	return dialer.DialContext(ctx, network, address)
}

func backendTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = BackendDialContext
	return transport
}
