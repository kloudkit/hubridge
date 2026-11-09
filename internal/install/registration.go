package install

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kloudkit/hubridge/internal/home"
	"github.com/kloudkit/hubridge/internal/proc"
)

const (
	RegUser     = "user"
	RegShadowed = "shadowed"
	RegMissing  = "missing"
	RegRepaired = "repaired"
	RegUnknown  = "unknown"
)

type Registration struct {
	State string    `json:"state"`
	Scope string    `json:"scope,omitempty"`
	Cwd   string    `json:"cwd,omitempty"`
	At    time.Time `json:"at"`
}

func (r Registration) Reason() string {
	switch r.State {
	case RegShadowed:
		return "shadowed by " + r.Scope + " config"
	case RegMissing:
		return "not registered"
	case RegUnknown:
		return "registration unknown"
	}
	return ""
}

func RegistrationDir() (string, error) {
	dir, err := ClaudeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "hubd", "mcp"), nil
}

func RegistrationPath(sessionID string) (string, error) {
	if !home.ValidID(sessionID) {
		return "", fmt.Errorf("session id %q", sessionID)
	}
	dir, err := RegistrationDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, sessionID+".json"), nil
}

func CheckRegistration(ctx context.Context, cwd string) Registration {
	r := Registration{State: RegUnknown, Cwd: cwd, At: time.Now().UTC().Truncate(time.Second)}
	claude, err := proc.Claude()
	if err != nil {
		return r
	}
	cmd := exec.CommandContext(ctx, claude, "mcp", "get", mcpName)
	cmd.Dir = cwd
	out, err := cmd.CombinedOutput()
	text := string(out)
	if err != nil {
		if notFound(out) {
			r.State = RegMissing
		}
		return r
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Scope:") {
			continue
		}
		scope := strings.ToLower(strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "Scope:")))[0])
		r.Scope = scope
		if scope == RegUser {
			r.State = RegUser
		} else {
			r.State = RegShadowed
		}
		return r
	}
	return r
}

func EnsureRegistration(ctx context.Context, h *home.Home, cwd, sessionID string, out io.Writer) (Registration, error) {
	r := CheckRegistration(ctx, cwd)
	if r.State == RegMissing {
		if err := RegisterMCP(h.Dir, out); err == nil {
			r.State = RegRepaired
		}
	}
	path, err := RegistrationPath(sessionID)
	if err != nil {
		return r, err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return r, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return r, err
	}
	return r, os.WriteFile(path, append(b, '\n'), 0o644)
}

func ClearRegistration(sessionID string) {
	if path, err := RegistrationPath(sessionID); err == nil {
		os.Remove(path)
	}
}
