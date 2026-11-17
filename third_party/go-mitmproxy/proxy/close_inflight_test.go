package proxy

import (
	"bufio"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

type helloWriteConn struct {
	net.Conn
	written chan struct{}
	once    sync.Once
}

func (c *helloWriteConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.once.Do(func() { close(c.written) })
	return n, err
}

func TestCloseDuringTLSHandshake(t *testing.T) {
	helper := &testProxyHelper{server: &http.Server{}, proxyAddr: ":29093"}
	helper.init(t)
	defer helper.tlsPlainLn.Close()
	defer helper.ln.Close()
	go helper.server.ServeTLS(helper.tlsPlainLn, "", "")
	for range 30 {
		p, err := NewProxy(&Options{Addr: ":29093", SslInsecure: true})
		if err != nil {
			t.Fatal(err)
		}
		served := make(chan error, 1)
		go func() { served <- p.Start() }()
		var conn net.Conn
		for range 100 {
			conn, err = net.DialTimeout("tcp", "127.0.0.1:29093", 20*time.Millisecond)
			if err == nil {
				break
			}
			time.Sleep(time.Millisecond)
		}
		if err != nil {
			p.Close()
			t.Fatal(err)
		}
		io.WriteString(conn, "CONNECT "+helper.tlsPlainLn.Addr().String()+" HTTP/1.1\r\nHost: "+helper.tlsPlainLn.Addr().String()+"\r\n\r\n")
		if _, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "CONNECT"}); err != nil {
			p.Close()
			conn.Close()
			t.Fatal(err)
		}
		finished := make(chan struct{})
		clientConn := &helloWriteConn{Conn: conn, written: make(chan struct{})}
		go func() {
			defer close(finished)
			client := tls.Client(clientConn, &tls.Config{InsecureSkipVerify: true})
			_ = client.Handshake()
			client.Close()
		}()
		<-clientConn.written
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
		<-served
		<-finished
	}
}
