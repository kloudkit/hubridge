package home

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/renameio/v2"

	"github.com/kloudkit/hubridge/internal/proc"
)

type Home struct {
	Dir       string
	StartTime func(pid int32) (int64, bool)
}

type Config struct {
	URL     string `json:"url"`
	Machine string `json:"machine"`
	Channel string `json:"channel"`
}

type Handover struct {
	PID     int32 `json:"pid"`
	Started int64 `json:"started"`
}

func Resolve(flag string) (string, error) {
	if flag != "" {
		return filepath.Abs(flag)
	}
	if env := os.Getenv("HUBD_HOME"); env != "" {
		return filepath.Abs(env)
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".config", "hubd"), nil
}

func Open(dir string) (*Home, error) {
	h := &Home{Dir: dir, StartTime: proc.StartTime}
	for _, d := range []string{dir, h.Path("sessions"), h.Path("queue")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
		if err := os.Chmod(d, 0o700); err != nil {
			return nil, err
		}
	}
	for _, f := range []string{"token", "config.json"} {
		if err := os.Chmod(h.Path(f), 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	return h, nil
}

func (h *Home) Path(parts ...string) string {
	return filepath.Join(append([]string{h.Dir}, parts...)...)
}

func WriteFile(path string, data []byte) error { return WriteFileMode(path, data, 0o600) }

func WriteFileMode(path string, data []byte, mode os.FileMode) error {
	f, err := renameio.NewPendingFile(path, renameio.WithStaticPermissions(mode))
	if err != nil {
		return err
	}
	defer f.Cleanup()
	if _, err := f.Write(data); err != nil {
		return err
	}
	return f.CloseAtomicallyReplace()
}

func (h *Home) Token() (string, error) {
	b, err := os.ReadFile(h.Path("token"))
	if err != nil {
		return "", err
	}
	t := strings.TrimSpace(string(b))
	if t == "" {
		return "", errors.New("token file is empty")
	}
	return t, nil
}

func (h *Home) WriteToken(token string) error {
	if err := WriteFile(h.Path("token"), []byte(token+"\n")); err != nil {
		return err
	}
	got, err := h.Token()
	if err != nil {
		return err
	}
	if got != token {
		return errors.New("token read back differs from the one written")
	}
	return nil
}

func (h *Home) Config() (Config, error) {
	var c Config
	b, err := os.ReadFile(h.Path("config.json"))
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("config.json: %w", err)
	}
	if c.Channel == "" {
		c.Channel = "bundled"
	}
	if c.URL == "" || c.Machine == "" {
		return c, errors.New("config.json needs url and machine")
	}
	return c, nil
}

func (h *Home) Handovers() map[string]Handover {
	type stored struct {
		PID     int32  `json:"pid"`
		Started *int64 `json:"started"`
	}
	out := map[string]Handover{}
	for id, raw := range readSessions[stored](h, "") {
		if raw.PID <= 0 {
			continue
		}
		ho := Handover{PID: raw.PID}
		if raw.Started != nil {
			ho.Started = *raw.Started
		} else if ms, ok := h.StartTime(raw.PID); ok {
			ho.Started = ms
			h.writeSession(id, "", ho)
		}
		out[id] = ho
	}
	return out
}

func readSessions[T any](h *Home, suffix string) map[string]T {
	out := map[string]T{}
	entries, _ := os.ReadDir(h.Path("sessions"))
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), suffix)
		if e.IsDir() || !ok || !ValidID(id) {
			continue
		}
		b, err := os.ReadFile(h.Path("sessions", e.Name()))
		if err != nil {
			continue
		}
		var v T
		if json.Unmarshal(b, &v) == nil {
			out[id] = v
		}
	}
	return out
}

func (h *Home) writeSession(id, suffix string, v any) error {
	if !ValidID(id) {
		return fmt.Errorf("bad session id %q", id)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return WriteFile(h.Path("sessions", id+suffix), b)
}

func (h *Home) clearSession(id, suffix string) {
	if ValidID(id) {
		os.Remove(h.Path("sessions", id+suffix))
	}
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

func ValidID(id string) bool { return len(id) <= 128 && idPattern.MatchString(id) }

func (h *Home) WriteConfig(c Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return WriteFile(h.Path("config.json"), append(b, '\n'))
}

func (h *Home) WriteHandover(id string, pid int32) error {
	return h.writeSession(id, "", map[string]int32{"pid": pid})
}

func (h *Home) MarkWaiting(id string, at time.Time) error {
	return h.writeSession(id, ".waiting", map[string]string{"at": at.UTC().Format(time.RFC3339Nano)})
}

func (h *Home) Waiting() map[string]time.Time {
	type stored struct {
		At time.Time `json:"at"`
	}
	out := map[string]time.Time{}
	for id, v := range readSessions[stored](h, ".waiting") {
		if !v.At.IsZero() {
			out[id] = v.At
		}
	}
	return out
}

type Background struct {
	At    time.Time      `json:"at"`
	Tasks map[string]int `json:"tasks,omitempty"`
	Crons int            `json:"crons,omitempty"`
}

func (h *Home) WriteBackground(id string, b Background) error {
	return h.writeSession(id, ".background", b)
}

func (h *Home) Backgrounds() map[string]Background {
	return readSessions[Background](h, ".background")
}

func (h *Home) ClearBackground(id string) { h.clearSession(id, ".background") }

func (h *Home) ClearWaiting(id string) { h.clearSession(id, ".waiting") }
