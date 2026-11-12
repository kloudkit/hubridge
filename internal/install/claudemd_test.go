package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPointerIsFirstKeepsTheRestAndRemovesCleanly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "CLAUDE.md")
	changed, err := WritePointer(path)
	if err != nil || !changed {
		t.Fatalf("first write: %v %v", changed, err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != Pointer {
		t.Fatalf("got %q", b)
	}
	if changed, _ = WritePointer(path); changed {
		t.Fatal("a rerun must not rewrite")
	}
	os.WriteFile(path, []byte("# Mine\n\nKeep this.\n"+Pointer), 0o644)
	if _, err := WritePointer(path); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if !strings.HasPrefix(string(b), Pointer) || !strings.HasSuffix(string(b), "# Mine\n\nKeep this.\n") || strings.Count(string(b), pointerStart) != 1 {
		t.Fatalf("got %q", b)
	}
	if err := RemovePointer(path); err != nil {
		t.Fatal(err)
	}
	if b, _ = os.ReadFile(path); string(b) != "# Mine\n\nKeep this.\n" {
		t.Fatalf("after remove %q", b)
	}
	WritePointer(path)
	os.WriteFile(path, []byte(Pointer), 0o644)
	if err := RemovePointer(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("a file that held only the block should be gone")
	}
	if err := RemovePointer(path); err != nil {
		t.Fatal("a second remove must not fail")
	}
}
