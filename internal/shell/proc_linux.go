package shell

import (
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// tcgetpgrp takes the master's raw number: os.File.Fd would put the
// descriptor back in blocking mode.
func tcgetpgrp(fd int) (int, error) {
	return unix.IoctlGetInt(fd, unix.TIOCGPGRP)
}

func procName(pid int) string {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func procCwd(pid int) string {
	if pid <= 0 {
		return ""
	}
	d, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/cwd")
	if err != nil {
		return ""
	}
	return d
}

func procArgs(pid int) []string {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil || len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
}

// loginShell reads the login shell from /etc/passwd.
func loginShell() string {
	uid := strconv.Itoa(os.Getuid())
	data, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(data), "\n") {
		f := strings.Split(l, ":")
		if len(f) >= 7 && f[2] == uid {
			return f[6]
		}
	}
	return ""
}

// Proc is an entry of the process table.
type Proc struct {
	Pid, Ppid, Pgid int
	Comm            string
}

// ListProcs reads the process table from /proc.
func ListProcs() ([]Proc, error) {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var out []Proc
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		s := string(b)
		// "pid (comm) state ppid pgrp …": comm may contain spaces and parentheses.
		l, r := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
		if l < 0 || r < l {
			continue
		}
		f := strings.Fields(s[r+1:])
		if len(f) < 3 {
			continue
		}
		ppid, _ := strconv.Atoi(f[1])
		pgid, _ := strconv.Atoi(f[2])
		out = append(out, Proc{Pid: pid, Ppid: ppid, Pgid: pgid, Comm: s[l+1 : r]})
	}
	return out, nil
}

// ProcArgs exposes a process's argv (forced restart).
func ProcArgs(pid int) []string { return procArgs(pid) }
