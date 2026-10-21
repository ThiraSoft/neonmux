package server

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerPID returns the PID of the process at the other end of a Unix socket.
// The kernel knows it even when that process is stuck and accepts nothing.
func peerPID(c *net.UnixConn) int {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0
	}
	pid := 0
	_ = raw.Control(func(fd uintptr) {
		if cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED); err == nil {
			pid = int(cred.Pid)
		}
	})
	return pid
}

// peerUID returns the uid of the process at the other end of a Unix socket,
// or -1 if the kernel does not tell.
func peerUID(c *net.UnixConn) int {
	raw, err := c.SyscallConn()
	if err != nil {
		return -1
	}
	uid := -1
	_ = raw.Control(func(fd uintptr) {
		if cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED); err == nil {
			uid = int(cred.Uid)
		}
	})
	return uid
}
