package egress

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"

	actool "github.com/Autumn-27/norma/tool"
	netproxy "golang.org/x/net/proxy"
)

// SearchProxyURL gives SDK search a data-only listener. Its upstream sockets
// follow the execution TUN; backend-only control routes are on another port.
func SearchProxyURL(raw string) string {
	if gateway := strings.TrimRight(os.Getenv("ARTEX_EGRESS_URL"), "/"); gateway != "" {
		u, err := url.Parse(gateway)
		if err == nil {
			u.Host = net.JoinHostPort(u.Hostname(), "8791")
			return u.String()
		}
	}
	return raw
}

func searchProxyHandler(transport http.RoundTripper, store *proxyStore, lifecycle context.Context, dialer *net.Dialer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			if !r.URL.IsAbs() || (r.URL.Scheme != "http" && r.URL.Scheme != "https") {
				http.Error(w, "invalid search URL", http.StatusBadRequest)
				return
			}
			forward := httputil.ReverseProxy{Transport: transport, FlushInterval: -1, Rewrite: func(pr *httputil.ProxyRequest) {
				pr.Out.URL, pr.Out.Host = r.URL, r.URL.Host
			}}
			forward.ServeHTTP(w, r)
			return
		}
		remote, err := dialSearchTarget(r.Context(), r.Host, store.value.Load(), dialer)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer remote.Close()
		client, buffer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer client.Close()
		stop := context.AfterFunc(lifecycle, func() { client.Close(); remote.Close() })
		defer stop()
		if _, err := buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return
		}
		if err := buffer.Flush(); err != nil {
			return
		}
		done := make(chan struct{})
		go func() {
			_, _ = io.Copy(remote, buffer)
			_ = remote.Close()
			close(done)
		}()
		_, _ = io.Copy(client, remote)
		_ = client.Close()
		<-done
	})
}

func dialSearchTarget(ctx context.Context, target string, proxyURL *url.URL, dialer *net.Dialer) (net.Conn, error) {
	if _, _, err := net.SplitHostPort(target); err != nil {
		return nil, fmt.Errorf("invalid search CONNECT target")
	}
	if proxyURL == nil {
		return dialer.DialContext(ctx, "tcp", target)
	}
	if proxyURL.Scheme == "socks5" {
		proxyDialer, err := netproxy.FromURL(proxyURL, dialer)
		if err != nil {
			return nil, err
		}
		return proxyDialer.(netproxy.ContextDialer).DialContext(ctx, "tcp", target)
	}
	port := proxyURL.Port()
	if port == "" {
		port = "80"
		if proxyURL.Scheme == "https" {
			port = "443"
		}
	}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(proxyURL.Hostname(), port))
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			conn.Close()
		}
	}()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	if proxyURL.Scheme == "https" {
		secured := tls.Client(conn, &tls.Config{ServerName: proxyURL.Hostname()})
		if err := secured.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		conn = secured
	}
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: target}, Host: target, Header: make(http.Header)}
	if proxyURL.User != nil {
		password, _ := proxyURL.User.Password()
		credentials := proxyURL.User.Username() + ":" + password
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(credentials)))
	}
	if err := req.Write(conn); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("search proxy CONNECT returned HTTP %d", resp.StatusCode)
	}
	_ = conn.SetDeadline(time.Time{})
	ok = true
	return &bufferedConn{Conn: conn, reader: reader}, nil
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

type searchProbeRequest struct {
	Config actool.WebSearchConfig `json:"config"`
	Query  string                 `json:"query"`
	Limit  int                    `json:"limit"`
}
type searchProbeResponse struct {
	Results []actool.SearchResult `json:"results,omitempty"`
	Error   string                `json:"error,omitempty"`
}

func searchProbeHandler(w http.ResponseWriter, r *http.Request) {
	var req searchProbeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var store proxyStore
	if err := store.set(req.Config.Proxy); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	transport := independentTransport(&store)
	defer transport.CloseIdleConnections()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	probeCtx, cancel := context.WithCancel(r.Context())
	defer cancel()
	server := &http.Server{Handler: searchProxyHandler(transport, &store, probeCtx, channelDialer()), ReadHeaderTimeout: 5 * time.Second}
	go server.Serve(listener)
	defer server.Close()
	req.Config.Proxy = "http://" + listener.Addr().String()
	results, err := actool.WebSearchProbe(r.Context(), req.Config, req.Query, req.Limit)
	out := searchProbeResponse{Results: results}
	if err != nil {
		out.Error = err.Error()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (c *Client) ProbeSearch(ctx context.Context, config actool.WebSearchConfig, query string, limit int) ([]actool.SearchResult, error) {
	body, _ := json.Marshal(searchProbeRequest{Config: config, Query: query, Limit: limit})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/search/probe", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	// Search/model backends have their own request-context timeout.
	client := &http.Client{Transport: c.http.Transport}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("search gateway returned HTTP %d", resp.StatusCode)
	}
	var out searchProbeResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.Error != "" {
		return nil, fmt.Errorf("%s", out.Error)
	}
	return out.Results, nil
}
