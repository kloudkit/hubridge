package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kloudkit/hubridge/internal/home"
	"github.com/kloudkit/hubridge/internal/protocol"
	"github.com/kloudkit/hubridge/internal/registry"
)

func withRegistry(t *testing.T, fx *fixture) *registry.Registry {
	t.Helper()
	r := fx.tracker.Registry
	if err := os.MkdirAll(r.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	r.Claude = ""
	r.Domain = ""
	r.StatStart = func(pid int32) (string, bool) { return fmt.Sprint(pid * 10), true }
	r.Running = func(int32) bool { return true }
	now := time.Date(2026, 11, 5, 8, 30, 0, 0, time.UTC)
	r.Now = func() time.Time { return now }
	return r
}

func regFile(t *testing.T, r *registry.Registry, pid int, id, status string) {
	t.Helper()
	b, _ := json.Marshal(registry.File{PID: pid, SessionID: id, ProcStart: fmt.Sprint(pid * 10), Status: status, Kind: "interactive", Name: "n-" + id[:2], Version: "2.1.318"})
	if err := os.WriteFile(filepath.Join(r.Dir, fmt.Sprint(pid)+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func byID(list []protocol.Summary) map[string]protocol.Summary {
	out := map[string]protocol.Summary{}
	for _, s := range list {
		out[s.ID] = s
	}
	return out
}

func TestRegistryDecidesStates(t *testing.T) {
	fx := setup(t)
	r := fx.tracker.Registry
	regFile(t, r, 1, idWaiting, "waiting")
	regFile(t, r, 2, idWorking, "shell")
	r.Run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`[
			{"pid":1,"kind":"interactive","sessionId":"` + idWaiting + `","status":"waiting","waitingFor":"permission prompt"},
			{"id":"bg1","kind":"background","sessionId":"` + idEnded + `","name":"PR plan","state":"done"}
		]`), nil
	}
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	out := &sink{}
	fx.tracker.Emit(out, fx.tracker.Scan())
	got := byID(out.sessions)

	w := got[idWaiting]
	if w.State != "waiting" || w.Status != "waiting" || w.WaitingFor != "permission prompt" || w.Kind != "interactive" || w.Name != "n-11" || w.Version != "2.1.318" {
		t.Fatalf("waiting session %+v", w)
	}
	if k := got[idWorking]; k.State != "working" || k.Status != "busy" || k.WaitingFor != "" {
		t.Fatalf("working session %+v", k)
	}
	bg := got[idEnded]
	if bg.State != "waiting" || bg.Status != "idle" || bg.Kind != "background" || bg.Name != "PR plan" || !reflect.DeepEqual(bg.Job, &protocol.Job{ID: "bg1", State: "done"}) || bg.EndedAt != nil {
		t.Fatalf("background session %+v", bg)
	}
	if want := []string{idWaiting, idWorking, idEnded}; len(out.lives) != 1 || !sameSet(out.lives[0].Live, want) {
		t.Fatalf("live %+v, want %v", out.lives, want)
	}

	regFile(t, r, 1, idWaiting, "idle")
	if err := os.Remove(filepath.Join(r.Dir, "2.json")); err != nil {
		t.Fatal(err)
	}
	out = &sink{}
	fx.tracker.EmitLive(out)
	got = byID(out.sessions)
	if w := got[idWaiting]; w.State != "waiting" || w.Status != "idle" || w.WaitingFor != "" {
		t.Fatalf("after the turn ended: %+v", w)
	}
	if k := got[idWorking]; k.State != "ended" || k.EndedAt == nil || k.Status != "" {
		t.Fatalf("after its file went: %+v", k)
	}
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, x := range a {
		seen[x]++
	}
	for _, x := range b {
		seen[x]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

const (
	idOriginal = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	idCopy     = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
)

func reply(ts, id, req string, input int, sidechain bool) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":"%s","cwd":"/w","isSidechain":%t,"requestId":"%s","message":{"id":"%s","model":"m","usage":{"input_tokens":%d,"output_tokens":1}}}`, ts, sidechain, req, id, input)
}

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func forkFixture(t *testing.T) (*Tracker, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "projects")
	dir := filepath.Join(root, "-w")
	writeLines(t, filepath.Join(dir, idOriginal+".jsonl"),
		reply("2026-11-05T09:00:00.000Z", "msg_1", "r1", 100, false),
		reply("2026-11-05T09:01:00.000Z", "msg_2", "r2", 10, false),
		`{"type":"continued-in","continuedInSessionId":"`+idCopy+`"}`)
	writeLines(t, filepath.Join(dir, idOriginal, "subagents", "agent-x.jsonl"),
		reply("2026-11-05T09:01:00.000Z", "msg_2", "r2", 10, true),
		reply("2026-11-05T09:02:00.000Z", "msg_4", "r4", 1000, true))
	writeLines(t, filepath.Join(dir, idOriginal, "subagents", "workflows", "wf1", "agent-y.jsonl"),
		reply("2026-11-05T09:03:00.000Z", "msg_5", "r5", 5000, true))
	writeLines(t, filepath.Join(dir, idOriginal, "subagents", "workflows", "wf1", "journal.jsonl"),
		reply("2026-11-05T09:03:00.000Z", "msg_6", "r6", 70000, true))
	writeLines(t, filepath.Join(dir, idCopy+".jsonl"),
		reply("2026-11-05T09:00:00.000Z", "msg_1", "r1", 100, false),
		reply("2026-11-05T09:10:00.000Z", "msg_3", "r3", 1, false))
	tr := New(root, slog.New(slog.NewTextHandler(&strings.Builder{}, nil)))
	tr.Now = func() time.Time { return time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC) }
	return tr, dir
}

func inputOf(s protocol.Summary) int64 {
	var n int64
	for _, d := range s.Tokens {
		n += d.Input
	}
	return n
}

func TestCopiedMessagesCountOnceForTheOriginal(t *testing.T) {
	tr, _ := forkFixture(t)
	out := &sink{}
	tr.Emit(out, tr.Scan())
	got := byID(out.sessions)
	if n := inputOf(got[idOriginal]); n != 100+10+1000+5000 {
		t.Fatalf("original input %d, want its own messages once, workflow agents included and the journal left out", n)
	}
	if n := inputOf(got[idCopy]); n != 1 {
		t.Fatalf("copy input %d, want only the message it added", n)
	}
}

func TestOwnershipMovesWhenTheOriginalIsReadLater(t *testing.T) {
	tr, dir := forkFixture(t)
	out := &sink{}
	id, _ := tr.Touch(filepath.Join(dir, idCopy+".jsonl"))
	tr.Emit(out, []string{id})
	if n := inputOf(byID(out.sessions)[idCopy]); n != 101 {
		t.Fatalf("copy read alone: input %d, want 101", n)
	}
	out = &sink{}
	id, _ = tr.Touch(filepath.Join(dir, idOriginal+".jsonl"))
	tr.Emit(out, []string{id})
	got := byID(out.sessions)
	if n := inputOf(got[idCopy]); n != 1 {
		t.Fatalf("after the original was read: copy input %d, want 1, re-sent", n)
	}
	if n := inputOf(got[idOriginal]); n != 110 {
		t.Fatalf("original input %d, want 110", n)
	}
}

func TestWatchFollowsRegistryChanges(t *testing.T) {
	fx := setup(t)
	r := fx.tracker.Registry
	r.Claude = "claude"
	calls := make(chan struct{}, 10)
	r.Run = func(context.Context, string, ...string) ([]byte, error) {
		calls <- struct{}{}
		if len(calls) == 1 {
			return []byte(`[]`), nil
		}
		return []byte(`[{"pid":2,"kind":"interactive","sessionId":"` + idWorking + `","status":"waiting","waitingFor":"input needed"}]`), nil
	}
	regFile(t, r, 2, idWorking, "busy")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chanSink, 100)
	go fx.tracker.Watch(ctx, out, Intervals{Debounce: 20 * time.Millisecond, Liveness: time.Hour, Rescan: time.Hour, Reconcile: time.Hour, Reason: 0})
	wait := func(what string, ok func(protocol.Summary) bool) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case s := <-out:
				if s.ID == idWorking && ok(s) {
					return
				}
			case <-deadline:
				t.Fatalf("no summary where %s", what)
			}
		}
	}
	wait("the session works", func(s protocol.Summary) bool { return s.State == "working" && s.Status == "busy" })
	time.Sleep(50 * time.Millisecond)
	regFile(t, r, 2, idWorking, "waiting")
	wait("it waits on input, with the reason from claude agents --json", func(s protocol.Summary) bool {
		return s.State == "waiting" && s.Status == "waiting" && s.WaitingFor == "input needed"
	})
	if len(calls) < 2 {
		t.Fatalf("claude agents --json ran %d times, want at start and again for the reason", len(calls))
	}
}

func TestIdleWithBackgroundWorkIsWorking(t *testing.T) {
	fx := setup(t)
	r := fx.tracker.Registry
	regFile(t, r, 1, idWaiting, "idle")
	regFile(t, r, 2, idWorking, "idle")
	bgs := map[string]home.Background{
		idWaiting: {Tasks: map[string]int{"subagent": 2, "monitor": 1}},
		idWorking: {Crons: 1},
		idEnded:   {Tasks: map[string]int{"shell": 1}},
	}
	var cleared []string
	fx.tracker.Background = func() map[string]home.Background { return bgs }
	fx.tracker.ClearBackground = func(id string) { cleared = append(cleared, id) }
	out := &sink{}
	fx.tracker.Emit(out, fx.tracker.Scan())
	got := byID(out.sessions)
	if w := got[idWaiting]; w.State != "working" || w.Status != "idle" || !reflect.DeepEqual(w.Background, &protocol.Background{Tasks: map[string]int{"subagent": 2, "monitor": 1}}) {
		t.Fatalf("idle with a subagent and a monitor running: %+v", w)
	}
	if k := got[idWorking]; k.State != "waiting" || !reflect.DeepEqual(k.Background, &protocol.Background{Crons: 1}) {
		t.Fatalf("idle with only a scheduled wake-up: %+v", k)
	}
	if e := got[idEnded]; e.State != "ended" || e.Background != nil || !reflect.DeepEqual(cleared, []string{idEnded}) {
		t.Fatalf("ended: %+v, cleared %v", e, cleared)
	}
}
