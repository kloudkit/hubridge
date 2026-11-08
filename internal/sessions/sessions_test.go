package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kloudkit/hubridge/internal/protocol"
)

const (
	idWaiting = "11111111-1111-4111-8111-111111111111"
	idWorking = "22222222-2222-4222-8222-222222222222"
	idEnded   = "33333333-3333-4333-8333-333333333333"
)

type sink struct {
	sessions []protocol.Summary
	lives    []protocol.Live
}

func (s *sink) Session(x protocol.Summary) { s.sessions = append(s.sessions, x) }
func (s *sink) Live(l protocol.Live)       { s.lives = append(s.lives, l) }

type fixture struct {
	root, repo, nogit string
	log               *bytes.Buffer
	tracker           *Tracker
}

func place(t *testing.T, fx *fixture, name, dst string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	s := strings.ReplaceAll(string(b), "__FOLDER__", fx.repo)
	s = strings.ReplaceAll(s, "__NOGIT__", fx.nogit)
	s = strings.ReplaceAll(s, "__BASE__", filepath.Dir(fx.repo))
	dst = filepath.Join(fx.root, dst)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func setup(t *testing.T) *fixture {
	t.Helper()
	base := t.TempDir()
	fx := &fixture{root: filepath.Join(base, "projects"), repo: filepath.Join(base, "repo"), nogit: filepath.Join(base, "nogit"), log: &bytes.Buffer{}}
	for _, d := range []string{filepath.Join(fx.repo, ".git"), filepath.Join(fx.repo, "sub"), fx.nogit} {
		os.MkdirAll(d, 0o700)
	}
	place(t, fx, "waiting.jsonl", "-repo-sub/"+idWaiting+".jsonl")
	place(t, fx, "working.jsonl", "-repo/"+idWorking+".jsonl")
	place(t, fx, "subagent.jsonl", "-repo/"+idWorking+"/subagents/agent-a1.jsonl")
	place(t, fx, "interrupted.jsonl", "-nogit/"+idEnded+".jsonl")
	fx.tracker = New(fx.root, slog.New(slog.NewTextHandler(fx.log, nil)))
	fx.tracker.Now = func() time.Time { return time.Date(2026, 11, 5, 8, 30, 0, 0, time.UTC) }
	r := withRegistry(t, fx)
	regFile(t, r, 1, idWaiting, "")
	regFile(t, r, 2, idWorking, "")
	return fx
}

func ptr(s string) *string { return &s }

func TestSummariesFromFixtures(t *testing.T) {
	fx := setup(t)
	out := &sink{}
	fx.tracker.Emit(out, fx.tracker.Scan())

	want := []protocol.Summary{
		{
			ID: idWaiting, Folder: fx.repo + "/sub", Root: fx.repo, Title: "Latest title",
			Excerpt: "Add a todo for the fixture and fix TOKEN=‹masked›", State: "waiting",
			Kind: "interactive", Name: "n-11", Version: "2.1.318",
			StartedAt: "2026-11-03T23:58:00.000Z", LastEventAt: "2026-11-04T00:01:05.000Z",
			Tokens: []protocol.DayTokens{
				{Day: "2026-11-03", Tokens: protocol.Tokens{Input: 10, Output: 20, CacheRead: 300, CacheWrite: 40}},
				{Day: "2026-11-04", Tokens: protocol.Tokens{Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4}},
			},
			Hours: []protocol.HourTokens{
				{Hour: "2026-11-03T23:00:00Z", Tokens: protocol.Tokens{Input: 10, Output: 20, CacheRead: 300, CacheWrite: 40}},
				{Hour: "2026-11-04T00:00:00Z", Tokens: protocol.Tokens{Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4}},
			},
			TodoWrites: []string{"T1", "T2", "T5"},
			Models: []protocol.ModelTokens{
				{Day: "2026-11-03", Input: 10, Output: 20, CacheRead: 300, CacheWrite5m: 40},
				{Day: "2026-11-04", Input: 1, Output: 2, CacheRead: 3, CacheWrite5m: 4},
			},
			Context: &protocol.Context{At: "2026-11-04T00:01:00.000Z", Tokens: 8},
		},
		{
			ID: idWorking, Folder: fx.repo, Root: fx.repo, Excerpt: "Second prompt, still running", State: "working",
			Kind: "interactive", Name: "n-22", Version: "2.1.318",
			StartedAt: "2026-11-04T08:00:00.000Z", LastEventAt: "2026-11-04T08:07:02.000Z",
			Tokens:     []protocol.DayTokens{{Day: "2026-11-04", Tokens: protocol.Tokens{Input: 5, Output: 6, CacheRead: 7, CacheWrite: 8}}},
			Hours:      []protocol.HourTokens{{Hour: "2026-11-04T08:00:00Z", Tokens: protocol.Tokens{Input: 5, Output: 6, CacheRead: 7, CacheWrite: 8}}},
			TodoWrites: []string{"T9"},
			Models:     []protocol.ModelTokens{{Day: "2026-11-04", Input: 5, Output: 6, CacheRead: 7, CacheWrite5m: 8}},
		},
		{
			ID: idEnded, Folder: fx.nogit + "/gone", Root: fx.nogit + "/gone", State: "ended",
			StartedAt: "2026-11-02T10:00:00.000Z", LastEventAt: "2026-11-02T10:00:05.000Z", EndedAt: ptr("2026-11-02T10:00:05.000Z"),
			Tokens: []protocol.DayTokens{}, TodoWrites: []string{},
		},
	}
	if !reflect.DeepEqual(out.sessions, want) {
		got, _ := json.MarshalIndent(out.sessions, "", " ")
		t.Fatalf("summaries:\n%s", got)
	}
	wantLive := protocol.Live{Type: "live", Live: []string{idWaiting, idWorking}, Present: []string{idWaiting, idWorking}}
	if len(out.lives) != 1 || !reflect.DeepEqual(out.lives[0], wantLive) {
		t.Fatalf("live: %+v", out.lives)
	}
	if n := strings.Count(fx.log.String(), "transcript parse failure"); n != 2 {
		t.Fatalf("want 2 parse failures logged, got %d:\n%s", n, fx.log)
	}

	out2 := &sink{}
	fx.tracker.Emit(out2, fx.tracker.Scan())
	if len(out2.sessions) != 0 || len(out2.lives) != 0 {
		t.Fatalf("unchanged transcripts were re-sent: %+v", out2)
	}
}

func TestEndedWhenProcessGoes(t *testing.T) {
	fx := setup(t)
	fx.tracker.Emit(&sink{}, fx.tracker.Scan())
	if err := os.Remove(filepath.Join(fx.tracker.Registry.Dir, "2.json")); err != nil {
		t.Fatal(err)
	}
	out := &sink{}
	fx.tracker.EmitLive(out)
	if len(out.sessions) != 1 || out.sessions[0].ID != idWorking || out.sessions[0].State != "ended" || *out.sessions[0].EndedAt != "2026-11-04T08:07:02.000Z" {
		t.Fatalf("got %+v", out.sessions)
	}
	if len(out.lives) != 1 || !reflect.DeepEqual(out.lives[0].Live, []string{idWaiting}) {
		t.Fatalf("live: %+v", out.lives)
	}
}

func TestIncrementalAppend(t *testing.T) {
	fx := setup(t)
	fx.tracker.Emit(&sink{}, fx.tracker.Scan())
	path := filepath.Join(fx.root, "-repo", idWorking+".jsonl")
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"type":"system","subtype":"turn_duration","isSidechain":false,"timestamp":"2026-11-04T08:09:00.000Z"}` + "\n")
	f.WriteString(`{"type":"ai-title","aiTitle":"Partial`)
	f.Close()

	out := &sink{}
	id, _ := fx.tracker.Touch(path)
	fx.tracker.Emit(out, []string{id})
	if len(out.sessions) != 1 || out.sessions[0].State != "waiting" || out.sessions[0].Title != "" || out.sessions[0].LastEventAt != "2026-11-04T08:09:00.000Z" {
		t.Fatalf("after turn end: %+v", out.sessions)
	}

	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(` title"}` + "\n")
	f.Close()
	out = &sink{}
	fx.tracker.Touch(path)
	fx.tracker.Emit(out, []string{id})
	if len(out.sessions) != 1 || out.sessions[0].Title != "Partial title" {
		t.Fatalf("after completing the line: %+v", out.sessions)
	}
}

func TestExcerptIsCappedAfterMasking(t *testing.T) {
	long := strings.Repeat("word ", 30) + "ghp_" + strings.Repeat("A1", 20) + " " + strings.Repeat("é", 300)
	ex := Excerpt(long)
	if n := len([]rune(ex)); n != ExcerptLen {
		t.Fatalf("excerpt has %d runes", n)
	}
	if strings.Contains(ex, "ghp_") || !strings.Contains(ex, "‹masked›") {
		t.Fatalf("excerpt not masked: %q", ex)
	}
}

type chanSink chan protocol.Summary

func (c chanSink) Session(s protocol.Summary) { c <- s }
func (c chanSink) Live(protocol.Live)         {}

func TestWatchPicksUpNewTranscripts(t *testing.T) {
	fx := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chanSink, 100)
	go fx.tracker.Watch(ctx, out, Intervals{Debounce: 20 * time.Millisecond, Liveness: time.Hour, Rescan: time.Hour})
	for i := 0; i < 3; i++ {
		select {
		case <-out:
		case <-time.After(5 * time.Second):
			t.Fatal("initial scan not emitted")
		}
	}
	const id = "44444444-4444-4444-8444-444444444444"
	time.Sleep(50 * time.Millisecond)
	place(t, fx, "working.jsonl", "-new-project/"+id+".jsonl")
	deadline := time.After(5 * time.Second)
	for {
		select {
		case s := <-out:
			if s.ID == id && s.State == "ended" && s.Folder == fx.repo {
				return
			}
		case <-deadline:
			t.Fatal("new transcript not picked up")
		}
	}
}

func TestRepoIsWhereTheEditsWent(t *testing.T) {
	fx := setup(t)
	base := filepath.Dir(fx.repo)
	other := filepath.Join(base, "other")
	os.MkdirAll(filepath.Join(other, ".git"), 0o700)
	const id = "55555555-5555-4555-8555-555555555555"
	place(t, fx, "workspace.jsonl", "-base/"+id+".jsonl")
	place(t, fx, "workspace-subagent.jsonl", "-base/"+id+"/subagents/agent-b1.jsonl")
	out := &sink{}
	fx.tracker.Emit(out, fx.tracker.Scan())
	var got *protocol.Summary
	for i := range out.sessions {
		if out.sessions[i].ID == id {
			got = &out.sessions[i]
		}
	}
	if got == nil || got.Folder != base || got.Root != fx.repo {
		t.Fatalf("want folder %s repo %s, got %+v", base, fx.repo, got)
	}

	path := filepath.Join(fx.root, "-base", id+".jsonl")
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"type":"assistant","entrypoint":"cli","isSidechain":false,"timestamp":"2026-11-04T09:04:00.000Z","message":{"id":"msg_w3","content":[{"type":"tool_use","id":"tu_w","name":"Write","input":{"file_path":"` + other + `/w.go","content":"x"}}]}}` + "\n")
	f.Close()
	out = &sink{}
	fx.tracker.Touch(path)
	fx.tracker.Emit(out, []string{id})
	if len(out.sessions) != 1 || out.sessions[0].Root != other || out.sessions[0].Folder != base {
		t.Fatalf("a tie should go to the most recent edit: %+v", out.sessions)
	}
}

func TestRepoFallsBackToStartFolder(t *testing.T) {
	fx := setup(t)
	fx.tracker.Emit(&sink{}, fx.tracker.Scan())
	fx.tracker.mu.Lock()
	defer fx.tracker.mu.Unlock()
	if r := fx.tracker.root(fx.repo+"/sub", nil); r != fx.repo {
		t.Errorf("no edits: want git root of the folder, got %s", r)
	}
	if r := fx.tracker.root(fx.nogit+"/x", nil); r != fx.nogit+"/x" {
		t.Errorf("no edits, no git: want the folder, got %s", r)
	}
}

func TestHeadlessRunsAreSkipped(t *testing.T) {
	fx := setup(t)
	const id = "66666666-6666-4666-8666-666666666666"
	place(t, fx, "headless.jsonl", "-base/"+id+".jsonl")
	out := &sink{}
	fx.tracker.Emit(out, fx.tracker.Scan())
	for _, s := range out.sessions {
		if s.ID == id {
			t.Fatalf("headless run sent: %+v", s)
		}
	}
	for _, l := range out.lives {
		for _, x := range append(l.Live, l.Present...) {
			if x == id {
				t.Fatalf("headless run listed in live: %+v", l)
			}
		}
	}
	if len(out.sessions) != 3 {
		t.Fatalf("interactive sessions: %d", len(out.sessions))
	}
}

func TestRepoFromReadsAndBashWhenNothingWasEdited(t *testing.T) {
	fx := setup(t)
	base := filepath.Dir(fx.repo)
	other := filepath.Join(base, "other")
	os.MkdirAll(filepath.Join(other, ".git"), 0o700)
	const id = "77777777-7777-4777-8777-777777777777"
	place(t, fx, "readonly.jsonl", "-base/"+id+".jsonl")
	out := &sink{}
	fx.tracker.Emit(out, fx.tracker.Scan())
	for _, s := range out.sessions {
		if s.ID == id {
			if s.Root != other || s.Folder != base {
				t.Fatalf("want repo %s folder %s, got %+v", other, base, s)
			}
			return
		}
	}
	t.Fatal("session not sent")
}

func TestHoursKeepOnlyTheLast48Hours(t *testing.T) {
	fx := setup(t)
	fx.tracker.Now = func() time.Time { return time.Date(2026, 11, 6, 0, 30, 0, 0, time.UTC) }
	out := &sink{}
	fx.tracker.Emit(out, fx.tracker.Scan())
	for _, s := range out.sessions {
		if s.ID != idWaiting {
			continue
		}
		want := []protocol.HourTokens{{Hour: "2026-11-04T00:00:00Z", Tokens: protocol.Tokens{Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4}}}
		if !reflect.DeepEqual(s.Hours, want) {
			t.Fatalf("hours %+v", s.Hours)
		}
		if len(s.Tokens) != 2 {
			t.Fatalf("day totals must not be windowed: %+v", s.Tokens)
		}
		return
	}
	t.Fatal("session not sent")
}
