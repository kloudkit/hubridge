package home

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func mode(t *testing.T, p string) os.FileMode {
	t.Helper()
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}

func TestOpenAndAtomicWrites(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "home")
	os.MkdirAll(dir, 0o755)
	h, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{dir, filepath.Join(dir, "sessions"), filepath.Join(dir, "queue")} {
		if m := mode(t, d); m != 0o700 {
			t.Errorf("%s is %o", d, m)
		}
	}
	if err := h.WriteToken("hubd_x"); err != nil {
		t.Fatal(err)
	}
	if m := mode(t, filepath.Join(dir, "token")); m != 0o600 {
		t.Errorf("token is %o", m)
	}
	if tok, _ := h.Token(); tok != "hubd_x" {
		t.Errorf("token read back %q", tok)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name()[0] == '.' {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestConfig(t *testing.T) {
	h, _ := Open(t.TempDir())
	os.WriteFile(filepath.Join(h.Dir, "config.json"), []byte(`{"url":"https://hubd.test","machine":"ws"}`), 0o600)
	c, err := h.Config()
	if err != nil || c.URL != "https://hubd.test" || c.Machine != "ws" {
		t.Fatalf("config %+v %v", c, err)
	}
}

func TestLock(t *testing.T) {
	h, _ := Open(t.TempDir())
	lockPath := filepath.Join(h.Dir, "daemon.lock")

	dead := exec.Command("true")
	dead.Run()
	os.WriteFile(lockPath, []byte(strconv.Itoa(dead.Process.Pid)+"\n"), 0o600)
	l, err := h.LockFile("daemon.lock")
	if err != nil {
		t.Fatalf("stale lock not taken: %v", err)
	}
	if m := mode(t, lockPath); m != 0o600 {
		t.Errorf("lock is %o", m)
	}
	if b, _ := os.ReadFile(lockPath); strings.TrimSpace(string(b)) != strconv.Itoa(os.Getpid()) {
		t.Errorf("lock file holds %q, not this pid", b)
	}
	if _, err := h.LockFile("daemon.lock"); err == nil || !strings.Contains(err.Error(), "holds the lock") {
		t.Fatalf("a held lock was taken again: %v", err)
	}
	if !h.Held("daemon.lock") {
		t.Error("Held does not see the lock")
	}
	l.Release()
	if h.Held("daemon.lock") {
		t.Error("lock still held after Release")
	}
	again, err := h.LockFile("daemon.lock")
	if err != nil {
		t.Fatalf("lock not free after Release: %v", err)
	}
	again.Release()
}

func TestQueueSupersedesAndCaps(t *testing.T) {
	h, _ := Open(t.TempDir())
	q := h.Queue()
	a1 := q.NewID("a")
	q.Put(a1, []byte("1"))
	b1 := q.NewID("b")
	q.Put(b1, []byte("2"))
	a2 := q.NewID("a")
	q.Put(a2, []byte("3"))
	p := q.Pending()
	if len(p) != 2 || p[0].ID != b1 || p[1].ID != a2 {
		t.Fatalf("pending %+v", p)
	}
	q.Ack(b1)
	if p := q.Pending(); len(p) != 1 || p[0].ID != a2 {
		t.Fatalf("after ack %+v", p)
	}
	if m := mode(t, filepath.Join(h.Dir, "queue", a2+".json")); m != 0o600 {
		t.Errorf("queue file is %o", m)
	}
	q.Cap = 10
	for i := 0; i < q.Cap+5; i++ {
		q.Put(q.NewID(strconv.Itoa(i)), []byte("x"))
	}
	p = q.Pending()
	if len(p) != q.Cap || p[len(p)-1].ID[20:] != strconv.Itoa(q.Cap+4) {
		t.Fatalf("cap: %d entries", len(p))
	}
}
