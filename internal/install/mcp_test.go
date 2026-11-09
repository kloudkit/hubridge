package install

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kloudkit/hubridge/internal/home"
)

func TestHeaders(t *testing.T) {
	h, _ := home.Open(t.TempDir())
	var out bytes.Buffer
	if err := Headers(h, &out); err == nil || out.Len() != 0 {
		t.Fatalf("no token: err %v, stdout %q", err, out.String())
	}
	h.WriteToken(issued)
	out.Reset()
	if err := Headers(h, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != `{"Authorization":"Bearer `+issued+`"}` {
		t.Fatalf("got %q", out.String())
	}
}

func fakeClaude(t *testing.T) string {
	dir := t.TempDir()
	log := filepath.Join(dir, "argv")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\037' \"$a\" >> '" + log + "'; done\necho >> '" + log + "'\n" +
		"if [ \"$2\" = remove ]; then echo 'No MCP server found with name: hubd' >&2; exit 1; fi\n"
	os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o700)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func calls(t *testing.T, log string) [][]string {
	b, _ := os.ReadFile(log)
	var out [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		out = append(out, strings.Split(strings.TrimSuffix(line, "\x1f"), "\x1f"))
	}
	return out
}

func TestRegisterMCPRemovesThenAdds(t *testing.T) {
	log := fakeClaude(t)
	homeDir := "/Users/me/Application Support/hubd"
	var out bytes.Buffer
	if err := RegisterMCP(homeDir, &out); err != nil {
		t.Fatal(err)
	}
	got := calls(t, log)
	if len(got) != 2 || strings.Join(got[0], " ") != "mcp remove --scope user hubd" {
		t.Fatalf("calls %q", got)
	}
	add := got[1]
	if len(add) != 6 || strings.Join(add[:5], " ") != "mcp add-json --scope user hubd" {
		t.Fatalf("add call %q", add)
	}
	var cfg struct {
		Type    string   `json:"type"`
		Command string   `json:"command"`
		Args    []string `json:"args"`
		URL     string   `json:"url"`
	}
	if err := json.Unmarshal([]byte(add[5]), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Type != "stdio" || cfg.Command != homeDir+"/bin/hubridge" || strings.Join(cfg.Args, " ") != "mcp --home "+homeDir || cfg.URL != "" {
		t.Errorf("unexpected config %s", add[5])
	}
	if strings.Contains(add[5], "hubd_") || strings.Contains(add[5], "headersHelper") {
		t.Errorf("config carries what it must not: %s", add[5])
	}

	os.Remove(log)
	if err := UnregisterMCP(&out); err != nil {
		t.Fatalf("a not-found remove should succeed: %v", err)
	}
	if got := calls(t, log); len(got) != 1 || strings.Join(got[0], " ") != "mcp remove --scope user hubd" {
		t.Fatalf("remove calls %q", got)
	}
}

func TestRegisterMCPWithoutClaude(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	if err := RegisterMCP("/h", &out); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if strings.Count(s, "\n") != 1 || !strings.Contains(s, "not registered") || !strings.Contains(s, `claude mcp add-json --scope user hubd '{"type":"stdio","command":"/h/bin/hubridge","args":["mcp","--home","/h"]}'`) {
		t.Fatalf("got %q", s)
	}
	out.Reset()
	if err := UnregisterMCP(&out); err != nil || !strings.Contains(out.String(), "claude mcp remove --scope user hubd") {
		t.Fatalf("remove without claude: %v %q", err, out.String())
	}
}
