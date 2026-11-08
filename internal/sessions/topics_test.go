package sessions

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseTopicsFlowAndBlock(t *testing.T) {
	flow := "---\ntype: Fact\ntopics: [ws, dev-box, 'k3s']\ngenerated: {by: x}\n---\n\n# x\n"
	if got := parseTopics(flow); !reflect.DeepEqual(got, []string{"ws", "dev-box", "k3s"}) {
		t.Fatalf("flow: %v", got)
	}
	block := "---\ntype: Fact\ntopics:\n  - argocd\n  - Bad Name\n  - airflow\ndefines: [x]\n---\n"
	if got := parseTopics(block); !reflect.DeepEqual(got, []string{"argocd", "airflow"}) {
		t.Fatalf("block: %v", got)
	}
	if got := parseTopics(strings.ReplaceAll(flow, "\n", "\r\n")); !reflect.DeepEqual(got, []string{"ws", "dev-box", "k3s"}) {
		t.Fatalf("crlf: %v", got)
	}
	if got := parseTopics("# no frontmatter\ntopics: [ws]\n"); got != nil {
		t.Fatalf("body only: %v", got)
	}
}

func TestSessionTopicsComeFromTheClaimsItReadOrEdited(t *testing.T) {
	fx := setup(t)
	base := filepath.Dir(fx.repo)
	claims := filepath.Join(base, "estate", "claims")
	link := filepath.Join(base, "link", "estate")
	os.MkdirAll(claims, 0o700)
	os.MkdirAll(filepath.Dir(link), 0o700)
	os.Symlink(filepath.Join(base, "estate"), link)
	write := func(slug, topics string) {
		os.WriteFile(filepath.Join(claims, slug+".md"), []byte("---\ntype: Fact\ntopics: "+topics+"\n---\n\n# "+slug+"\n"), 0o600)
	}
	write("a", "[ws, k3s]")
	write("b", "[k3s]")
	write("c", "[argocd]")
	const id = "88888888-8888-4888-8888-888888888888"
	use := func(n, tool, path string) string {
		return `{"type":"assistant","entrypoint":"cli","cwd":"` + fx.repo + `","isSidechain":false,"timestamp":"2026-11-04T10:0` + n + `:00.000Z","message":{"id":"msg_t` + n + `","role":"assistant","content":[{"type":"tool_use","id":"tu_t` + n + `","name":"` + tool + `","input":{"file_path":"` + path + `"}}]}}`
	}
	lines := []string{
		`{"type":"user","entrypoint":"cli","cwd":"` + fx.repo + `","isSidechain":false,"timestamp":"2026-11-04T10:00:00.000Z","message":{"role":"user","content":"Read the claims"}}`,
		use("1", "Read", filepath.Join(claims, "a.md")),
		use("2", "Read", filepath.Join(link, "claims", "a.md")),
		use("3", "Read", filepath.Join(claims, "b.md")),
		use("4", "Edit", filepath.Join(claims, "c.md")),
		use("5", "Read", filepath.Join(fx.repo, "README.md")),
		use("6", "Read", filepath.Join(base, "moved", "estate", "claims", "d.md")),
		use("7", "Read", filepath.Join(base, "moved", "estate", "claims", "gone.md")),
	}
	write("d", "[airflow]")
	fx.tracker.Estate = filepath.Join(base, "estate")
	dir := filepath.Join(fx.root, "-repo")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600)
	out := &sink{}
	fx.tracker.Emit(out, fx.tracker.Scan())
	for _, s := range out.sessions {
		if s.ID == id {
			if want := []string{"k3s", "airflow", "argocd", "ws"}; !reflect.DeepEqual(s.Topics, want) {
				t.Fatalf("want %v, got %v", want, s.Topics)
			}
			return
		}
	}
	t.Fatal("session not sent")
}
