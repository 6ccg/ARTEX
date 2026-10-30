//go:build linux

package egress

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const backendMark = 0x8792

func backendSocketControl(_, _ string, raw syscall.RawConn) error {
	return setSocketMark(raw, backendMark)
}

func gatewayHealthControl(_, _ string, raw syscall.RawConn) error {
	return setSocketMark(raw, llmDirectMark)
}

func setSocketMark(raw syscall.RawConn, mark int) error {
	var sockErr error
	if err := raw.Control(func(fd uintptr) {
		sockErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK, mark)
	}); err != nil {
		return err
	}
	return sockErr
}

// LockdownBackend keeps CAP_NET_RAW effective only in this Go process so it
// can mark control sockets. Dropping bounding, inheritable and ambient sets on
// ALL runtime threads makes every exec (Bash, PTY, scripts, MCP, browsers) lose
// the capability, including root children. Failure must stop startup.
func LockdownBackend() error {
	if os.Getenv("ARTEX_EGRESS_URL") == "" {
		return nil
	}
	all := func(trap, a, b, c uintptr) error {
		_, _, errno := syscall.AllThreadsSyscall(trap, a, b, c)
		if errno != 0 {
			return errno
		}
		return nil
	}
	for _, cap := range []uintptr{unix.CAP_NET_RAW, unix.CAP_SETPCAP} {
		if err := all(unix.SYS_PRCTL, unix.PR_CAPBSET_DROP, cap, 0); err != nil {
			return fmt.Errorf("drop child network capabilities: %w", err)
		}
	}
	if err := all(unix.SYS_PRCTL, unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL, 0); err != nil {
		return fmt.Errorf("clear ambient capabilities: %w", err)
	}
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	data := [2]unix.CapUserData{{Effective: 1 << unix.CAP_NET_RAW, Permitted: 1 << unix.CAP_NET_RAW}}
	if err := all(unix.SYS_CAPSET, uintptr(unsafe.Pointer(&header)), uintptr(unsafe.Pointer(&data[0])), 0); err != nil {
		return fmt.Errorf("restrict backend capabilities: %w", err)
	}
	if err := all(unix.SYS_PRCTL, unix.PR_SET_DUMPABLE, 0, 0); err != nil {
		return fmt.Errorf("protect backend control sockets: %w", err)
	}
	return nil
}
