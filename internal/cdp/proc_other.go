//go:build !linux

package cdp

import (
	"os/exec"
	"syscall"
)

// sysProcAttr puts the browser in its own process group, so that its
// helpers go with it. Without Pdeathsig, the browser notices that neonmux
// died when its pipe closes, and quits.
func sysProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

func kill(cmd *exec.Cmd) { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
