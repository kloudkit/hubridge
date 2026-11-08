package doctor

import (
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kloudkit/hubridge/internal/status"
)

func TestCheckStatus(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	path := filepath.Join(dir, "hubd", "status.json")
	at := time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)
	w := status.New(path, "ws", "https://hubd.example.com", "v0.4.0", slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.Now = func() time.Time { return at }

	for _, c := range []struct {
		write func()
		level Level
		want  string
	}{
		{func() {}, Info, path + ": not written yet"},
		{func() { w.Offline("connection refused") }, Info, path + ": offline for 1m30s: connection refused"},
		{w.Connected, OK, path + ": connected to https://hubd.example.com for 1m30s"},
	} {
		c.write()
		r := &Report{}
		CheckStatus(r, at.Add(90*time.Second))
		if len(r.Lines) != 1 || r.Lines[0].Level != c.level || r.Lines[0].Area != "status" || !strings.HasPrefix(r.Lines[0].Text, c.want) {
			t.Fatalf("got %+v, want %s %q", r.Lines, labels[c.level], c.want)
		}
	}
}
