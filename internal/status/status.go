package status

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/renameio/v2"

	"github.com/kloudkit/hubridge/internal/install"
	"github.com/kloudkit/hubridge/internal/mask"
)

const (
	Connected = "connected"
	Offline   = "offline"
)

type Status struct {
	State   string    `json:"state"`
	Since   time.Time `json:"since"`
	Machine string    `json:"machine"`
	URL     string    `json:"url"`
	Version string    `json:"version"`
	Error   string    `json:"error,omitempty"`
}

func Path() (string, error) {
	dir, err := install.ClaudeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "hubd", "status.json"), nil
}

func Read(path string) (Status, error) {
	var s Status
	b, err := os.ReadFile(path)
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

type Writer struct {
	Path    string
	Machine string
	URL     string
	Version string
	Log     *slog.Logger
	Now     func() time.Time

	mu      sync.Mutex
	last    Status
	written bool
}

func New(path, machine, base, version string, log *slog.Logger) *Writer {
	return &Writer{Path: path, Machine: machine, URL: bare(base), Version: version, Log: log, Now: time.Now}
}

func bare(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}
	u.User = nil
	return u.String()
}

func (w *Writer) Connected() { w.set(Connected, "") }

func (w *Writer) Offline(reason string) { w.set(Offline, mask.String(reason)) }

func (w *Writer) set(state, reason string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.written && w.last.State == state && w.last.Error == reason {
		return
	}
	since := w.last.Since
	if w.last.State != state {
		since = w.Now().UTC().Truncate(time.Second)
	}
	w.last = Status{State: state, Since: since, Machine: w.Machine, URL: w.URL, Version: w.Version, Error: reason}
	w.written = w.write() == nil
}

func (w *Writer) write() error {
	b, err := json.MarshalIndent(w.last, "", "  ")
	if err == nil {
		err = writeFile(w.Path, append(b, '\n'))
	}
	if err != nil {
		w.Log.Warn("write the status file", "path", w.Path, "err", err)
	}
	return err
}

func writeFile(path string, body []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := renameio.NewPendingFile(path, renameio.WithTempDir(dir), renameio.WithStaticPermissions(0o644))
	if err != nil {
		return err
	}
	defer func() { _ = f.Cleanup() }()
	if _, err := f.Write(body); err != nil {
		return err
	}
	return f.CloseAtomicallyReplace()
}
