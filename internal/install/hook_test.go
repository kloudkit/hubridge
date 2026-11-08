package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kloudkit/hubridge/internal/home"
	"github.com/kloudkit/hubridge/internal/proc"
)

func tree(t map[int32]proc.Info) Lookup {
	return func(pid int32) (proc.Info, bool) { i, ok := t[pid]; return i, ok }
}

func TestClaudePID(t *testing.T) {
	procs := tree(map[int32]proc.Info{
		300: {PPID: 200, Name: "sh", Argv0: "/bin/sh"},
		200: {PPID: 100, Name: "node", Argv0: "/usr/local/bin/claude"},
		100: {PPID: 1, Name: "bash", Argv0: "bash"},
		500: {PPID: 400, Name: "sh"},
		400: {PPID: 1, Name: "claude"},
		700: {PPID: 600, Name: "sh"},
		600: {PPID: 1, Name: "code"},
	})
	for start, want := range map[int32]int32{300: 200, 500: 400, 700: 700, 999: 999} {
		if got := ClaudePID(start, procs); got != want {
			t.Errorf("from %d: got %d want %d", start, got, want)
		}
	}
}

func TestSessionStartHandsOverAndStartsSupervisor(t *testing.T) {
	h, _ := home.Open(t.TempDir())
	procs := tree(map[int32]proc.Info{300: {PPID: 200, Name: "sh"}, 200: {PPID: 1, Name: "claude"}})
	started := 0
	start := func() error { started++; return nil }

	if err := SessionStart(h, HookInput{SessionID: "s-1"}, 300, procs, start); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(h.Dir, "sessions", "s-1"))
	if string(b) != `{"pid":200}` || started != 1 {
		t.Fatalf("handover %s, started %d", b, started)
	}
	if st, _ := os.Stat(filepath.Join(h.Dir, "sessions", "s-1")); st.Mode().Perm() != 0o600 {
		t.Errorf("handover is %o", st.Mode().Perm())
	}

	held, err := h.LockFile("supervisor.lock")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	SessionStart(h, HookInput{SessionID: "s-2"}, 300, procs, start)
	if started != 1 {
		t.Fatal("started a supervisor while one holds the lock")
	}
	if err := SessionStart(h, HookInput{SessionID: "../evil"}, 300, procs, start); err == nil {
		t.Fatal("accepted a session id with a path in it")
	}
}

func TestReadInputNeverBlocks(t *testing.T) {
	r, w, _ := os.Pipe()
	defer w.Close()
	began := time.Now()
	in := ReadInput(r)
	if d := time.Since(began); d > 50*time.Millisecond || in.SessionID != "" {
		t.Fatalf("took %v", d)
	}
	r2, w2, _ := os.Pipe()
	w2.Write([]byte(`{"session_id":"abc","notification_type":"permission_prompt"}`))
	w2.Close()
	if in := ReadInput(r2); in.SessionID != "abc" || in.NotificationType != "permission_prompt" {
		t.Fatalf("got %+v", in)
	}
}

func TestNotificationWritesWaitingFlag(t *testing.T) {
	h, _ := home.Open(t.TempDir())
	at := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	if err := Notification(h, HookInput{SessionID: "s-1"}, at); err != nil {
		t.Fatal(err)
	}
	if w := h.Waiting(); !w["s-1"].Equal(at) {
		t.Fatalf("waiting %+v", w)
	}
	if len(h.Handovers()) != 0 {
		t.Fatal("the waiting flag was read as a hand-over")
	}
	h.ClearWaiting("s-1")
	if len(h.Waiting()) != 0 {
		t.Fatal("flag not cleared")
	}
}

func TestStopKeepsWhatStillRuns(t *testing.T) {
	h := &home.Home{Dir: t.TempDir()}
	os.MkdirAll(h.Path("sessions"), 0o700)
	now := time.Date(2026, 11, 5, 21, 0, 0, 0, time.UTC)
	in := ReadInput(strings.NewReader(`{"session_id":"s1","hook_event_name":"Stop","background_tasks":[{"id":"a","type":"subagent","status":"running"},{"id":"b","type":"subagent"},{"id":"c","type":"monitor","server":"x","tool":"y"}],"session_crons":[{"id":"c1"}]}`))
	if err := Stop(h, in, now); err != nil {
		t.Fatal(err)
	}
	got := h.Backgrounds()["s1"]
	if !got.At.Equal(now) || got.Tasks["subagent"] != 2 || got.Tasks["monitor"] != 1 || got.Crons != 1 {
		t.Fatalf("background %+v", got)
	}
	if err := Stop(h, ReadInput(strings.NewReader(`{"session_id":"s1","hook_event_name":"Stop"}`)), now); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.Backgrounds()["s1"]; ok {
		t.Fatal("a Stop without the task list should clear the old one")
	}
	if _, ok := h.Handovers()["s1.background"]; ok {
		t.Fatal("the background file must not read as a hand-over")
	}
}
