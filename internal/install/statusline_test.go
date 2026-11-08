package install

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"github.com/kloudkit/hubridge/internal/home"
	"github.com/kloudkit/hubridge/internal/usage"
)

const withStatusLine = `{
  "model": "opus",
  "statusLine": {
    "type": "command",
    "command": "zsh /etc/claude-code/statusline.zsh",
    "padding": 0
  },
  "permissions": {
    "allow": [
      "Bash(ls:*)"
    ]
  }
}
`

func TestStatusLineIsWrappedOnceAndRestored(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	homeDir := filepath.Join(dir, "hubd")
	os.MkdirAll(homeDir, 0o700)
	os.WriteFile(path, []byte(withStatusLine), 0o644)

	wrapped, err := WrapStatusLine(path, homeDir)
	if err != nil || !wrapped {
		t.Fatalf("wrap %v %v", wrapped, err)
	}
	b, _ := os.ReadFile(path)
	if cmd := gjson.GetBytes(b, "statusLine.command").String(); cmd != StatusLineCommand(homeDir) {
		t.Fatalf("command %q", cmd)
	}
	if gjson.GetBytes(b, "statusLine.padding").Int() != 0 || !strings.Contains(string(b), `"Bash(ls:*)"`) {
		t.Fatalf("other settings changed:\n%s", b)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o644 {
		t.Fatalf("mode changed to %o", st.Mode().Perm())
	}
	if again, _ := WrapStatusLine(path, homeDir); again {
		t.Fatal("a wrapped status line was wrapped again")
	}

	rewritten := strings.Replace(withStatusLine, "statusline.zsh", "statusline-v2.zsh", 1)
	os.WriteFile(path, []byte(rewritten), 0o644)
	if again, _ := WrapStatusLine(path, homeDir); !again {
		t.Fatal("a status line rewritten by another tool was not wrapped again")
	}
	saved, _ := os.ReadFile(filepath.Join(homeDir, StatusLineFile))
	if gjson.GetBytes(saved, "command").String() != "zsh /etc/claude-code/statusline-v2.zsh" {
		t.Fatalf("saved original %s", saved)
	}

	if err := UnwrapStatusLine(path, homeDir); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if string(b) != rewritten {
		t.Fatalf("unwrap did not restore the original:\n%s", b)
	}
	if _, err := os.Stat(filepath.Join(homeDir, StatusLineFile)); !os.IsNotExist(err) {
		t.Fatal("the saved original was left behind")
	}
}

func TestNoStatusLineIsNeverAdded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	os.WriteFile(path, []byte(`{"model":"opus"}`), 0o600)
	if wrapped, err := WrapStatusLine(path, dir); err != nil || wrapped {
		t.Fatalf("wrap %v %v", wrapped, err)
	}
	if b, _ := os.ReadFile(path); string(b) != `{"model":"opus"}` {
		t.Fatalf("settings changed: %s", b)
	}
	if wrapped, err := WrapStatusLine(filepath.Join(dir, "missing.json"), dir); err != nil || wrapped {
		t.Fatalf("missing settings: %v %v", wrapped, err)
	}
}

func TestStatusLineKeepsTheReadingAndRunsTheOriginal(t *testing.T) {
	h, _ := home.Open(t.TempDir())
	os.WriteFile(h.Path(StatusLineFile), []byte(`{"type":"command","command":"cat | wc -c | tr -d ' \\n'; printf ' ok'"}`), 0o600)
	in := `{"rate_limits":{"seven_day":{"used_percentage":40,"resets_at":1794117600}}}`
	var out, errOut bytes.Buffer
	now := time.Date(2026, 11, 5, 18, 0, 0, 0, time.UTC)
	if code := RunStatusLine(context.Background(), h, strings.NewReader(in), &out, &errOut, now); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if out.String() != "75 ok" {
		t.Fatalf("the original did not get the same input: %q", out.String())
	}
	if r, ok := usage.Load(h); !ok || r.SevenDay == nil || r.SevenDay.Utilization != 0.4 {
		t.Fatalf("reading %+v", r)
	}

	os.Remove(h.Path(StatusLineFile))
	out.Reset()
	if code := RunStatusLine(context.Background(), h, strings.NewReader(in), &out, &errOut, now); code != 0 || out.Len() != 0 {
		t.Fatalf("with no original: exit %d, printed %q", code, out.String())
	}
	os.WriteFile(h.Path(StatusLineFile), []byte(`{"type":"command","command":"exit 3"}`), 0o600)
	if code := RunStatusLine(context.Background(), h, strings.NewReader(in), &out, &errOut, now); code != 3 {
		t.Fatalf("exit code not passed through: %d", code)
	}
}
