//go:build !linux

package egress

import "os/exec"

func setParentDeathSignal(*exec.Cmd) {}
