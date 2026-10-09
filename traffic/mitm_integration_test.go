package traffic

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"
)

func TestMITMDecryptsHTTPSAndRecordsBody(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "recorded HTTPS response")
	}))
	defer target.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	tr, err := Open(t.TempDir(), addr)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	go tr.Start()
	defer tr.proxy.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("MITM listener did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	ca, err := os.ReadFile(tr.CACertPath())
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		t.Fatal("MITM CA unavailable")
	}
	proxyURL, _ := url.Parse("http://" + addr)
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: roots}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, target.URL+"/capture", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || string(body) != "recorded HTTPS response" {
		t.Fatalf("HTTPS response: %q %v", body, err)
	}
	var recorded []byte
	if err := tr.DB().QueryRow(`SELECT b.resp_body FROM exchange_bodies b JOIN exchanges e ON e.id=b.id WHERE e.url=? AND e.status=200`, req.URL.String()).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if string(recorded) != string(body) {
		t.Fatalf("MITM did not record decrypted body: %q", recorded)
	}
}
