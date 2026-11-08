package rules

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"time"

	"github.com/google/renameio/v2"

	"github.com/kloudkit/hubridge/internal/home"
	"github.com/kloudkit/hubridge/internal/install"
)

const (
	ManifestFile = "rules.json"
	maxBody      = 4 << 20
)

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*\.md$`)

type File struct {
	Name string `json:"name"`
	Body string `json:"body"`
}

type Manifest struct {
	ETag  string           `json:"etag"`
	Files map[string]Entry `json:"files"`
}

type Entry struct {
	SHA256 string  `json:"sha256"`
	Body   *string `json:"body,omitempty"`
}

// UnmarshalJSON also reads a v0.2.0 entry, which is a bare sha256 with no body.
func (e *Entry) UnmarshalJSON(b []byte) error {
	var sum string
	if json.Unmarshal(b, &sum) == nil {
		*e = Entry{SHA256: sum}
		return nil
	}
	type plain Entry
	return json.Unmarshal(b, (*plain)(e))
}

func entry(body []byte) Entry {
	s := string(body)
	return Entry{SHA256: digest(body), Body: &s}
}

func (e Entry) same(o Entry) bool {
	return e.SHA256 == o.SHA256 && (e.Body == nil) == (o.Body == nil)
}

func (m Manifest) Unrestorable() []string {
	var out []string
	for _, name := range slices.Sorted(maps.Keys(m.Files)) {
		if m.Files[name].Body == nil {
			out = append(out, name)
		}
	}
	return out
}

type Answer struct {
	Changed bool
	ETag    string
	Files   []File
}

func Dir() (string, error) {
	dir, err := install.ClaudeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "rules", "hubd"), nil
}

func Fetch(ctx context.Context, c *http.Client, base, machine, token, etag string, log *slog.Logger) (Answer, error) {
	u := base + "/api/client/rules?" + url.Values{"machine": {machine}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Answer{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := c.Do(req)
	if err != nil {
		return Answer{}, err
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err := errors.Join(err, resp.Body.Close()); err != nil {
		return Answer{}, err
	}
	switch resp.StatusCode {
	case http.StatusNotModified:
		return Answer{}, nil
	case http.StatusOK:
	default:
		return Answer{}, fmt.Errorf("%s: HTTP %d", u, resp.StatusCode)
	}
	if len(b) > maxBody {
		return Answer{}, fmt.Errorf("%s: answer larger than %d bytes", u, maxBody)
	}
	files, err := Parse(b, log)
	if err != nil {
		return Answer{}, fmt.Errorf("%s: %w", u, err)
	}
	return Answer{Changed: true, ETag: resp.Header.Get("ETag"), Files: files}, nil
}

func Parse(b []byte, log *slog.Logger) ([]File, error) {
	var raw *struct {
		Files []File `json:"files"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, errors.New("the answer is null, not an object")
	}
	var out []File
	seen := map[string]bool{}
	for _, f := range raw.Files {
		switch {
		case !namePattern.MatchString(f.Name):
			log.Warn("skip an instruction file with an invalid name", "name", f.Name)
		case seen[f.Name]:
			log.Warn("skip a repeated instruction file", "name", f.Name)
		default:
			seen[f.Name] = true
			out = append(out, f)
		}
	}
	return out, nil
}

func digest(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func writeFile(dir, name string, body []byte) error {
	f, err := renameio.NewPendingFile(filepath.Join(dir, name), renameio.WithTempDir(dir), renameio.WithStaticPermissions(0o644))
	if err != nil {
		return err
	}
	defer func() { _ = f.Cleanup() }()
	if _, err := f.Write(body); err != nil {
		return err
	}
	return f.CloseAtomicallyReplace()
}

// Write leaves alone every file prev does not list, even one named like a wanted file.
func Write(dir string, files []File, etag string, prev Manifest, log *slog.Logger) (Manifest, error) {
	next := Manifest{ETag: etag, Files: map[string]Entry{}}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return prev, err
	}
	want := map[string][]byte{}
	for _, f := range files {
		want[f.Name] = []byte(f.Body)
	}
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(want)) {
		body := want[name]
		_, ours := prev.Files[name]
		cur, err := os.ReadFile(filepath.Join(dir, name))
		switch {
		case err == nil && bytes.Equal(cur, body):
		case err == nil && !ours:
			log.Warn("an instruction file hubridge did not write is in the way; remove it to let hubridge manage it", "path", filepath.Join(dir, name))
			continue
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			errs = append(errs, err)
			if ours {
				next.Files[name] = prev.Files[name]
			}
			continue
		default:
			if err := writeFile(dir, name, body); err != nil {
				errs = append(errs, err)
				if ours {
					next.Files[name] = prev.Files[name]
				}
				continue
			}
		}
		next.Files[name] = entry(body)
	}
	for _, name := range slices.Sorted(maps.Keys(prev.Files)) {
		if _, ok := want[name]; ok || !namePattern.MatchString(name) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
			next.Files[name] = prev.Files[name]
		}
	}
	if len(errs) > 0 {
		next.ETag = ""
	}
	return next, errors.Join(errs...)
}

func ReadManifest(path string) (Manifest, bool, error) {
	m := Manifest{Files: map[string]Entry{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return m, false, nil
	}
	if err != nil {
		return m, false, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, true, fmt.Errorf("%s: %w", path, err)
	}
	if m.Files == nil {
		m.Files = map[string]Entry{}
	}
	return m, true, nil
}

func writeManifest(path string, m Manifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return home.WriteFile(path, append(b, '\n'))
}

type Mirror struct {
	URL     string
	Machine string
	Home    *home.Home
	Dir     string
	HTTP    *http.Client
	Log     *slog.Logger

	mu sync.Mutex
}

func New(url, machine string, h *home.Home, dir string, log *slog.Logger) *Mirror {
	return &Mirror{URL: url, Machine: machine, Home: h, Dir: dir, HTTP: &http.Client{Timeout: 15 * time.Second}, Log: log}
}

func (m *Mirror) manifestPath() string { return m.Home.Path(ManifestFile) }

func (m *Mirror) Restore() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev, found, err := ReadManifest(m.manifestPath())
	if !found || err != nil {
		return err
	}
	var restored, waiting []string
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(prev.Files)) {
		e := prev.Files[name]
		cur, err := os.ReadFile(filepath.Join(m.Dir, name))
		switch {
		case !namePattern.MatchString(name):
		case e.Body == nil:
			if err != nil || digest(cur) != e.SHA256 {
				waiting = append(waiting, name)
			}
		case err == nil && string(cur) == *e.Body:
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			errs = append(errs, err)
		default:
			if err := os.MkdirAll(m.Dir, 0o755); err != nil {
				return errors.Join(append(errs, err)...)
			}
			if err := writeFile(m.Dir, name, []byte(*e.Body)); err != nil {
				errs = append(errs, err)
				continue
			}
			restored = append(restored, name)
		}
	}
	if len(restored) > 0 {
		m.Log.Info("instructions restored from the manifest", "dir", m.Dir, "files", restored)
	}
	if len(waiting) > 0 {
		m.Log.Info("the manifest keeps no body for these instructions, so they wait for hubd", "dir", m.Dir, "files", waiting)
	}
	return errors.Join(errs...)
}

func (m *Mirror) Refresh(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev, found, err := ReadManifest(m.manifestPath())
	if err != nil {
		return err
	}
	token, err := m.Home.Token()
	if err != nil {
		return err
	}
	etag := ""
	if m.intact(prev) {
		etag = prev.ETag
	}
	ans, err := Fetch(ctx, m.HTTP, m.URL, m.Machine, token, etag, m.Log)
	if err != nil || !ans.Changed {
		return err
	}
	return m.apply(ans.Files, ans.ETag, prev, !found)
}

// intact reports whether every listed file is still on disk as written and has its body kept, so a 304 can leave neither a deleted or edited file unrepaired nor a v0.2.0 manifest without bodies.
func (m *Mirror) intact(prev Manifest) bool {
	for name, e := range prev.Files {
		b, err := os.ReadFile(filepath.Join(m.Dir, name))
		if e.Body == nil || err != nil || digest(b) != e.SHA256 {
			return false
		}
	}
	return true
}

func (m *Mirror) apply(files []File, etag string, prev Manifest, save bool) error {
	next, err := Write(m.Dir, files, etag, prev, m.Log)
	changed := !maps.EqualFunc(next.Files, prev.Files, func(a, b Entry) bool { return a.SHA256 == b.SHA256 })
	if save || next.ETag != prev.ETag || !maps.EqualFunc(next.Files, prev.Files, Entry.same) {
		if werr := writeManifest(m.manifestPath(), next); werr != nil {
			return errors.Join(err, werr)
		}
	}
	if changed {
		m.Log.Info("instructions updated", "dir", m.Dir, "files", slices.Sorted(maps.Keys(next.Files)))
	}
	return err
}
