package install

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeClaudePlugin(t *testing.T, installed bool) string {
	dir := t.TempDir()
	log := filepath.Join(dir, "argv")
	list := `[]`
	if installed {
		list = `[\n  {\n    "id": "gopls-lsp@claude-plugins-official",\n    "scope": "user"\n  },\n  {\n    "id": "hubd@hubd",\n    "version": "0.4.0",\n    "scope": "user",\n    "enabled": true\n  }\n]`
	}
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\037' \"$a\" >> '" + log + "'; done\necho >> '" + log + "'\n" +
		"if [ \"$1\" = plugin ] && [ \"$2\" = list ]; then printf '%b\\n' '" + list + "'; exit 0; fi\n" +
		"if [ \"$1\" = plugin ] && [ \"$2\" = install ]; then echo 'Installing…'; echo '{\"command\":\"install\",\"outcome\":\"success\",\"message\":\"Successfully installed plugin: hubd\"}'; exit 0; fi\n" +
		"if [ \"$1\" = plugin ] && [ \"$2\" = update ]; then echo '{\"command\":\"update\",\"outcome\":\"failed\",\"failureCode\":\"already_in_goal_state\",\"alreadyInGoalState\":true,\"message\":\"up to date\"}'; exit 1; fi\n" +
		"if [ \"$1\" = plugin ] && [ \"$2\" = uninstall ]; then echo '{\"command\":\"uninstall\",\"outcome\":\"success\"}'; exit 0; fi\n" +
		"if [ \"$1\" = mcp ] && [ \"$2\" = get ]; then echo 'No MCP server named \"hubd\".' >&2; exit 1; fi\n" +
		"if [ \"$1\" = mcp ] && [ \"$2\" = remove ]; then echo 'No MCP server found with name: hubd' >&2; exit 1; fi\n"
	os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o700)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func TestShimExecsTheBinaryWithTheHome(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	path, err := WriteShim("/Users/me/Application Support/hubd")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "#!/bin/sh\nexec \"/Users/me/Application Support/hubd/bin/hubridge\" \"$@\" --home \"/Users/me/Application Support/hubd\"\n" {
		t.Fatalf("got %q", b)
	}
	if st, _ := os.Stat(path); st.Mode().Perm()&0o100 == 0 || filepath.Base(filepath.Dir(path)) != "bin" {
		t.Fatalf("not an executable under hubd/bin: %s %v", path, st.Mode())
	}
	if err := RemoveShim(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("shim should be gone")
	}
	if err := RemoveShim(); err != nil {
		t.Fatal("a second remove must not fail")
	}
}

func TestRegisterMarketplaceWritesTheHelperAndRemovesCleanly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	os.WriteFile(path, []byte("{\n  \"model\": \"opus\"\n}\n"), 0o644)
	if err := RegisterMarketplace(path, "/h", "https://hubd.test/", false); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	s := string(b)
	for _, want := range []string{`"extraKnownMarketplaces"`, `"hubd": {`, `"source": "url"`, `"url": "https://hubd.test/marketplace.json"`, `"headersHelper": "\"/h/bin/hubridge\" mcp-headers --home \"/h\""`, `"enabledPlugins"`, `"hubd@hubd": true`, `"model": "opus"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in\n%s", want, s)
		}
	}
	if strings.Contains(s, "hubd_") {
		t.Fatal("the settings must never carry the token")
	}
	if err := RegisterMarketplace(path, "/h", "https://hubd.test/", false); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(path); !bytes.Equal(again, b) {
		t.Fatal("a rerun changed the file")
	}
	if err := RegisterMarketplace(path, "/h", "", true); err != nil {
		t.Fatal(err)
	}
	if b, _ = os.ReadFile(path); string(b) != "{\n  \"model\": \"opus\"\n}\n" {
		t.Fatalf("remove left:\n%s", b)
	}
}

func TestEnsurePluginInstallsThenUpdates(t *testing.T) {
	ctx := context.Background()
	log := fakeClaudePlugin(t, false)
	var out bytes.Buffer
	if err := EnsurePlugin(ctx, &out); err != nil {
		t.Fatal(err)
	}
	got := calls(t, log)
	if len(got) != 2 || strings.Join(got[1], " ") != "plugin install hubd@hubd --scope user --yes --json" || !strings.Contains(out.String(), "installed hubd's plugin") {
		t.Fatalf("calls %q, out %q", got, out.String())
	}

	log = fakeClaudePlugin(t, true)
	out.Reset()
	if err := EnsurePlugin(ctx, &out); err != nil {
		t.Fatal(err)
	}
	got = calls(t, log)
	if len(got) != 2 || strings.Join(got[1], " ") != "plugin update hubd@hubd --yes --json" || out.Len() != 0 {
		t.Fatalf("calls %q, out %q", got, out.String())
	}
	if ok, err := PluginInstalled(ctx); !ok || err != nil {
		t.Fatalf("installed: %v %v", ok, err)
	}
	if err := UninstallPlugin(ctx, &out); err != nil || !strings.Contains(out.String(), "removed hubd's plugin") {
		t.Fatalf("uninstall: %v %q", err, out.String())
	}
}

func TestEnsurePluginWithoutClaude(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	if err := EnsurePlugin(context.Background(), &bytes.Buffer{}); err == nil {
		t.Fatal("no claude must be reported, so the daemon can retry")
	}
	var out bytes.Buffer
	if err := InstallPlugin(context.Background(), &out); err != nil || !strings.Contains(out.String(), "not installed; run later") {
		t.Fatalf("install without claude: %v %q", err, out.String())
	}
}
