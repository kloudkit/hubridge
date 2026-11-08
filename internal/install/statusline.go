package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/kloudkit/hubridge/internal/home"
	"github.com/kloudkit/hubridge/internal/usage"
)

const (
	StatusLineFile    = "statusline.json"
	statusLineTimeout = 10 * time.Second
)

var oursStatusLine = regexp.MustCompile(`hubridge"? statusline\b`)

func StatusLineCommand(homeDir string) string { return selfCommand(homeDir, "statusline") }

func statusLineOf(raw []byte) (gjson.Result, string, bool) {
	sl := gjson.GetBytes(raw, "statusLine")
	cmd := sl.Get("command").String()
	return sl, cmd, sl.IsObject() && sl.Get("type").String() == "command" && cmd != ""
}

func WrapStatusLine(settingsPath, homeDir string) (bool, error) {
	saved := filepath.Join(homeDir, StatusLineFile)
	path, raw, mode, err := readSettings(settingsPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("%s: %w", settingsPath, err)
	}
	sl, cmd, ok := statusLineOf(raw)
	if !ok {
		os.Remove(saved)
		return false, nil
	}
	if oursStatusLine.MatchString(cmd) {
		return false, nil
	}
	if err := home.WriteFile(saved, []byte(sl.Raw)); err != nil {
		return false, fmt.Errorf("keep the original status line: %w", err)
	}
	out, err := sjson.SetBytes(raw, "statusLine.command", StatusLineCommand(homeDir))
	if err != nil {
		return false, err
	}
	return true, writeSettings(path, out, mode)
}

func UnwrapStatusLine(settingsPath, homeDir string) error {
	saved := filepath.Join(homeDir, StatusLineFile)
	defer os.Remove(saved)
	path, raw, mode, err := readSettings(settingsPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s: %w", settingsPath, err)
	}
	if _, cmd, ok := statusLineOf(raw); !ok || !oursStatusLine.MatchString(cmd) {
		return nil
	}
	var out []byte
	if original, err := os.ReadFile(saved); err == nil && gjson.ValidBytes(original) {
		out, err = sjson.SetRawBytes(raw, "statusLine", original)
		if err != nil {
			return err
		}
	} else if out, err = sjson.DeleteBytes(raw, "statusLine"); err != nil {
		return err
	}
	return writeSettings(path, out, mode)
}

func originalStatusLine(h *home.Home) string {
	b, err := os.ReadFile(h.Path(StatusLineFile))
	if err != nil {
		return ""
	}
	cmd := gjson.GetBytes(b, "command").String()
	if oursStatusLine.MatchString(cmd) {
		return ""
	}
	return cmd
}

func RunStatusLine(ctx context.Context, h *home.Home, stdin io.Reader, stdout, stderr io.Writer, now time.Time) int {
	raw, _ := io.ReadAll(io.LimitReader(stdin, 1<<20))
	if r, ok := usage.FromStatusLine(raw, now); ok {
		if _, err := usage.Save(h, r, now); err != nil {
			fmt.Fprintln(stderr, "hubd statusline:", err)
		}
	}
	command := originalStatusLine(h)
	if command == "" {
		return 0
	}
	ctx, cancel := context.WithTimeout(ctx, statusLineTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(raw), stdout, stderr
	if err := cmd.Run(); err != nil {
		if exit, ok := errors.AsType[*exec.ExitError](err); ok {
			return exit.ExitCode()
		}
		fmt.Fprintln(stderr, "hubd statusline:", err)
		return 1
	}
	return 0
}
