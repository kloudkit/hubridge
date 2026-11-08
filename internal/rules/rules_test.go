package rules

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kloudkit/hubridge/internal/home"
)

const token = "hubd_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type answer struct {
	status int
	etag   string
	body   string
}

type hubd struct {
	mu   sync.Mutex
	next answer
	seen []*http.Request
}

func (s *hubd) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = append(s.seen, r)
	if r.URL.Path != "/api/client/rules" {
		http.NotFound(w, r)
		return
	}
	if s.next.etag != "" {
		w.Header().Set("ETag", s.next.etag)
	}
	w.WriteHeader(s.next.status)
	_, _ = io.WriteString(w, s.next.body)
}

func (s *hubd) answer(status int, etag, body string) {
	s.mu.Lock()
	s.next = answer{status, etag, body}
	s.mu.Unlock()
}

func (s *hubd) last() *http.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen[len(s.seen)-1]
}

type fixture struct {
	srv    *hubd
	mirror *Mirror
	dir    string
	logs   *strings.Builder
}

func setup(t *testing.T) *fixture {
	t.Helper()
	srv := &hubd{}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	base := t.TempDir()
	h, err := home.Open(filepath.Join(base, "home"))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.WriteToken(token); err != nil {
		t.Fatal(err)
	}
	logs := &strings.Builder{}
	dir := filepath.Join(base, "claude", "rules", "hubd")
	return &fixture{srv: srv, mirror: New(ts.URL, "ws one", h, dir, slog.New(slog.NewTextHandler(logs, nil))), dir: dir, logs: logs}
}

func (f *fixture) refresh(t *testing.T) {
	t.Helper()
	if err := f.mirror.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) files(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func (f *fixture) read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (f *fixture) manifest(t *testing.T) Manifest {
	t.Helper()
	m, found, err := ReadManifest(f.mirror.Home.Path(ManifestFile))
	if err != nil || !found {
		t.Fatalf("manifest: found %v, err %v", found, err)
	}
	return m
}

func want(t *testing.T, got []string, names ...string) {
	t.Helper()
	if !slices.Equal(got, names) {
		t.Fatalf("want %v, got %v", names, got)
	}
}

func TestBuiltinsAreEmbedded(t *testing.T) {
	for name, title := range map[string]string{"estate": "# Estate\n", "secrets": "# Never print a secret value\n", "git": "# Other sessions share the checkouts"} {
		b := string(builtin(name))
		if !strings.HasPrefix(b, title) || !strings.HasSuffix(b, ".\n") || strings.HasSuffix(b, "\n\n") {
			t.Fatalf("%s.md: %q", name, b)
		}
	}
}

func TestOKWritesThenRemovesOnlyWhatWasDropped(t *testing.T) {
	f := setup(t)
	f.srv.answer(http.StatusOK, `"v1"`, `{"builtins":{"estate":true,"secrets":true,"git":false,"future":"x"},"files":[{"name":"pinned.md","body":"# Pinned\n"},{"name":"extra.md","body":"# Extra\n"}]}`)
	f.refresh(t)
	r := f.srv.last()
	if r.Header.Get("Authorization") != "Bearer "+token || r.URL.Query().Get("machine") != "ws one" || r.Header.Get("If-None-Match") != "" {
		t.Fatalf("first request: auth set %v, machine %q, If-None-Match %q", r.Header.Get("Authorization") != "", r.URL.Query().Get("machine"), r.Header.Get("If-None-Match"))
	}
	want(t, f.files(t), "estate.md", "extra.md", "pinned.md", "secrets.md")
	if f.read(t, "pinned.md") != "# Pinned\n" || f.read(t, "estate.md") != string(builtin("estate")) {
		t.Fatal("contents differ from the answer")
	}
	if st, _ := os.Stat(filepath.Join(f.dir, "pinned.md")); st.Mode().Perm() != 0o644 {
		t.Fatalf("file mode %o", st.Mode().Perm())
	}
	m := f.manifest(t)
	if m.ETag != `"v1"` || len(m.Files) != 4 || m.Files["pinned.md"] != digest([]byte("# Pinned\n")) {
		t.Fatalf("manifest %+v", m)
	}
	estate, _ := os.Stat(filepath.Join(f.dir, "estate.md"))

	f.srv.answer(http.StatusOK, `"v2"`, `{"builtins":{"git":false},"files":[{"name":"pinned.md","body":"# Pinned\n"}]}`)
	f.refresh(t)
	if f.srv.last().Header.Get("If-None-Match") != `"v1"` {
		t.Fatal("the stored ETag was not sent")
	}
	want(t, f.files(t), "estate.md", "pinned.md", "secrets.md")
	if again, _ := os.Stat(filepath.Join(f.dir, "estate.md")); !os.SameFile(estate, again) {
		t.Fatal("an unchanged file was rewritten")
	}
	if m := f.manifest(t); m.ETag != `"v2"` || len(m.Files) != 3 {
		t.Fatalf("manifest %+v", m)
	}

	f.srv.answer(http.StatusOK, `"v3"`, `{"builtins":{},"files":[]}`)
	f.refresh(t)
	want(t, f.files(t), "estate.md", "git.md", "secrets.md")
}

func TestNotModifiedWritesNothing(t *testing.T) {
	f := setup(t)
	f.srv.answer(http.StatusOK, `"v1"`, `{"files":[{"name":"pinned.md","body":"x"}]}`)
	f.refresh(t)
	before, _ := os.Stat(filepath.Join(f.dir, "pinned.md"))
	manifest, _ := os.Stat(f.mirror.Home.Path(ManifestFile))

	f.srv.answer(http.StatusNotModified, "", "")
	f.refresh(t)
	if f.srv.last().Header.Get("If-None-Match") != `"v1"` {
		t.Fatal("no If-None-Match")
	}
	after, _ := os.Stat(filepath.Join(f.dir, "pinned.md"))
	if m, _ := os.Stat(f.mirror.Home.Path(ManifestFile)); !os.SameFile(before, after) || !os.SameFile(manifest, m) {
		t.Fatal("a 304 rewrote a file")
	}
	want(t, f.files(t), "estate.md", "git.md", "pinned.md", "secrets.md")
}

func TestAMissingFileDropsTheETag(t *testing.T) {
	f := setup(t)
	f.srv.answer(http.StatusOK, `"v1"`, `{"files":[{"name":"pinned.md","body":"x"}]}`)
	f.refresh(t)
	if err := os.Remove(filepath.Join(f.dir, "pinned.md")); err != nil {
		t.Fatal(err)
	}
	f.refresh(t)
	if f.srv.last().Header.Get("If-None-Match") != "" {
		t.Fatal("If-None-Match sent with a listed file gone")
	}
	if f.read(t, "pinned.md") != "x" {
		t.Fatal("the deleted file was not restored")
	}
}

func TestNotFoundKeepsTheFiles(t *testing.T) {
	f := setup(t)
	f.srv.answer(http.StatusOK, `"v1"`, `{"builtins":{"estate":false},"files":[{"name":"pinned.md","body":"x"}]}`)
	f.refresh(t)
	want(t, f.files(t), "git.md", "pinned.md", "secrets.md")
	pinned, _ := os.Stat(filepath.Join(f.dir, "pinned.md"))
	manifest, _ := os.Stat(f.mirror.Home.Path(ManifestFile))
	f.srv.answer(http.StatusNotFound, "", "")
	if err := f.mirror.Refresh(context.Background()); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("a 404 returned %v", err)
	}
	want(t, f.files(t), "git.md", "pinned.md", "secrets.md")
	after, _ := os.Stat(filepath.Join(f.dir, "pinned.md"))
	if m, _ := os.Stat(f.mirror.Home.Path(ManifestFile)); !os.SameFile(pinned, after) || !os.SameFile(manifest, m) {
		t.Fatal("a 404 rewrote a file")
	}
	if m := f.manifest(t); m.ETag != `"v1"` || len(m.Files) != 3 {
		t.Fatalf("manifest %+v", m)
	}
}

func TestOtherAnswersKeepWhatIsOnDisk(t *testing.T) {
	f := setup(t)
	f.srv.answer(http.StatusOK, `"v1"`, `{"files":[{"name":"pinned.md","body":"x"}]}`)
	f.refresh(t)
	for _, a := range []answer{{http.StatusInternalServerError, "", ""}, {http.StatusUnauthorized, "", ""}, {http.StatusOK, `"v2"`, `{"files":`}, {http.StatusOK, `"v2"`, `{"builtins":{"git":"no"}}`}} {
		f.srv.answer(a.status, a.etag, a.body)
		if err := f.mirror.Refresh(context.Background()); err == nil || strings.Contains(err.Error(), token) {
			t.Fatalf("%d %s: err %v", a.status, a.body, err)
		}
		want(t, f.files(t), "estate.md", "git.md", "pinned.md", "secrets.md")
		if m := f.manifest(t); m.ETag != `"v1"` {
			t.Fatalf("manifest %+v", m)
		}
	}
}

func TestForeignFilesSurvive(t *testing.T) {
	f := setup(t)
	if err := os.MkdirAll(f.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"mine.md": "mine", "taken.md": "theirs"} {
		if err := os.WriteFile(filepath.Join(f.dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f.srv.answer(http.StatusOK, `"v1"`, `{"files":[{"name":"taken.md","body":"ours"},{"name":"pinned.md","body":"x"}]}`)
	f.refresh(t)
	if f.read(t, "taken.md") != "theirs" || f.read(t, "mine.md") != "mine" {
		t.Fatal("a file hubridge did not write was overwritten")
	}
	if _, ok := f.manifest(t).Files["taken.md"]; ok {
		t.Fatal("a foreign file was claimed")
	}
	if !strings.Contains(f.logs.String(), "taken.md") {
		t.Fatal("the skipped file was not logged")
	}
	f.srv.answer(http.StatusOK, `"v2"`, `{"builtins":{"estate":false,"secrets":false,"git":false},"files":[]}`)
	f.refresh(t)
	want(t, f.files(t), "mine.md", "taken.md")
}

func TestInvalidNamesAreSkipped(t *testing.T) {
	f := setup(t)
	f.srv.answer(http.StatusOK, `"v1"`, `{"files":[
		{"name":"../escape.md","body":"x"},{"name":"Upper.md","body":"x"},{"name":"-dash.md","body":"x"},
		{"name":"notes.txt","body":"x"},{"name":"a/b.md","body":"x"},{"name":"estate.md","body":"spoof"},
		{"name":"git.md","body":"spoof"},{"name":"ok-1.md","body":"first"},{"name":"ok-1.md","body":"second"}
	]}`)
	f.refresh(t)
	want(t, f.files(t), "estate.md", "git.md", "ok-1.md", "secrets.md")
	if f.read(t, "estate.md") != string(builtin("estate")) || f.read(t, "ok-1.md") != "first" {
		t.Fatal("an invalid entry was written")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(f.dir), "escape.md")); err == nil {
		t.Fatal("a name escaped the rules dir")
	}
	for _, name := range []string{"../escape.md", "Upper.md", "-dash.md", "notes.txt", "a/b.md", "estate.md", "git.md", "ok-1.md"} {
		if !strings.Contains(f.logs.String(), name) {
			t.Fatalf("no warning for %s", name)
		}
	}
}

func TestRefreshesNeverOverlap(t *testing.T) {
	f := setup(t)
	f.srv.answer(http.StatusOK, `"v1"`, `{}`)
	var mu sync.Mutex
	inflight, most := 0, 0
	slow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		inflight++
		most = max(most, inflight)
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		f.srv.ServeHTTP(w, r)
		mu.Lock()
		inflight--
		mu.Unlock()
	})
	ts := httptest.NewServer(slow)
	defer ts.Close()
	f.mirror.URL = ts.URL
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if err := f.mirror.Refresh(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if most != 1 {
		t.Fatalf("%d refreshes ran at once", most)
	}
}

func TestSeedWritesTheDefaultsOnce(t *testing.T) {
	f := setup(t)
	if err := f.mirror.Seed(); err != nil {
		t.Fatal(err)
	}
	want(t, f.files(t), "estate.md", "git.md", "secrets.md")
	if st, _ := os.Stat(f.dir); !st.IsDir() {
		t.Fatal("no rules dir")
	}
	if m := f.manifest(t); m.ETag != "" || len(m.Files) != 3 {
		t.Fatalf("manifest %+v", m)
	}
	f.srv.answer(http.StatusOK, `"v1"`, `{"builtins":{"git":false}}`)
	f.refresh(t)
	if err := f.mirror.Seed(); err != nil {
		t.Fatal(err)
	}
	want(t, f.files(t), "estate.md", "secrets.md")
	if len(f.srv.seen) != 1 {
		t.Fatalf("seed called hubd: %d requests", len(f.srv.seen))
	}
}
