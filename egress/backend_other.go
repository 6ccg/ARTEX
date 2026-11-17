//go:build !linux

package egress

import "syscall"

func backendSocketControl(_, _ string, _ syscall.RawConn) error { return nil }
func gatewayHealthControl(_, _ string, _ syscall.RawConn) error { return nil }
func LockdownBackend() error                                    { return nil }
