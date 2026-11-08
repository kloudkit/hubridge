package transcript

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	absPath  = regexp.MustCompile(`(?:^|[\s'"=:(])(/[^\s'"` + "`" + `;|&<>()$*?]+)`)
	cdDir    = regexp.MustCompile(`(?:^|[\s;&|(])cd\s+("[^"]+"|'[^']+'|[^\s;&|)]+)`)
	gitC     = regexp.MustCompile(`\bgit\s+-C\s+("[^"]+"|'[^']+'|\S+)`)
	relToken = regexp.MustCompile(`^[\w.][\w.-]*/[\w./-]*$`)
)

func (f *File) bump(m map[string]*Edit, p string, at time.Time) {
	p = filepath.Clean(p)
	if !filepath.IsAbs(p) {
		return
	}
	e := m[p]
	if e == nil {
		e = &Edit{}
		m[p] = e
	}
	e.Count++
	if at.After(e.Last) {
		e.Last = at
	}
}

func (f *File) bumpTouch(p string, at time.Time) {
	if p != "" && !isScratch(filepath.Clean(p)) {
		f.bump(f.Touches, p, at)
	}
}

func isScratch(p string) bool {
	return strings.Contains(p+"/", "/scratchpad/") || strings.HasPrefix(p, "/tmp/claude-")
}

func (f *File) touch(name string, input json.RawMessage, cwd string, at time.Time) {
	var in struct {
		FilePath string `json:"file_path"`
		Path     string `json:"path"`
		Command  string `json:"command"`
	}
	json.Unmarshal(input, &in)
	base := f.Folder
	if base == "" {
		base = cwd
	}
	switch name {
	case "Read":
		f.bumpTouch(in.FilePath, at)
	case "Grep", "Glob", "LS":
		f.bumpTouch(resolve(in.Path, base), at)
	case "Bash":
		for _, p := range BashPaths(in.Command, base) {
			f.bumpTouch(p, at)
		}
	}
}

func resolve(p, base string) string {
	p = strings.Trim(p, `"'`)
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	if base == "" {
		return ""
	}
	return filepath.Join(base, p)
}

func BashPaths(cmd, base string) []string {
	var out []string
	for _, m := range absPath.FindAllStringSubmatch(cmd, -1) {
		out = append(out, m[1])
	}
	cds := cdDir.FindAllStringSubmatch(cmd, -1)
	for _, m := range cds {
		out = append(out, resolve(m[1], base))
	}
	for _, m := range gitC.FindAllStringSubmatch(cmd, -1) {
		if !filepath.IsAbs(strings.Trim(m[1], `"'`)) {
			out = append(out, resolve(m[1], base))
		}
	}
	if len(cds) == 0 && base != "" {
		for _, tok := range strings.Fields(cmd) {
			tok = strings.Trim(tok, `"'`)
			if relToken.MatchString(tok) && !strings.Contains(tok, "://") {
				out = append(out, filepath.Join(base, tok))
			}
		}
	}
	seen := map[string]bool{}
	kept := out[:0]
	for _, p := range out {
		if p != "" && !strings.HasPrefix(p, "//") && !seen[p] {
			seen[p] = true
			kept = append(kept, p)
		}
	}
	return kept
}
