package install

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kloudkit/hubridge/internal/home"
)

func fakeClaudeGet(t *testing.T, scope string, missing bool) string {
	dir := t.TempDir()
	log := filepath.Join(dir, "argv")
	body := "echo 'hubd:'; echo '  Scope: " + scope + "'; echo '  Status: ✔ Connected'\n"
	if missing {
		body = "echo 'No MCP server named \"hubd\". Configured servers: claude.ai Claude Docs' >&2; exit 1\n"
	}
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\037' \"$a\" >> '" + log + "'; done\necho >> '" + log + "'\n" +
		"pwd >> '" + log + ".cwd'\n" +
		"if [ \"$2\" = get ]; then " + body + "fi\n" +
		"if [ \"$2\" = remove ]; then echo 'No MCP server found with name: hubd' >&2; exit 1; fi\n"
	os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o700)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func TestCheckRegistrationScopes(t *testing.T) {
	cases := []struct{ scope, state, want string }{
		{"User config (available in all your projects)", RegUser, ""},
		{"Project config (shared via .mcp.json)", RegShadowed, "shadowed by project config"},
		{"Local config (private to you in this project)", RegShadowed, "shadowed by local config"},
	}
	for _, c := range cases {
		log := fakeClaudeGet(t, c.scope, false)
		cwd := t.TempDir()
		r := CheckRegistration(context.Background(), cwd)
		if r.State != c.state || r.Reason() != c.want || r.Cwd != cwd {
			t.Errorf("%q: %+v, reason %q", c.scope, r, r.Reason())
		}
		got := calls(t, log)
		if len(got) != 1 || got[0][0] != "mcp" || got[0][1] != "get" || got[0][2] != "hubd" {
			t.Errorf("calls %q", got)
		}
		b, _ := os.ReadFile(log + ".cwd")
		if real, _ := filepath.EvalSymlinks(cwd); filepath.Clean(string(bytes.TrimSpace(b))) != real {
			t.Errorf("ran in %q, want %q", b, real)
		}
	}
}

func TestEnsureRegistrationRepairsAMissingEntry(t *testing.T) {
	log := fakeClaudeGet(t, "", true)
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	h, _ := home.Open(t.TempDir())
	var out bytes.Buffer
	r, err := EnsureRegistration(context.Background(), h, t.TempDir(), "sess-1", &out)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != RegRepaired || r.Reason() != "" {
		t.Fatalf("got %+v", r)
	}
	got := calls(t, log)
	if len(got) != 3 || got[1][1] != "remove" || got[2][1] != "add-json" {
		t.Fatalf("calls %q", got)
	}
	b, err := os.ReadFile(filepath.Join(claudeDir, "hubd", "mcp", "sess-1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved Registration
	json.Unmarshal(b, &saved)
	if saved.State != RegRepaired || saved.At.IsZero() {
		t.Fatalf("saved %s", b)
	}
	ClearRegistration("sess-1")
	if _, err := os.Stat(filepath.Join(claudeDir, "hubd", "mcp", "sess-1.json")); !os.IsNotExist(err) {
		t.Fatal("the session file should be gone")
	}
}

func TestEnsureRegistrationWithoutClaude(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	h, _ := home.Open(t.TempDir())
	r, err := EnsureRegistration(context.Background(), h, "/", "sess-2", &bytes.Buffer{})
	if err != nil || r.State != RegUnknown || r.Reason() != "registration unknown" {
		t.Fatalf("got %+v, %v", r, err)
	}
	if _, err := RegistrationPath("../x"); err == nil {
		t.Fatal("a bad session id must be refused")
	}
}
