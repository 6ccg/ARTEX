//go:build linux

package egress

import (
	"net"
	"os"
	"os/exec"
	"testing"

	"golang.org/x/sys/unix"
)

// Run in a disposable Linux container with only NET_RAW and SETPCAP enabled.
// Lockdown changes process-wide capabilities, so exercise it in a child test
// process instead of affecting the rest of the test suite.
func TestBackendCapabilityIsolation(t *testing.T) {
	mode := os.Getenv("ARTEX_TEST_CAPABILITY_PROCESS")
	if mode == "" {
		header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
		var caps [2]unix.CapUserData
		if err := unix.Capget(&header, &caps[0]); err != nil || caps[0].Effective&(1<<unix.CAP_SETPCAP|1<<unix.CAP_NET_RAW) != 1<<unix.CAP_SETPCAP|1<<unix.CAP_NET_RAW {
			t.Skip("requires NET_RAW and SETPCAP; run the container check")
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestBackendCapabilityIsolation$", "-test.v")
		cmd.Env = append(os.Environ(), "ARTEX_EGRESS_URL=http://172.30.254.2:8790", "ARTEX_TEST_CAPABILITY_PROCESS=backend")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("backend isolation: %v\n%s", err, output)
		}
		return
	}
	if mode == "backend" {
		if err := LockdownBackend(); err != nil {
			t.Fatal(err)
		}
	}
	packet, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer packet.Close()
	raw, err := packet.(*net.UDPConn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	err = backendSocketControl("", "", raw)
	if mode == "agent" {
		if err == nil {
			t.Fatal("Agent inherited the ability to mark backend control sockets")
		}
		return
	}
	if err != nil {
		t.Fatalf("backend lost control socket capability: %v", err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestBackendCapabilityIsolation$", "-test.v")
	cmd.Env = append(os.Environ(), "ARTEX_TEST_CAPABILITY_PROCESS=agent")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Agent isolation: %v\n%s", err, output)
	}
}
