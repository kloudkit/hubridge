package install

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kloudkit/hubridge/internal/home"
)

func TestSessionStartStartsSupervisor(t *testing.T) {
	h, _ := home.Open(t.TempDir())
	started := 0
	start := func() error { started++; return nil }
	if err := SessionStart(h, start); err != nil || started != 1 {
		t.Fatalf("started %d, err %v", started, err)
	}
	held, err := h.LockFile("supervisor.lock")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	if err := SessionStart(h, start); err != nil || started != 1 {
		t.Fatal("started a supervisor while one holds the lock")
	}
}

func TestReadInputNeverBlocks(t *testing.T) {
	r, w, _ := os.Pipe()
	defer w.Close()
	began := time.Now()
	in := ReadInputWait(r, StdinWait)
	if d := time.Since(began); d > 50*time.Millisecond || in.SessionID != "" {
		t.Fatalf("took %v", d)
	}
	r2, w2, _ := os.Pipe()
	w2.Write([]byte(`{"session_id":"abc","notification_type":"permission_prompt"}`))
	w2.Close()
	if in := ReadInputWait(r2, StdinWait); in.SessionID != "abc" {
		t.Fatalf("got %+v", in)
	}
}

func TestStopKeepsWhatStillRuns(t *testing.T) {
	h := &home.Home{Dir: t.TempDir()}
	os.MkdirAll(h.Path("sessions"), 0o700)
	now := time.Date(2026, 11, 5, 21, 0, 0, 0, time.UTC)
	in := ReadInputWait(strings.NewReader(`{"session_id":"s1","hook_event_name":"Stop","background_tasks":[{"id":"a","type":"subagent","status":"running"},{"id":"b","type":"subagent"},{"id":"c","type":"monitor","server":"x","tool":"y"}],"session_crons":[{"id":"c1"}]}`), StdinWait)
	if err := Stop(h, in, now); err != nil {
		t.Fatal(err)
	}
	got := h.Backgrounds()["s1"]
	if !got.At.Equal(now) || got.Tasks["subagent"] != 2 || got.Tasks["monitor"] != 1 || got.Crons != 1 {
		t.Fatalf("background %+v", got)
	}
	if err := Stop(h, ReadInputWait(strings.NewReader(`{"session_id":"s1","hook_event_name":"Stop"}`), StdinWait), now); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.Backgrounds()["s1"]; ok {
		t.Fatal("a Stop without the task list should clear the old one")
	}
}
