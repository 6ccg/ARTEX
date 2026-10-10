//go:build !linux

package egress

import (
	"net"
	"time"
)

func channelDialer() *net.Dialer {
	return &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
}
