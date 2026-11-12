package install

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"

	"github.com/google/renameio/v2"
)

const (
	pointerStart = "<!-- hubd:start -->"
	pointerEnd   = "<!-- hubd:end -->"
)

// Pointer is the block hubridge keeps at the top of the user's CLAUDE.md: the one rule that must be read first, with where the rest is.
const Pointer = pointerStart + `
hubd holds this user's estate: what no command can tell a session about their machines, repos
and ways of working. Before assuming topology, credentials, naming or how a thing is done
here, search it with the hubd MCP tools (` + "`estate_search`, `estate_get`" + `) and read the
manual in ` + "`rules/hubd/hubd-estate.md`" + `, beside this file.
` + pointerEnd + "\n"

func ClaudeMdPath() (string, error) {
	dir, err := ClaudeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "CLAUDE.md"), nil
}

func stripPointer(raw []byte) []byte {
	i := bytes.Index(raw, []byte(pointerStart))
	j := bytes.Index(raw, []byte(pointerEnd))
	if i < 0 || j < i {
		return raw
	}
	j += len(pointerEnd)
	for j < len(raw) && raw[j] == '\n' {
		j++
	}
	return append(append([]byte{}, raw[:i]...), raw[j:]...)
}

// WritePointer puts the block first in CLAUDE.md and leaves everything else as it is; it reports whether the file changed.
func WritePointer(path string) (bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	rest := bytes.TrimLeft(stripPointer(raw), "\n")
	out := []byte(Pointer)
	if len(rest) > 0 {
		out = append(append(out, '\n'), rest...)
	}
	if bytes.Equal(out, raw) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	return true, renameio.WriteFile(path, out, 0o644)
}

// RemovePointer takes the block out and deletes the file when nothing else was in it.
func RemovePointer(path string) error {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	rest := bytes.TrimLeft(stripPointer(raw), "\n")
	if len(rest) == 0 {
		return os.Remove(path)
	}
	if bytes.Equal(rest, raw) {
		return nil
	}
	return renameio.WriteFile(path, rest, 0o644)
}
