package shell

import (
	"strings"

	"golang.org/x/sys/unix"
)

// refusedFS reports a filesystem graphics files are never read from: any
// that is not local (NFS, SMB, AFP, WebDAV), FUSE ones (macFUSE, fuse-t),
// where a read can hang for good, and devfs.
func refusedFS(st *unix.Statfs_t) bool {
	if st.Flags&unix.MNT_LOCAL == 0 {
		return true
	}
	name := unix.ByteSliceToString(st.Fstypename[:])
	return strings.Contains(name, "fuse") || name == "devfs"
}
