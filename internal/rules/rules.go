package rules

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
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

//go:embed builtin/*.md
var builtinFS embed.FS

var Builtins = []string{"estate", "secrets", "git"}

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*\.md$`)

type File struct {
	Name string `json:"name"`
	Body string `json:"body"`
}

type Rules struct {
	Builtins map[string]bool
	Files    []File
}

type Manifest struct {
	ETag  string            `json:"etag"`
	Files map[string]string `json:"files"`
}

type Answer struct {
	Changed bool
	ETag    string
	Rules   Rules
}

func Defaults() Rules {
	on := map[string]bool{}
	for _, b := range Builtins {
		on[b] = true
	}
	return Rules{Builtins: on}
}

func Dir() (string, error) {
	dir, err := install.ClaudeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "rules", "hubd"), nil
}

func builtin(name string) []byte {
	b, err := builtinFS.ReadFile("builtin/" + name + ".md")
	if err != nil {
		panic(err)
	}
	return b
}

func isBuiltin(file string) bool {
	return slices.ContainsFunc(Builtins, func(b string) bool { return b+".md" == file })
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
	r, err := Parse(b, log)
	if err != nil {
		return Answer{}, fmt.Errorf("%s: %w", u, err)
	}
	return Answer{Changed: true, ETag: resp.Header.Get("ETag"), Rules: r}, nil
}

func Parse(b []byte, log *slog.Logger) (Rules, error) {
	var raw struct {
		Builtins map[string]json.RawMessage `json:"builtins"`
		Files    []File                     `json:"files"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return Rules{}, err
	}
	r := Defaults()
	for _, name := range Builtins {
		v, ok := raw.Builtins[name]
		if !ok {
			continue
		}
		on := true
		if err := json.Unmarshal(v, &on); err != nil {
			return Rules{}, fmt.Errorf("builtins.%s: %w", name, err)
		}
		r.Builtins[name] = on
	}
	seen := map[string]bool{}
	for _, f := range raw.Files {
		switch {
		case !namePattern.MatchString(f.Name):
			log.Warn("skip a rule file with an invalid name", "name", f.Name)
		case isBuiltin(f.Name):
			log.Warn("skip a rule file named like a built-in", "name", f.Name)
		case seen[f.Name]:
			log.Warn("skip a repeated rule file", "name", f.Name)
		default:
			seen[f.Name] = true
			r.Files = append(r.Files, f)
		}
	}
	return r, nil
}

func (r Rules) contents() map[string][]byte {
	out := map[string][]byte{}
	for _, name := range Builtins {
		if r.Builtins[name] {
			out[name+".md"] = builtin(name)
		}
	}
	for _, f := range r.Files {
		out[f.Name] = []byte(f.Body)
	}
	return out
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
func Write(dir string, r Rules, etag string, prev Manifest, log *slog.Logger) (Manifest, error) {
	next := Manifest{ETag: etag, Files: map[string]string{}}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return prev, err
	}
	want := r.contents()
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(want)) {
		body := want[name]
		_, ours := prev.Files[name]
		cur, err := os.ReadFile(filepath.Join(dir, name))
		switch {
		case err == nil && bytes.Equal(cur, body):
		case err == nil && !ours:
			log.Warn("a rule file hubridge did not write is in the way; remove it to let hubridge manage it", "path", filepath.Join(dir, name))
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
		next.Files[name] = digest(body)
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
	m := Manifest{Files: map[string]string{}}
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
		m.Files = map[string]string{}
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

func (m *Mirror) Seed() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev, found, err := ReadManifest(m.manifestPath())
	if found || err != nil {
		return err
	}
	return m.apply(Defaults(), "", prev, true)
}

func (m *Mirror) Refresh(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev, _, err := ReadManifest(m.manifestPath())
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
	return m.apply(ans.Rules, ans.ETag, prev, false)
}

// intact reports whether every listed file is still on disk as written, so a 304 cannot leave a deleted or edited file unrepaired.
func (m *Mirror) intact(prev Manifest) bool {
	for name, sum := range prev.Files {
		b, err := os.ReadFile(filepath.Join(m.Dir, name))
		if err != nil || digest(b) != sum {
			return false
		}
	}
	return true
}

func (m *Mirror) apply(r Rules, etag string, prev Manifest, save bool) error {
	next, err := Write(m.Dir, r, etag, prev, m.Log)
	changed := !maps.Equal(next.Files, prev.Files)
	if save || changed || next.ETag != prev.ETag {
		if werr := writeManifest(m.manifestPath(), next); werr != nil {
			return errors.Join(err, werr)
		}
	}
	if changed {
		m.Log.Info("session rules updated", "dir", m.Dir, "files", slices.Sorted(maps.Keys(next.Files)))
	}
	return err
}
