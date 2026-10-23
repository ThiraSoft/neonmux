package remote

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// ensureDir creates dir with mode 0700 if needed and checks that it is ours
// and has strict 0700 permissions, matching the socket directory checks.
func ensureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var st unix.Stat_t
	if err := unix.Lstat(dir, &st); err != nil {
		return err
	}
	switch {
	case st.Mode&unix.S_IFMT != unix.S_IFDIR:
		return fmt.Errorf("%s is not a directory", dir)
	case int(st.Uid) != os.Getuid():
		return fmt.Errorf("%s belongs to uid %d, not to us", dir, st.Uid)
	case st.Mode&0o777 != 0o700:
		return fmt.Errorf("%s has mode %o, it must be 700", dir, st.Mode&0o777)
	}
	return nil
}

// atomicWriteFile writes data to path atomically by creating a temporary file
// in the same directory, fsyncing it, closing it, and renaming it into place.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := ensureDir(dir); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}
