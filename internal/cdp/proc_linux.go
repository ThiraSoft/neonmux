package cdp

import (
	"os/exec"
	"syscall"
)

// sysProcAttr puts the browser in its own process group, so that its
// helpers (renderer, GPU, zygote) go with it, and kills it if neonmux
// dies without closing it.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}

func kill(cmd *exec.Cmd) { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
