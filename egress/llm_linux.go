//go:build linux

package egress

import (
	"context"
	"net"
	"syscall"
	"time"
)

const llmDirectMark = 0x8791

func channelDialer() *net.Dialer {
	control := func(_, address string, raw syscall.RawConn) error {
		host, _, _ := net.SplitHostPort(address)
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
		var sockErr error
		if err := raw.Control(func(fd uintptr) {
			sockErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK, llmDirectMark)
		}); err != nil {
			return err
		}
		return sockErr
	}
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: control}
	dnsDialer := &net.Dialer{Timeout: 5 * time.Second, Control: control}
	dialer.Resolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		return dnsDialer.DialContext(ctx, network, address)
	}}
	return dialer
}
