package sessions

import (
	"cmp"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/kloudkit/hubridge/internal/transcript"
	"go.yaml.in/yaml/v3"
)

var (
	claimFile   = regexp.MustCompile(`/estate/claims/([a-z0-9][a-z0-9-]*)\.md$`)
	frontmatter = regexp.MustCompile(`\A---\r?\n((?s:.*?))\r?\n---`)
	topicName   = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,39}$`)
)

type claimTopics struct {
	mod    time.Time
	topics []string
}

func (t *Tracker) readTopics(files []*transcript.File) []string {
	count := map[string]int{}
	seen := map[string]bool{}
	for _, f := range files {
		for _, paths := range []map[string]*transcript.Edit{f.Touches, f.Edits} {
			for p := range paths {
				m := claimFile.FindStringSubmatch(filepath.Clean(p))
				if m == nil || seen[m[1]] {
					continue
				}
				seen[m[1]] = true
				for _, topic := range t.claimTopics(p, m[1]) {
					count[topic]++
				}
			}
		}
	}
	out := slices.Collect(maps.Keys(count))
	slices.SortFunc(out, func(a, b string) int { return cmp.Or(cmp.Compare(count[b], count[a]), strings.Compare(a, b)) })
	return out
}

func estateDir() string {
	if dir := os.Getenv("ESTATE"); dir != "" {
		return dir
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".claude", "estate")
	}
	return ""
}

func (t *Tracker) claimTopics(path, slug string) []string {
	st, err := os.Stat(path)
	if err != nil && t.Estate != "" {
		path = filepath.Join(t.Estate, "claims", slug+".md")
		st, err = os.Stat(path)
	}
	if err != nil {
		return nil
	}
	if c, ok := t.topics[path]; ok && c.mod.Equal(st.ModTime()) {
		return c.topics
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	topics := parseTopics(string(b))
	t.topics[path] = claimTopics{mod: st.ModTime(), topics: topics}
	return topics
}

func parseTopics(doc string) []string {
	fm := frontmatter.FindStringSubmatch(doc)
	if fm == nil {
		return nil
	}
	var meta struct {
		Topics []string `yaml:"topics"`
	}
	if yaml.Unmarshal([]byte(fm[1]), &meta) != nil {
		return nil
	}
	var out []string
	for _, name := range meta.Topics {
		if name = strings.TrimSpace(name); topicName.MatchString(name) {
			out = append(out, name)
		}
	}
	return out
}
