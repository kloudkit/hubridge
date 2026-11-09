package install

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/kloudkit/hubridge/internal/home"
	"github.com/kloudkit/hubridge/internal/proc"
)

const mcpName = "hubd"

func Headers(h *home.Home, out io.Writer) error {
	token, err := h.Token()
	if err != nil {
		return fmt.Errorf("no readable token in %s: %w", h.Dir, err)
	}
	b, err := json.Marshal(map[string]string{"Authorization": "Bearer " + token})
	if err != nil {
		return err
	}
	_, err = out.Write(b)
	return err
}

func mcpConfig(homeDir string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(struct {
		Type    string   `json:"type"`
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}{"stdio", Binary(homeDir), []string{"mcp", "--home", homeDir}})
	return strings.TrimSpace(b.String())
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func RegisterMCP(homeDir string, out io.Writer) error {
	cfg := mcpConfig(homeDir)
	claude, err := proc.Claude()
	if err != nil {
		fmt.Fprintf(out, "claude is not on PATH or in ~/.local/bin, so hubd's MCP server was not registered; run later: claude mcp add-json --scope user %s %s\n", mcpName, shellQuote(cfg))
		return nil
	}
	removeMCP(claude)
	if b, err := exec.Command(claude, "mcp", "add-json", "--scope", "user", mcpName, cfg).CombinedOutput(); err != nil {
		return fmt.Errorf("claude mcp add-json: %v: %s", err, strings.TrimSpace(string(b)))
	}
	fmt.Fprintln(out, "registered hubd's MCP server for Claude Code (user scope)")
	return nil
}

func UnregisterMCP(out io.Writer) error {
	claude, err := proc.Claude()
	if err != nil {
		fmt.Fprintf(out, "claude is not on PATH or in ~/.local/bin; if hubd's MCP server is registered, run: claude mcp remove --scope user %s\n", mcpName)
		return nil
	}
	if b, err := removeMCP(claude); err != nil && !notFound(b) {
		return fmt.Errorf("claude mcp remove: %v: %s", err, strings.TrimSpace(string(b)))
	}
	fmt.Fprintln(out, "removed hubd's MCP server from Claude Code")
	return nil
}

func removeMCP(claude string) ([]byte, error) {
	return exec.Command(claude, "mcp", "remove", "--scope", "user", mcpName).CombinedOutput()
}

func notFound(out []byte) bool {
	s := strings.ToLower(string(out))
	return strings.Contains(s, "not found") || strings.Contains(s, "no mcp server") || strings.Contains(s, "does not exist")
}
