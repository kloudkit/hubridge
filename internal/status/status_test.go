package status

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func writer(t *testing.T, log *slog.Logger) (*Writer, *clock) {
	t.Helper()
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	c := &clock{now: time.Date(2026, 11, 8, 11, 0, 0, 500, time.FixedZone("IST", 2*3600))}
	w := New(filepath.Join(t.TempDir(), "claude", "hubd", "status.json"), "ws", "https://op:hunter2@hubd.example.com", "v0.4.0", log)
	w.Now = c.Now
	return w, c
}

func fields(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, b)
	}
	return m
}

func only(t *testing.T, dir string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%s holds %v, want %v", dir, got, want)
	}
}

func TestTheFileHasTheAgreedShape(t *testing.T) {
	w, _ := writer(t, nil)
	w.Offline("starting")
	m := fields(t, w.Path)
	want := map[string]any{
		"state": "offline", "since": "2026-11-08T09:00:00Z", "machine": "ws",
		"url": "https://hubd.example.com", "version": "v0.4.0", "error": "starting",
	}
	if !maps.Equal(m, want) {
		t.Fatalf("got %v, want %v", m, want)
	}
	w.Connected()
	m = fields(t, w.Path)
	delete(want, "error")
	want["state"] = "connected"
	if !maps.Equal(m, want) {
		t.Fatalf("got %v, want %v", m, want)
	}

	st, err := os.Stat(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o644 {
		t.Fatalf("file mode %o", st.Mode().Perm())
	}
	dir, _ := os.Stat(filepath.Dir(w.Path))
	if dir.Mode().Perm() != 0o755 {
		t.Fatalf("dir mode %o", dir.Mode().Perm())
	}
	only(t, filepath.Dir(w.Path), "status.json")
}

func TestSinceMovesOnlyWhenTheStateChanges(t *testing.T) {
	w, c := writer(t, nil)
	w.Offline("starting")
	c.add(time.Minute)
	w.Offline("connection refused")
	s, err := Read(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)
	if s.Error != "connection refused" || !s.Since.Equal(start) {
		t.Fatalf("a new reason should keep since: %+v", s)
	}

	if err := os.WriteFile(w.Path, []byte("sentinel"), 0o644); err != nil {
		t.Fatal(err)
	}
	c.add(time.Minute)
	w.Offline("connection refused")
	if b, _ := os.ReadFile(w.Path); string(b) != "sentinel" {
		t.Fatalf("rewritten while the state held: %s", b)
	}

	c.add(time.Minute)
	w.Connected()
	s, _ = Read(w.Path)
	if s.State != Connected || s.Error != "" || !s.Since.Equal(start.Add(3*time.Minute)) {
		t.Fatalf("connect: %+v", s)
	}
	c.add(time.Minute)
	w.Connected()
	if s2, _ := Read(w.Path); s2 != s {
		t.Fatalf("a second welcome changed the file: %+v", s2)
	}
	c.add(time.Minute)
	w.Offline("connection lost")
	s, _ = Read(w.Path)
	if s.State != Offline || s.Error != "connection lost" || !s.Since.Equal(start.Add(5*time.Minute)) {
		t.Fatalf("drop: %+v", s)
	}
}

func TestTheReasonCarriesNoSecret(t *testing.T) {
	w, _ := writer(t, nil)
	tok := "hubd_" + strings.Repeat("x", 43)
	w.Offline("closed by hubd: bad " + tok + " for https://op:hunter2@hubd.example.com")
	b, _ := os.ReadFile(w.Path)
	if strings.Contains(string(b), tok) || strings.Contains(string(b), "hunter2") {
		t.Fatalf("a secret reached the status file: %s", b)
	}
}

func TestAFailedWriteWarnsAndLeavesNothingBehind(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only dir")
	}
	var logs bytes.Buffer
	w, _ := writer(t, slog.New(slog.NewTextHandler(&logs, nil)))
	w.Connected()
	dir := filepath.Dir(w.Path)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	w.Offline("connection lost")
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "write the status file") {
		t.Fatalf("no warning for a failed write: %q", logs.String())
	}
	if s, _ := Read(w.Path); s.State != Connected {
		t.Fatalf("a failed write changed the file: %+v", s)
	}
	only(t, dir, "status.json")

	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	w.Offline("connection lost")
	if s, _ := Read(w.Path); s.State != Offline || s.Error != "connection lost" {
		t.Fatalf("the failed write was not retried: %+v", s)
	}
}

func TestReadersNeverSeeAPartialFile(t *testing.T) {
	w, c := writer(t, nil)
	w.Offline("starting")
	stop := make(chan struct{})
	bad := make(chan string, 1)
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			b, err := os.ReadFile(w.Path)
			if err != nil {
				select {
				case bad <- "read: " + err.Error():
				default:
				}
				return
			}
			var s Status
			if json.Unmarshal(b, &s) != nil || s.Machine != "ws" {
				select {
				case bad <- string(b):
				default:
				}
				return
			}
		}
	})
	for i := range 40 {
		c.add(time.Second)
		if i%2 == 0 {
			w.Connected()
		} else {
			w.Offline(strings.Repeat("connection lost ", i%7+1))
		}
	}
	close(stop)
	wg.Wait()
	select {
	case b := <-bad:
		t.Fatalf("a reader saw a partial or missing file: %q", b)
	default:
	}
	only(t, filepath.Dir(w.Path), "status.json")
}

func TestANilWriterDoesNothing(t *testing.T) {
	var w *Writer
	w.Connected()
	w.Offline("stopped")
}
