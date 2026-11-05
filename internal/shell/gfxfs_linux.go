package shell

import "golang.org/x/sys/unix"

// refusedFS reports a filesystem graphics files are never read from: remote
// or FUSE ones, where a read can hang for good, and kernel pseudo
// filesystems wherever they are mounted, where a read has side effects or
// leaks secrets.
func refusedFS(st *unix.Statfs_t) bool {
	switch uint32(st.Type) {
	case unix.NFS_SUPER_MAGIC, unix.SMB_SUPER_MAGIC, unix.SMB2_SUPER_MAGIC,
		unix.CIFS_SUPER_MAGIC, unix.FUSE_SUPER_MAGIC, unix.V9FS_MAGIC,
		unix.CODA_SUPER_MAGIC, unix.AFS_SUPER_MAGIC, unix.AFS_FS_MAGIC,
		unix.CEPH_SUPER_MAGIC, unix.NCP_SUPER_MAGIC,
		unix.PROC_SUPER_MAGIC, unix.SYSFS_MAGIC, unix.DEBUGFS_MAGIC,
		unix.TRACEFS_MAGIC, unix.SECURITYFS_MAGIC, unix.DEVPTS_SUPER_MAGIC,
		unix.CGROUP_SUPER_MAGIC, unix.CGROUP2_SUPER_MAGIC, unix.BPF_FS_MAGIC,
		unix.EFIVARFS_MAGIC:
		return true
	}
	return false
}
