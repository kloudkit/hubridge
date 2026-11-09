package install

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/google/renameio/v2"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/kloudkit/hubridge/internal/proc"
)

const (
	Marketplace = "hubd"
	PluginID    = "hubd@hubd"
)

func ShimPath() (string, error) {
	dir, err := ClaudeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "hubd", "bin", "hubridge"), nil
}

// WriteShim puts a fixed-path launcher under the Claude config dir, because the plugin's hooks and MCP entry cannot know the client home.
func WriteShim(homeDir string) (string, error) {
	path, err := ShimPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	body := fmt.Sprintf("#!/bin/sh\nexec \"%s\" \"$@\" --home \"%s\"\n", Binary(homeDir), homeDir)
	return path, renameio.WriteFile(path, []byte(body), 0o755)
}

func RemoveShim() error {
	path, err := ShimPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func MarketplaceURL(hubd string) string { return strings.TrimRight(hubd, "/") + "/marketplace.json" }

func marketplaceEntry(homeDir, hubd string) []byte {
	b, _ := json.Marshal(map[string]any{"source": map[string]string{"source": "url", "url": MarketplaceURL(hubd), "headersHelper": selfCommand(homeDir, "mcp-headers")}})
	return b
}

// RegisterMarketplace declares hubd's marketplace and enables its plugin in user settings: Claude Code registers the marketplace and installs the plugin at the next session start, and runs the headersHelper without asking, so the catalog and its archives are fetched with the client token.
func RegisterMarketplace(settingsPath, homeDir, hubd string, remove bool) error {
	path, raw, err := readSettings(settingsPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if remove {
			return nil
		}
		raw = []byte("{}")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
	case err != nil:
		return fmt.Errorf("%s: %w", path, err)
	}
	if !gjson.ParseBytes(raw).IsObject() {
		return fmt.Errorf("%s: not a JSON object", path)
	}
	key := "extraKnownMarketplaces." + Marketplace
	enabled := "enabledPlugins." + strings.NewReplacer(".", "\\.", "@", "\\@").Replace(PluginID)
	var out []byte
	if remove {
		if out, err = sjson.DeleteBytes(raw, key); err != nil {
			return err
		}
		if out, err = sjson.DeleteBytes(out, enabled); err != nil {
			return err
		}
		for _, k := range []string{"extraKnownMarketplaces", "enabledPlugins"} {
			if left := gjson.GetBytes(out, k); left.Exists() && len(left.Map()) == 0 {
				if out, err = sjson.DeleteBytes(out, k); err != nil {
					return err
				}
			}
		}
	} else {
		if out, err = sjson.SetRawBytes(raw, key, marketplaceEntry(homeDir, hubd)); err != nil {
			return err
		}
		if out, err = sjson.SetBytes(out, enabled, true); err != nil {
			return err
		}
	}
	if bytes.Equal(out, raw) {
		return nil
	}
	return writeSettings(path, out)
}

type pluginResult struct {
	Outcome            string `json:"outcome"`
	Message            string `json:"message"`
	FailureCode        string `json:"failureCode"`
	AlreadyInGoalState bool   `json:"alreadyInGoalState"`
}

func lastJSON(b []byte) pluginResult {
	var r pluginResult
	lines := bytes.Split(bytes.TrimSpace(b), []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		if json.Unmarshal(bytes.TrimSpace(lines[i]), &r) == nil && r.Outcome != "" {
			return r
		}
	}
	return r
}

func claudePlugin(ctx context.Context, args ...string) ([]byte, error) {
	claude, err := proc.Claude()
	if err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, claude, append([]string{"plugin"}, args...)...).CombinedOutput()
}

// PluginInstalled reports whether hubd's plugin is installed, or false with an error when claude cannot say.
func PluginInstalled(ctx context.Context) (bool, error) {
	out, err := claudePlugin(ctx, "list", "--json")
	if err != nil {
		return false, fmt.Errorf("claude plugin list: %v: %s", err, strings.TrimSpace(string(out)))
	}
	doc := bytes.TrimSpace(out)
	if i, j := bytes.IndexByte(doc, '['), bytes.LastIndexByte(doc, ']'); i >= 0 && j > i {
		doc = doc[i : j+1]
	}
	if !gjson.ValidBytes(doc) {
		return false, fmt.Errorf("claude plugin list --json: not JSON: %s", firstLine("", out))
	}
	found := false
	gjson.ParseBytes(doc).ForEach(func(_, v gjson.Result) bool {
		found = v.Get("id").String() == PluginID
		return !found
	})
	return found, nil
}

func InstallPlugin(ctx context.Context, out io.Writer) error {
	b, err := claudePlugin(ctx, "install", PluginID, "--scope", "user", "--yes", "--json")
	r := lastJSON(b)
	if err != nil && !r.AlreadyInGoalState && r.FailureCode != "already_in_goal_state" {
		if errors.Is(err, exec.ErrNotFound) {
			fmt.Fprintf(out, "claude is not on PATH or in ~/.local/bin, so hubd's plugin was not installed; run later: claude plugin install %s --yes\n", PluginID)
			return nil
		}
		if strings.Contains(string(b), "not found in marketplace") || strings.Contains(string(b), "not found") {
			fmt.Fprintf(out, "hubd's plugin installs at the next Claude Code session start (the marketplace is registered then)\n")
			return nil
		}
		return fmt.Errorf("claude plugin install %s: %s", PluginID, firstLine(r.Message, b))
	}
	fmt.Fprintf(out, "installed hubd's plugin (%s) from its marketplace\n", PluginID)
	return nil
}

func UpdatePlugin(ctx context.Context) error {
	b, err := claudePlugin(ctx, "update", PluginID, "--yes", "--json")
	r := lastJSON(b)
	if err != nil && !r.AlreadyInGoalState {
		return fmt.Errorf("claude plugin update %s: %s", PluginID, firstLine(r.Message, b))
	}
	return nil
}

func UninstallPlugin(ctx context.Context, out io.Writer) error {
	b, err := claudePlugin(ctx, "uninstall", PluginID, "--scope", "user", "--json")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			fmt.Fprintf(out, "claude is not on PATH or in ~/.local/bin; if hubd's plugin is installed, run: claude plugin uninstall %s\n", PluginID)
			return nil
		}
		if bytes.Contains(b, []byte("not installed")) || bytes.Contains(b, []byte("Failed to uninstall")) {
			return nil
		}
		return fmt.Errorf("claude plugin uninstall %s: %s", PluginID, firstLine("", b))
	}
	fmt.Fprintln(out, "removed hubd's plugin from Claude Code")
	return nil
}

// EnsurePlugin installs the plugin when it is missing and otherwise updates it, so a daemon start or a sync brings every machine current.
func EnsurePlugin(ctx context.Context, out io.Writer) error {
	ok, err := PluginInstalled(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return InstallPlugin(ctx, out)
	}
	return UpdatePlugin(ctx)
}

func firstLine(msg string, raw []byte) string {
	if msg != "" {
		return msg
	}
	s := strings.TrimSpace(string(raw))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}
