package shell

import (
	"bytes"
	"encoding/binary"
	"os/exec"
	"os/user"
	"strings"
	"unsafe"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"
)

// tcgetpgrp takes the master's raw number: os.File.Fd would put the
// descriptor back in blocking mode.
func tcgetpgrp(fd int) (int, error) {
	return unix.IoctlGetInt(fd, unix.TIOCGPGRP)
}

// procName reads the process name through sysctl, without running a command.
func procName(pid int) string {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return ""
	}
	name := k.Proc.P_comm[:]
	if i := bytes.IndexByte(name, 0); i >= 0 {
		name = name[:i]
	}
	return string(name)
}

// proc_pidinfo from libproc, called through purego (no cgo): reading the
// current directory of a process takes a few microseconds, against
// ~30 ms for lsof.
var procPidinfo func(pid int32, flavor int32, arg uint64, buf unsafe.Pointer, size int32) int32

func init() {
	if lib, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL); err == nil {
		purego.RegisterLibFunc(&procPidinfo, lib, "proc_pidinfo")
	}
}

const (
	procPidVnodePathInfo = 9    // PROC_PIDVNODEPATHINFO
	vnodePathInfoSize    = 2352 // sizeof(struct proc_vnodepathinfo)
	cdirPathOffset       = 152  // pvi_cdir.vip_path
)

func procCwd(pid int) string {
	if pid <= 0 || procPidinfo == nil {
		return ""
	}
	var buf [vnodePathInfoSize]byte
	if procPidinfo(int32(pid), procPidVnodePathInfo, 0, unsafe.Pointer(&buf[0]), vnodePathInfoSize) <= 0 {
		return ""
	}
	path := buf[cdirPathOffset : cdirPathOffset+1024]
	if i := bytes.IndexByte(path, 0); i >= 0 {
		path = path[:i]
	}
	return string(path)
}

// procArgs reads argv through kern.procargs2: argc, executable path,
// padding, then the arguments separated by zero bytes.
func procArgs(pid int) []string {
	buf, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(buf) < 4 {
		return nil
	}
	argc := int(binary.LittleEndian.Uint32(buf))
	rest := buf[4:]
	if i := bytes.IndexByte(rest, 0); i >= 0 {
		rest = rest[i:]
	}
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}
	var args []string
	for len(args) < argc && len(rest) > 0 {
		j := bytes.IndexByte(rest, 0)
		if j < 0 {
			args = append(args, string(rest))
			break
		}
		args = append(args, string(rest[:j]))
		rest = rest[j+1:]
	}
	return args
}

// loginShell reads the login shell from the local directory (dscl).
func loginShell() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	out, err := exec.Command("dscl", ".", "-read", "/Users/"+u.Username, "UserShell").Output()
	if err != nil {
		return ""
	}
	_, sh, ok := strings.Cut(strings.TrimSpace(string(out)), ": ")
	if !ok {
		return ""
	}
	return sh
}

// Proc is an entry of the process table.
type Proc struct {
	Pid, Ppid, Pgid int
	Comm            string
}

// ListProcs reads the process table (sysctl kern.proc.all).
func ListProcs() ([]Proc, error) {
	ks, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	out := make([]Proc, 0, len(ks))
	for _, k := range ks {
		name := k.Proc.P_comm[:]
		if i := bytes.IndexByte(name, 0); i >= 0 {
			name = name[:i]
		}
		out = append(out, Proc{Pid: int(k.Proc.P_pid), Ppid: int(k.Eproc.Ppid), Pgid: int(k.Eproc.Pgid), Comm: string(name)})
	}
	return out, nil
}

// ProcArgs exposes a process's argv (forced restart).
func ProcArgs(pid int) []string { return procArgs(pid) }
