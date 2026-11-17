package traffic

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	mproxy "github.com/lqqyt2423/go-mitmproxy/proxy"
)

type blockedCapture struct {
	mproxy.BaseAddon
	entered chan struct{}
	release chan struct{}
}

func (b *blockedCapture) Response(*mproxy.Flow) {
	close(b.entered)
	<-b.release
}

func TestCloseDrainsCompletedCapture(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "complete evidence") }))
	defer upstream.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	dir := t.TempDir()
	tr, err := Open(dir, addr)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	block := &blockedCapture{entered: make(chan struct{}), release: make(chan struct{})}
	defer func() {
		select {
		case <-block.release:
		default:
			close(block.release)
		}
	}()
	tr.proxy.Addons = append([]mproxy.Addon{block}, tr.proxy.Addons...)
	serveDone := make(chan error, 1)
	go func() { serveDone <- tr.Start() }()
	proxyURL, _ := url.Parse("http://" + addr)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}, Timeout: 3 * time.Second}
	defer client.CloseIdleConnections()
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		for range 100 {
			response, err := client.Get(upstream.URL)
			if err == nil {
				response.Body.Close()
				return
			}
			select {
			case <-block.entered:
				return
			default:
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	select {
	case <-block.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("response did not reach capture")
	}
	closed := make(chan error, 1)
	go func() { closed <- tr.Close() }()
	<-serveDone // listener has stopped; the completed response is still queued.
	select {
	case err := <-closed:
		t.Fatalf("Close returned before capture finished: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(block.release)
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not finish after capture drained")
	}
	<-requestDone
	reopened, err := Open(dir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	_, response, err := reopened.Get(onlyExchangeID(t, reopened))
	if err != nil || !strings.HasSuffix(response, "complete evidence") {
		t.Fatalf("shutdown lost completed capture: response=%q err=%v", response, err)
	}
}

func TestCloseStopsRecordingProxy(t *testing.T) {
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
	defer tr.proxy.Close()
	defer tr.Close()
	tr.proxy.AddAddon(mproxy.NewUpstreamCertAddon(false))
	done := make(chan error, 1)
	go func() { done <- tr.Start() }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("proxy did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := fmt.Fprint(conn, "CONNECT example.invalid:443 HTTP/1.1\r\nHost: example.invalid:443\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "CONNECT"})
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT response=%v err=%v", response, err)
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatal(err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("recording proxy still accepts connections after Close")
	}
	if err := conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("hijacked CONNECT connection remained open after Close: %v", err)
	}
}
