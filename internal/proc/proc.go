package proc

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/shirou/gopsutil/v4/process"
)

const startSlackMs = 1000

func StatStart(pid int32) (string, bool) {
	if pid <= 0 {
		return "", false
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", false
	}
	s := string(b)
	// The command name may hold spaces and parentheses, so fields count from the last ')'.
	i := strings.LastIndexByte(s, ')')
	if i < 0 || i+2 > len(s) {
		return "", false
	}
	f := strings.Fields(s[i+2:])
	if len(f) < 20 {
		return "", false
	}
	return f[19], true
}

func Domain() string {
	id, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		return ""
	}
	ns, err := os.Readlink("/proc/self/ns/pid")
	if err != nil {
		return ""
	}
	return "linux:" + strings.TrimSpace(string(id)) + ":" + ns
}

func Running(pid int32) bool {
	if pid <= 0 {
		return false
	}
	ok, err := process.PidExists(pid)
	return err == nil && ok
}

func StartTime(pid int32) (int64, bool) {
	p, err := process.NewProcess(pid)
	if err != nil {
		return 0, false
	}
	ms, err := p.CreateTime()
	return ms, err == nil
}

func Alive(pid int32, started int64) bool {
	if !Running(pid) {
		return false
	}
	ms, ok := StartTime(pid)
	if !ok {
		return false
	}
	if started < 100_000_000_000 {
		started *= 1000
	}
	d := ms - started
	return d >= -startSlackMs && d <= startSlackMs
}

type Info struct {
	PPID  int32
	Name  string
	Argv0 string
}

func Lookup(pid int32) (Info, bool) {
	p, err := process.NewProcess(pid)
	if err != nil {
		return Info{}, false
	}
	var i Info
	i.PPID, _ = p.Ppid()
	i.Name, _ = p.Name()
	if args, err := p.CmdlineSlice(); err == nil && len(args) > 0 {
		i.Argv0 = args[0]
	}
	return i, true
}

func Claude() (string, error) {
	if p, err := exec.LookPath("claude"); err == nil {
		return p, nil
	}
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".local", "bin", "claude")
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", exec.ErrNotFound
}
