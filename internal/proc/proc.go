package proc

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/shirou/gopsutil/v4/process"
)

func StatStart(pid int32) (string, bool) {
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
	ok, err := process.PidExists(pid)
	return err == nil && ok
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
