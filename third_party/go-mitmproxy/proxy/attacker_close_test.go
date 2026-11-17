package proxy

import (
	"errors"
	"net"
	"testing"
	"time"
)

func TestAttackerListenerCloseUnblocksAccept(t *testing.T) {
	l := &attackerListener{connChan: make(chan net.Conn), done: make(chan struct{})}
	done := make(chan error, 1)
	go func() { _, err := l.Accept(); done <- err }()
	l.Close()
	l.Close()
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("attacker listener remained blocked after close")
	}
}
