package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/kloudkit/hubridge/internal/home"
)

const notificationMatcher = "permission_prompt|elicitation_dialog|agent_needs_input|idle_prompt"

var ours = regexp.MustCompile(`hubridge"? hook (session-start|notification|stop)\b`)

func ClaudeDir() (string, error) {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	u, err := os.UserHomeDir()
	return filepath.Join(u, ".claude"), err
}

func SettingsPath() (string, error) {
	dir, err := ClaudeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "settings.json"), nil
}

func Binary(homeDir string) string { return filepath.Join(homeDir, "bin", "hubridge") }

func selfCommand(homeDir, args string) string {
	return fmt.Sprintf(`"%s" %s --home "%s"`, Binary(homeDir), args, homeDir)
}

func hookCommand(homeDir, event string) string { return selfCommand(homeDir, "hook "+event) }

func readSettings(path string) (string, []byte, os.FileMode, error) {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return path, nil, 0, err
	}
	mode := os.FileMode(0o600)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte("{}")
	}
	if !gjson.ValidBytes(raw) {
		return path, nil, 0, errors.New("not valid JSON")
	}
	return path, raw, mode, nil
}

func writeSettings(path string, raw []byte, mode os.FileMode) error {
	var out bytes.Buffer
	if err := json.Indent(&out, bytes.TrimSpace(raw), "", "  "); err != nil {
		return err
	}
	out.WriteByte('\n')
	return home.WriteFileMode(path, out.Bytes(), mode)
}

func Setup(path, homeDir string, remove bool) error {
	path, raw, mode, err := readSettings(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if remove {
			return nil
		}
		raw, mode = []byte("{}"), 0o600
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
	case err != nil:
		return fmt.Errorf("%s: %w", path, err)
	default:
		bak := path + ".hubd-bak"
		if _, err := os.Stat(bak); errors.Is(err, os.ErrNotExist) {
			if err := home.WriteFileMode(bak, raw, mode); err != nil {
				return fmt.Errorf("back up settings: %w", err)
			}
		}
	}
	out, err := merge(raw, homeDir, remove)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return writeSettings(path, out, mode)
}

func merge(raw []byte, homeDir string, remove bool) ([]byte, error) {
	if !gjson.ValidBytes(raw) || !gjson.ParseBytes(raw).IsObject() {
		return nil, errors.New("not a JSON object")
	}
	if h := gjson.GetBytes(raw, "hooks"); h.Exists() && !h.IsObject() {
		return nil, errors.New("hooks: not a JSON object")
	}
	add := map[string]string{}
	if !remove {
		add["SessionStart"] = group("", hookCommand(homeDir, "session-start"))
		add["Notification"] = group(notificationMatcher, hookCommand(homeDir, "notification"))
		add["Stop"] = group("", hookCommand(homeDir, "stop"))
	}
	out := raw
	var err error
	for _, event := range []string{"SessionStart", "Notification", "Stop"} {
		path := "hooks." + event
		groups := gjson.GetBytes(out, path)
		if groups.Exists() && !groups.IsArray() {
			return nil, fmt.Errorf("hooks.%s: not an array", event)
		}
		list := groups.Array()
		for i := len(list) - 1; i >= 0; i-- {
			entries := list[i].Get("hooks").Array()
			mine := 0
			for j := len(entries) - 1; j >= 0; j-- {
				if ours.MatchString(entries[j].Get("command").String()) {
					if out, err = sjson.DeleteBytes(out, fmt.Sprintf("%s.%d.hooks.%d", path, i, j)); err != nil {
						return nil, err
					}
					mine++
				}
			}
			if mine > 0 && mine == len(entries) {
				if out, err = sjson.DeleteBytes(out, fmt.Sprintf("%s.%d", path, i)); err != nil {
					return nil, err
				}
			}
		}
		if g, ok := add[event]; ok {
			if out, err = sjson.SetRawBytes(out, path+".-1", []byte(g)); err != nil {
				return nil, err
			}
		}
		if left := gjson.GetBytes(out, path); left.Exists() && len(left.Array()) == 0 {
			if out, err = sjson.DeleteBytes(out, path); err != nil {
				return nil, err
			}
		}
	}
	if h := gjson.GetBytes(out, "hooks"); h.Exists() && len(h.Map()) == 0 {
		if out, err = sjson.DeleteBytes(out, "hooks"); err != nil {
			return nil, err
		}
	}
	return out, nil
}

type hookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

type hookGroup struct {
	Matcher string      `json:"matcher,omitempty"`
	Hooks   []hookEntry `json:"hooks"`
}

func group(matcher, command string) string {
	b, _ := json.Marshal(hookGroup{Matcher: matcher, Hooks: []hookEntry{{Type: "command", Command: command, Timeout: 5}}})
	return string(b)
}
