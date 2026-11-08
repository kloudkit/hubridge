package home

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/kloudkit/hubridge/internal/proc"
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
	os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0o644)
	h, err := Open(dir)
	if m := mode(t, filepath.Join(dir, "config.json")); m != 0o600 {
		t.Errorf("config.json left at %o", m)
	}
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

func TestConfigAndHandovers(t *testing.T) {
	h, _ := Open(t.TempDir())
	os.WriteFile(filepath.Join(h.Dir, "config.json"), []byte(`{"url":"https://hubd.test","machine":"ws"}`), 0o600)
	c, err := h.Config()
	if err != nil || c.URL != "https://hubd.test" || c.Machine != "ws" {
		t.Fatalf("config %+v %v", c, err)
	}
	os.WriteFile(filepath.Join(h.Dir, "sessions", "abc"), []byte(`{"pid":42,"started":1700000000000}`), 0o600)
	os.WriteFile(filepath.Join(h.Dir, "sessions", "bad"), []byte(`nope`), 0o600)
	hos := h.Handovers()
	if len(hos) != 1 || hos["abc"] != (Handover{PID: 42, Started: 1700000000000}) {
		t.Fatalf("handovers %+v", hos)
	}
}

func TestLock(t *testing.T) {
	h, _ := Open(t.TempDir())
	lockPath := filepath.Join(h.Dir, "daemon.lock")

	dead := exec.Command("true")
	dead.Run()
	os.WriteFile(lockPath, []byte(strconv.Itoa(dead.Process.Pid)+"\n"), 0o600)
	l, err := h.Lock()
	if err != nil {
		t.Fatalf("stale lock not taken: %v", err)
	}
	if m := mode(t, lockPath); m != 0o600 {
		t.Errorf("lock is %o", m)
	}
	if b, _ := os.ReadFile(lockPath); strings.TrimSpace(string(b)) != strconv.Itoa(os.Getpid()) {
		t.Errorf("lock file holds %q, not this pid", b)
	}
	if _, err := h.Lock(); !errors.Is(err, ErrLocked) {
		t.Fatalf("a held lock was taken again: %v", err)
	}
	if !h.Held("daemon.lock") {
		t.Error("Held does not see the lock")
	}
	l.Release()
	if h.Held("daemon.lock") {
		t.Error("lock still held after Release")
	}
	again, err := h.Lock()
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

func TestHandoverIsStampedOnFirstRead(t *testing.T) {
	h, _ := Open(t.TempDir())
	calls := 0
	h.StartTime = func(pid int32) (int64, bool) { calls++; return 1795787185520, pid == 7 }
	path := filepath.Join(h.Dir, "sessions", "s1")
	os.WriteFile(path, []byte(`{"pid":7}`), 0o644)
	os.WriteFile(filepath.Join(h.Dir, "sessions", "gone"), []byte(`{"pid":8}`), 0o600)

	hos := h.Handovers()
	if hos["s1"] != (Handover{PID: 7, Started: 1795787185520}) {
		t.Fatalf("first read %+v", hos)
	}
	if hos["gone"] != (Handover{PID: 8}) {
		t.Fatalf("a dead pid was stamped: %+v", hos["gone"])
	}
	b, _ := os.ReadFile(path)
	if string(b) != `{"pid":7,"started":1795787185520}` {
		t.Fatalf("file not stamped: %s", b)
	}
	if m := mode(t, path); m != 0o600 {
		t.Errorf("stamped file is %o", m)
	}
	calls = 0
	h.StartTime = func(int32) (int64, bool) { calls++; return 1, true }
	if h.Handovers()["s1"].Started != 1795787185520 {
		t.Fatal("stored start time was replaced on a later read")
	}
	if calls != 1 {
		t.Fatalf("StartTime called %d times; only the unstamped dead pid should be retried", calls)
	}
}

func TestHandoverWithChangedStartTimeIsNotAlive(t *testing.T) {
	h, _ := Open(t.TempDir())
	cmd := exec.Command("sleep", "30")
	cmd.Start()
	defer cmd.Process.Kill()
	path := filepath.Join(h.Dir, "sessions", "s1")
	os.WriteFile(path, []byte(`{"pid":`+strconv.Itoa(cmd.Process.Pid)+`}`), 0o600)

	ho := h.Handovers()["s1"]
	if !proc.Alive(ho.PID, ho.Started) {
		t.Fatalf("freshly stamped pid not alive: %+v", ho)
	}
	os.WriteFile(path, []byte(`{"pid":`+strconv.Itoa(cmd.Process.Pid)+`,"started":`+strconv.FormatInt(ho.Started-60_000, 10)+`}`), 0o600)
	ho = h.Handovers()["s1"]
	if proc.Alive(ho.PID, ho.Started) {
		t.Fatal("pid with a different start time counted as alive")
	}
}
