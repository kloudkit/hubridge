package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const existing = `{
  "model": "opus",
  "hooks": {
    "SessionStart": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "echo start && date"
          }
        ]
      }
    ],
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [
          {
            "type": "command",
            "command": "guard.sh"
          }
        ]
      }
    ]
  },
  "permissions": {
    "allow": [
      "Bash(ls:*)"
    ]
  }
}
`

func TestSetupMergesIdempotentlyAndRemoves(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	os.WriteFile(path, []byte(existing), 0o644)
	homeDir := "/h/hubd"

	if err := Setup(path, homeDir, false); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(path)
	s := string(first)
	for _, want := range []string{
		`"command": "echo start && date"`,
		`"command": "guard.sh"`,
		`"command": "\"/h/hubd/bin/hubridge\" hook session-start --home \"/h/hubd\""`,
		`"command": "\"/h/hubd/bin/hubridge\" hook stop --home \"/h/hubd\""`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in\n%s", want, s)
		}
	}
	if strings.Index(s, `"model"`) >= strings.Index(s, `"hooks"`) || strings.Index(s, `"hooks"`) >= strings.Index(s, `"permissions"`) {
		t.Errorf("key order changed:\n%s", s)
	}
	if strings.Index(s, "echo start") > strings.Index(s, "hook session-start") {
		t.Error("our SessionStart entry should come after the existing one")
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o644 {
		t.Errorf("mode changed to %o", st.Mode().Perm())
	}
	if bak, _ := os.ReadFile(path + ".hubd-bak"); string(bak) != existing {
		t.Error("backup is not the original file")
	}

	if err := Setup(path, homeDir, false); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if string(second) != string(first) {
		t.Fatalf("re-run changed the file:\n%s", second)
	}
	if bak, _ := os.ReadFile(path + ".hubd-bak"); string(bak) != existing {
		t.Error("backup was overwritten")
	}

	if err := Setup(path, homeDir, true); err != nil {
		t.Fatal(err)
	}
	removed, _ := os.ReadFile(path)
	if string(removed) != existing {
		t.Fatalf("remove did not restore the other entries exactly:\n%s", removed)
	}
}

func TestSetupCreatesSettings(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "cfg"))
	path, err := SettingsPath()
	if err != nil || path != filepath.Join(dir, "cfg", "settings.json") {
		t.Fatalf("path %s %v", path, err)
	}
	if err := Setup(path, "/h", false); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "hook session-start") || !strings.HasPrefix(string(b), "{\n  \"hooks\": {") {
		t.Fatalf("got\n%s", b)
	}
	if err := Setup(path, "/h", true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "{}\n" {
		t.Fatalf("after remove: %s", b)
	}
}
