package rules

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestOKWritesThenRemovesOnlyWhatWasDropped(t *testing.T) {
	f := setup(t)
	f.srv.answer(http.StatusOK, `"v1"`, `{"files":[{"name":"pinned.md","body":"# Pinned\n"},{"name":"extra.md","body":"# Extra\n"}]}`)
	f.refresh(t)
	r := f.srv.last()
	if r.Header.Get("Authorization") != "Bearer "+token || r.URL.Query().Get("machine") != "ws one" || r.Header.Get("If-None-Match") != "" {
		t.Fatalf("first request: auth set %v, machine %q, If-None-Match %q", r.Header.Get("Authorization") != "", r.URL.Query().Get("machine"), r.Header.Get("If-None-Match"))
	}
	want(t, f.files(t), "extra.md", "pinned.md")
	if f.read(t, "pinned.md") != "# Pinned\n" || f.read(t, "extra.md") != "# Extra\n" {
		t.Fatal("contents differ from the answer")
	}
	if st, _ := os.Stat(filepath.Join(f.dir, "pinned.md")); st.Mode().Perm() != 0o644 {
		t.Fatalf("file mode %o", st.Mode().Perm())
	}
	m := f.manifest(t)
	if e := m.Files["pinned.md"]; m.ETag != `"v1"` || len(m.Files) != 2 || e.SHA256 != digest([]byte("# Pinned\n")) || e.Body == nil || *e.Body != "# Pinned\n" {
		t.Fatalf("manifest %+v", m)
	}
	pinned, _ := os.Stat(filepath.Join(f.dir, "pinned.md"))

	f.srv.answer(http.StatusOK, `"v2"`, `{"files":[{"name":"pinned.md","body":"# Pinned\n"}]}`)
	f.refresh(t)
	if f.srv.last().Header.Get("If-None-Match") != `"v1"` {
		t.Fatal("the stored ETag was not sent")
	}
	want(t, f.files(t), "pinned.md")
	if again, _ := os.Stat(filepath.Join(f.dir, "pinned.md")); !os.SameFile(pinned, again) {
		t.Fatal("an unchanged file was rewritten")
	}
	if m := f.manifest(t); m.ETag != `"v2"` || len(m.Files) != 1 {
		t.Fatalf("manifest %+v", m)
	}

	for i, body := range []string{`{"files":[{"name":"pinned.md","body":"x"}]}`, `{"files":[]}`, `{"files":[{"name":"pinned.md","body":"x"}]}`, `{"files":null}`, `{"files":[{"name":"pinned.md","body":"x"}]}`, `{}`} {
		f.srv.answer(http.StatusOK, fmt.Sprintf(`"v%d"`, i+3), body)
		f.refresh(t)
		if i%2 == 0 {
			want(t, f.files(t), "pinned.md")
			continue
		}
		want(t, f.files(t))
		if m := f.manifest(t); len(m.Files) != 0 {
			t.Fatalf("%s: manifest %+v", body, m)
		}
	}
}

func TestTheBuiltinsKeyIsIgnored(t *testing.T) {
	f := setup(t)
	f.srv.answer(http.StatusOK, `"v1"`, `{"builtins":{"estate":true,"secrets":false,"git":"no","future":{}},"files":[{"name":"estate.md","body":"mine"}]}`)
	f.refresh(t)
	want(t, f.files(t), "estate.md")
	if f.read(t, "estate.md") != "mine" {
		t.Fatal("estate.md is not the body hubd sent")
	}
	f.srv.answer(http.StatusOK, `"v2"`, `{"builtins":"anything"}`)
	f.refresh(t)
	want(t, f.files(t))
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
	want(t, f.files(t), "pinned.md")
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
	f.srv.answer(http.StatusOK, `"v1"`, `{"files":[{"name":"pinned.md","body":"x"},{"name":"extra.md","body":"y"}]}`)
	f.refresh(t)
	pinned, _ := os.Stat(filepath.Join(f.dir, "pinned.md"))
	manifest, _ := os.Stat(f.mirror.Home.Path(ManifestFile))
	f.srv.answer(http.StatusNotFound, "", "404 page not found")
	if err := f.mirror.Refresh(context.Background()); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("a 404 returned %v", err)
	}
	want(t, f.files(t), "extra.md", "pinned.md")
	after, _ := os.Stat(filepath.Join(f.dir, "pinned.md"))
	if m, _ := os.Stat(f.mirror.Home.Path(ManifestFile)); !os.SameFile(pinned, after) || !os.SameFile(manifest, m) {
		t.Fatal("a 404 rewrote a file")
	}
	if m := f.manifest(t); m.ETag != `"v1"` || len(m.Files) != 2 {
		t.Fatalf("manifest %+v", m)
	}
}

func TestOtherAnswersKeepWhatIsOnDisk(t *testing.T) {
	f := setup(t)
	f.srv.answer(http.StatusOK, `"v1"`, `{"files":[{"name":"pinned.md","body":"x"}]}`)
	f.refresh(t)
	for _, a := range []answer{
		{http.StatusInternalServerError, "", ""}, {http.StatusBadGateway, "", "<html>bad gateway</html>"}, {http.StatusUnauthorized, "", ""},
		{http.StatusOK, `"v2"`, `{"files":`}, {http.StatusOK, `"v2"`, ``}, {http.StatusOK, `"v2"`, `null`}, {http.StatusOK, `"v2"`, `[]`},
		{http.StatusOK, `"v2"`, `<html>maintenance</html>`}, {http.StatusOK, `"v2"`, `{"files":{"pinned.md":"x"}}`},
	} {
		f.srv.answer(a.status, a.etag, a.body)
		if err := f.mirror.Refresh(context.Background()); err == nil || strings.Contains(err.Error(), token) {
			t.Fatalf("%d %s: err %v", a.status, a.body, err)
		}
		want(t, f.files(t), "pinned.md")
		if m := f.manifest(t); m.ETag != `"v1"` || len(m.Files) != 1 {
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
	f.srv.answer(http.StatusOK, `"v2"`, `{"files":[]}`)
	f.refresh(t)
	want(t, f.files(t), "mine.md", "taken.md")
}

func TestInvalidNamesAreSkipped(t *testing.T) {
	f := setup(t)
	f.srv.answer(http.StatusOK, `"v1"`, `{"files":[
		{"name":"../escape.md","body":"x"},{"name":"Upper.md","body":"x"},{"name":"-dash.md","body":"x"},
		{"name":"notes.txt","body":"x"},{"name":"a/b.md","body":"x"},{"name":"","body":"x"},{"name":".md","body":"x"},
		{"name":"estate.md","body":"named like an old built-in"},{"name":"ok-1.md","body":"first"},{"name":"ok-1.md","body":"second"}
	]}`)
	f.refresh(t)
	want(t, f.files(t), "estate.md", "ok-1.md")
	if f.read(t, "estate.md") != "named like an old built-in" || f.read(t, "ok-1.md") != "first" {
		t.Fatal("an invalid entry was written")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(f.dir), "escape.md")); err == nil {
		t.Fatal("a name escaped the rules dir")
	}
	for _, name := range []string{"../escape.md", "Upper.md", "-dash.md", "notes.txt", "a/b.md", ".md", "ok-1.md"} {
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

func (f *fixture) write(t *testing.T, name, body string) {
	t.Helper()
	if err := os.MkdirAll(f.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) restart() *Mirror {
	return New("http://127.0.0.1:1", f.mirror.Machine, f.mirror.Home, f.dir, f.mirror.Log)
}

func TestRestoreRewritesListedFilesWithoutHubd(t *testing.T) {
	f := setup(t)
	f.srv.answer(http.StatusOK, `"v1"`, `{"files":[{"name":"gone.md","body":"# Gone\n"},{"name":"edited.md","body":"# Edited\n"},{"name":"kept.md","body":"# Kept\n"},{"name":"empty.md","body":""}]}`)
	f.refresh(t)
	requests := len(f.srv.seen)
	manifest, _ := os.Stat(f.mirror.Home.Path(ManifestFile))
	kept, _ := os.Stat(filepath.Join(f.dir, "kept.md"))
	if err := os.Remove(filepath.Join(f.dir, "gone.md")); err != nil {
		t.Fatal(err)
	}
	f.write(t, "edited.md", "# Edited by hand\n")
	f.write(t, "foreign.md", "not hubridge's")
	f.logs.Reset()

	if err := f.restart().Restore(); err != nil {
		t.Fatal(err)
	}
	want(t, f.files(t), "edited.md", "empty.md", "foreign.md", "gone.md", "kept.md")
	if f.read(t, "gone.md") != "# Gone\n" || f.read(t, "edited.md") != "# Edited\n" || f.read(t, "foreign.md") != "not hubridge's" {
		t.Fatal("restore did not bring back the stored bodies, or touched a foreign file")
	}
	if again, _ := os.Stat(filepath.Join(f.dir, "kept.md")); !os.SameFile(kept, again) {
		t.Fatal("restore rewrote an intact file")
	}
	if m, _ := os.Stat(f.mirror.Home.Path(ManifestFile)); !os.SameFile(manifest, m) {
		t.Fatal("restore rewrote the manifest")
	}
	if logs := f.logs.String(); !strings.Contains(logs, "files=\"[edited.md gone.md]\"") {
		t.Fatalf("the restored files were not logged: %s", logs)
	}

	if err := os.RemoveAll(f.dir); err != nil {
		t.Fatal(err)
	}
	if err := f.restart().Restore(); err != nil {
		t.Fatal(err)
	}
	want(t, f.files(t), "edited.md", "empty.md", "gone.md", "kept.md")
	if st, _ := os.Stat(filepath.Join(f.dir, "kept.md")); st.Mode().Perm() != 0o644 {
		t.Fatalf("file mode %o", st.Mode().Perm())
	}
	if len(f.srv.seen) != requests {
		t.Fatalf("restore called hubd: %d requests", len(f.srv.seen)-requests)
	}

	f.srv.answer(http.StatusNotModified, "", "")
	f.refresh(t)
	if f.srv.last().Header.Get("If-None-Match") != `"v1"` {
		t.Fatal("a restored dir did not send the stored ETag")
	}
}

func TestRestoreWithoutAManifestDoesNothing(t *testing.T) {
	f := setup(t)
	if err := f.mirror.Restore(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.dir); err == nil {
		t.Fatal("restore made the rules dir with no manifest")
	}
	if _, found, _ := ReadManifest(f.mirror.Home.Path(ManifestFile)); found {
		t.Fatal("restore wrote a manifest")
	}
}

func TestRestoreSkipsNamesOutsideTheDir(t *testing.T) {
	f := setup(t)
	if err := home.WriteFile(f.mirror.Home.Path(ManifestFile), []byte(`{"etag":"","files":{"../escape.md":{"sha256":"x","body":"x"}}}`)); err != nil {
		t.Fatal(err)
	}
	if err := f.mirror.Restore(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(f.dir), "escape.md")); err == nil {
		t.Fatal("a name escaped the rules dir")
	}
}

func TestAV020ManifestIsRead(t *testing.T) {
	f := setup(t)
	f.write(t, "pinned.md", "x")
	f.write(t, "dropped.md", "y")
	legacy := fmt.Sprintf(`{"etag":"\"v1\"","files":{"pinned.md":%q,"dropped.md":%q}}`, digest([]byte("x")), digest([]byte("y")))
	if err := home.WriteFile(f.mirror.Home.Path(ManifestFile), []byte(legacy)); err != nil {
		t.Fatal(err)
	}
	m := f.manifest(t)
	if e := m.Files["pinned.md"]; m.ETag != `"v1"` || e.SHA256 != digest([]byte("x")) || e.Body != nil {
		t.Fatalf("manifest %+v", m)
	}
	if got := m.Unrestorable(); !slices.Equal(got, []string{"dropped.md", "pinned.md"}) {
		t.Fatalf("unrestorable %v", got)
	}

	if err := os.Remove(filepath.Join(f.dir, "pinned.md")); err != nil {
		t.Fatal(err)
	}
	if err := f.restart().Restore(); err != nil {
		t.Fatal(err)
	}
	want(t, f.files(t), "dropped.md")
	if !strings.Contains(f.logs.String(), "pinned.md") {
		t.Fatal("the file restore could not write was not logged")
	}
	f.write(t, "pinned.md", "x")
	pinned, _ := os.Stat(filepath.Join(f.dir, "pinned.md"))

	f.srv.answer(http.StatusOK, `"v1"`, `{"files":[{"name":"pinned.md","body":"x"}]}`)
	f.refresh(t)
	if f.srv.last().Header.Get("If-None-Match") != "" {
		t.Fatal("If-None-Match sent while the manifest keeps no bodies")
	}
	want(t, f.files(t), "pinned.md")
	if again, _ := os.Stat(filepath.Join(f.dir, "pinned.md")); !os.SameFile(pinned, again) {
		t.Fatal("an unchanged file was rewritten")
	}
	raw, err := os.ReadFile(f.mirror.Home.Path(ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	var stored struct {
		ETag  string `json:"etag"`
		Files map[string]struct {
			SHA256 string `json:"sha256"`
			Body   string `json:"body"`
		} `json:"files"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("the manifest is not in the new shape: %v", err)
	}
	if e := stored.Files["pinned.md"]; stored.ETag != `"v1"` || len(stored.Files) != 1 || e.SHA256 != digest([]byte("x")) || e.Body != "x" {
		t.Fatalf("manifest %s", raw)
	}

	f.srv.answer(http.StatusNotModified, "", "")
	f.refresh(t)
	if f.srv.last().Header.Get("If-None-Match") != `"v1"` {
		t.Fatal("the ETag is not sent once the manifest keeps the bodies")
	}
}
