package helper

import (
	"context"
	"errors"
	"net/url"
	"testing"
)

func TestGetProxyConnCanceledSOCKS5Dial(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	conn, err := GetProxyConn(ctx, &url.URL{Scheme: "socks5", Host: "127.0.0.1:1"}, "example.com:443", false)
	if conn != nil {
		conn.Close()
		t.Fatal("failed dial returned a connection")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
