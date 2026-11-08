package registry

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

func write(t *testing.T, dir, stem string, f File) {
	t.Helper()
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, stem+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

type clock struct{ now time.Time }

func fixture(t *testing.T) (*Registry, *clock) {
	t.Helper()
	r := New(t.TempDir())
	r.Claude = "claude"
	r.Domain = "linux:test:pid:[1]"
	starts := map[int32]string{100: "5000", 200: "6000", 300: "9999", 600: "7000"}
	r.StatStart = func(pid int32) (string, bool) { s, ok := starts[pid]; return s, ok }
	r.Running = func(int32) bool { return false }
	c := &clock{now: time.Date(2026, 11, 5, 18, 0, 0, 0, time.UTC)}
	r.Now = func() time.Time { return c.now }
	return r, c
}

func ids(m map[string]Session) []string {
	out := make([]string, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func TestSessionsKeepsOnlyVerifiedProcesses(t *testing.T) {
	r, _ := fixture(t)
	write(t, r.Dir, "100", File{PID: 100, SessionID: "a", ProcStart: "5000", PIDDomain: r.Domain, Status: "busy", Kind: "interactive", Name: "ws-a1", Version: "2.1.318", Entrypoint: "cli", Cwd: "/w"})
	write(t, r.Dir, "200", File{PID: 200, SessionID: "b", ProcStart: "6000", PIDDomain: r.Domain, Status: "shell"})
	write(t, r.Dir, "300", File{PID: 300, SessionID: "reused", ProcStart: "1234", PIDDomain: r.Domain, Status: "idle"})
	write(t, r.Dir, "400", File{PID: 400, SessionID: "gone", ProcStart: "1", Status: "idle"})
	write(t, r.Dir, "500", File{PID: 100, SessionID: "misnamed", ProcStart: "5000", Status: "idle"})
	write(t, r.Dir, "600", File{PID: 600, SessionID: "elsewhere", ProcStart: "7000", PIDDomain: "linux:other:pid:[2]", Status: "idle"})
	if err := os.WriteFile(filepath.Join(r.Dir, "700.json"), []byte(`{"pid":70`), 0o600); err != nil {
		t.Fatal(err)
	}

	got := r.Sessions()
	if want := []string{"a", "b"}; !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("live = %v, want %v", ids(got), want)
	}
	if want := (Session{SessionID: "a", PID: 100, Kind: "interactive", Name: "ws-a1", Status: Busy, Version: "2.1.318", Entrypoint: "cli", Cwd: "/w"}); got["a"] != want {
		t.Fatalf("a = %+v, want %+v", got["a"], want)
	}
	if got["b"].Status != Busy || got["b"].Kind != Interactive {
		t.Fatalf("b = %+v, want busy and interactive", got["b"])
	}
	if _, errs := r.Files(); errs["700"] == nil {
		t.Fatal("a half-written file should report a parse error")
	}
}

func TestUnknownStatusKeepsTheLast(t *testing.T) {
	r, _ := fixture(t)
	write(t, r.Dir, "100", File{PID: 100, SessionID: "a", ProcStart: "5000", Status: "busy"})
	if s := r.Sessions()["a"].Status; s != Busy {
		t.Fatalf("status = %q, want busy", s)
	}
	write(t, r.Dir, "100", File{PID: 100, SessionID: "a", ProcStart: "5000", Status: "pondering"})
	if s := r.Sessions()["a"].Status; s != Busy {
		t.Fatalf("status after an unknown value = %q, want the last known busy", s)
	}
}

func TestListingAddsReasonsNamesAndBackgroundSessions(t *testing.T) {
	r, _ := fixture(t)
	write(t, r.Dir, "100", File{PID: 100, SessionID: "a", ProcStart: "5000", Status: "waiting", Name: "ws-a1"})
	write(t, r.Dir, "200", File{PID: 200, SessionID: "b", ProcStart: "6000", Status: "busy"})
	r.Run = func(_ context.Context, bin string, args ...string) ([]byte, error) {
		if bin != "claude" || !reflect.DeepEqual(args, []string{"agents", "--json", "--all"}) {
			t.Fatalf("ran %s %v", bin, args)
		}
		return []byte(`[
			{"pid":100,"cwd":"/w","kind":"interactive","startedAt":1,"sessionId":"a","name":"renamed","status":"waiting","waitingFor":"permission prompt"},
			{"pid":200,"cwd":"/w","kind":"interactive","startedAt":1,"sessionId":"b","status":"waiting","waitingFor":"input needed"},
			{"id":"62615bd4","cwd":"/w/bmad","kind":"background","startedAt":1,"sessionId":"bg","name":"PR plan","state":"blocked"},
			{"pid":900,"cwd":"/w","kind":"interactive","startedAt":1,"sessionId":"gone","status":"busy"}
		]`), nil
	}
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := r.Sessions()
	if want := []string{"a", "b", "bg"}; !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("sessions = %v, want %v", ids(got), want)
	}
	if a := got["a"]; a.Status != Waiting || a.WaitingFor != "permission prompt" || a.Name != "renamed" {
		t.Fatalf("a = %+v, want waiting on a permission prompt, renamed", a)
	}
	if b := got["b"]; b.Status != Busy || b.WaitingFor != "" {
		t.Fatalf("b = %+v, want the file's newer busy and no reason", b)
	}
	if bg := got["bg"]; bg.Kind != Background || bg.JobID != "62615bd4" || bg.JobState != "blocked" || bg.Running() {
		t.Fatalf("bg = %+v, want a blocked background session with no process", bg)
	}
}

func TestStaleListingIsIgnored(t *testing.T) {
	r, c := fixture(t)
	write(t, r.Dir, "100", File{PID: 100, SessionID: "a", ProcStart: "5000", Status: "waiting"})
	r.Run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`[{"pid":100,"sessionId":"a","status":"waiting","waitingFor":"dialog open"},{"id":"x","kind":"background","sessionId":"bg","state":"working"}]`), nil
	}
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.now = c.now.Add(r.Fresh + time.Second)
	got := r.Sessions()
	if want := []string{"a"}; !reflect.DeepEqual(ids(got), want) || got["a"].WaitingFor != "" {
		t.Fatalf("with a stale listing: %+v, want only a, with no reason", got)
	}
}

func TestReconcileNeedsClaude(t *testing.T) {
	r, _ := fixture(t)
	r.Claude = ""
	if err := r.Reconcile(context.Background()); !errors.Is(err, ErrNoClaude) {
		t.Fatalf("err = %v, want ErrNoClaude", err)
	}
}

func TestUnknownJobStatePassesThrough(t *testing.T) {
	r, _ := fixture(t)
	r.Run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`[{"id":"x","kind":"background","sessionId":"bg","state":"hibernating"}]`), nil
	}
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := r.Sessions()["bg"].JobState; s != "hibernating" {
		t.Fatalf("job state = %q, want it passed through", s)
	}
}
