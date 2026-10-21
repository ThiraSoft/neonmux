package server

import "net"

// Daemon helpers reused by the window for its own socket (ui.StartIPC) and
// its demo files.

// PrivateDir creates dir if needed and checks that it really is ours: a
// real directory, owned by our uid, with mode 0700 (see privateDir).
func PrivateDir(dir string) error { return privateDir(dir) }

// PeerUID returns the uid of the process at the other end of a Unix socket,
// or -1 if the kernel does not tell.
func PeerUID(c *net.UnixConn) int { return peerUID(c) }
