package transcript

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kloudkit/hubridge/internal/protocol"
)

func TestInterruptedTurnIsNotWorking(t *testing.T) {
	f := NewFile(filepath.Join("..", "sessions", "testdata", "interrupted.jsonl"), true)
	if err := f.Update(); err != nil {
		t.Fatal(err)
	}
	if f.Working {
		t.Fatal("an interrupted turn still counts as working")
	}
	if len(f.Errors) != 2 {
		t.Fatalf("want 2 parse errors, got %v", f.Errors)
	}
}

func TestTruncatedFileIsReread(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	os.WriteFile(p, []byte(`{"type":"ai-title","aiTitle":"Old"}`+"\n"+`{"type":"ai-title","aiTitle":"Older"}`+"\n"), 0o600)
	f := NewFile(p, true)
	f.Update()
	os.WriteFile(p, []byte(`{"type":"ai-title","aiTitle":"New"}`+"\n"), 0o600)
	f.Update()
	if f.Title != "New" || f.Line != 1 {
		t.Fatalf("title %q line %d", f.Title, f.Line)
	}
}

func TestIsTodoWrite(t *testing.T) {
	for name, want := range map[string]bool{
		"mcp__hubd__todo_add":            true,
		"mcp__hubd__todo_update":         true,
		"mcp__hubd__todo_done":           true,
		"mcp__hubd__todo_undo":           true,
		"mcp__plugin_hub_hubd__todo_add": true,
		"mcp__hubd__todo_list":           false,
		"mcp__hubd__todo_get":            false,
		"mcp__other__todo_add":           false,
		"mcp__hubd__apps_add":            false,
		"TodoWrite":                      false,
	} {
		if IsTodoWrite(name) != want {
			t.Errorf("%s: want %v", name, want)
		}
	}
}

func TestBashPaths(t *testing.T) {
	cases := map[string][]string{
		"git -C /w/a status":              {"/w/a"},
		"cd /w/b && git log":              {"/w/b"},
		"cd sub && make":                  {"/start/sub"},
		"ls kloudkit/hubd/protocol":       {"/start/kloudkit/hubd/protocol"},
		`grep -r x "/w/c/file.go" | head`: {"/w/c/file.go"},
		"git -C rel/dir diff":             {"/start/rel/dir"},
		"curl https://example.com/a/b":    nil,
		"echo hello":                      nil,
	}
	for cmd, want := range cases {
		got := BashPaths(cmd, "/start")
		if len(got) != len(want) {
			t.Errorf("%q: got %v want %v", cmd, got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("%q: got %v want %v", cmd, got, want)
			}
		}
	}
}

func TestReadsLedgerLinksMarkersAndUsage(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	lines := []string{
		`{"type":"custom-title","customTitle":"Port the parser"}`,
		`{"type":"user","timestamp":"2026-11-05T10:00:00.000Z","cwd":"/w","version":"2.1.318","gitBranch":"main","message":{"role":"user","content":"go"}}`,
		`{"type":"assistant","timestamp":"2026-11-05T10:00:05.000Z","requestId":"req_1","requestedModel":"claude-opus-5-5[1m]","message":{"id":"msg_1","model":"claude-opus-5-5","usage":{"input_tokens":3,"output_tokens":40,"cache_read_input_tokens":1000,"cache_creation_input_tokens":500,"cache_creation":{"ephemeral_5m_input_tokens":100,"ephemeral_1h_input_tokens":400},"server_tool_use":{"web_search_requests":2,"web_fetch_requests":1},"speed":"fast"},"content":[{"type":"text","text":"a"}]}}`,
		`{"type":"assistant","timestamp":"2026-11-05T10:00:05.000Z","requestId":"req_1","requestedModel":"claude-opus-5-5[1m]","message":{"id":"msg_1","model":"claude-opus-5-5","usage":{"input_tokens":3,"output_tokens":40,"cache_read_input_tokens":1000,"cache_creation_input_tokens":500},"content":[{"type":"text","text":"b"}]}}`,
		`{"type":"assistant","timestamp":"2026-11-05T10:01:00.000Z","isApiErrorMessage":true,"error":"rate_limit","quotaLimits":{"status":"rejected","resetsAt":1794117600,"rateLimitType":"seven_day"},"message":{"id":"msg_err","model":"<synthetic>","usage":{"input_tokens":0,"output_tokens":0},"content":[{"type":"text","text":"limit"}]}}`,
		`{"type":"system","subtype":"compact_boundary","timestamp":"2026-11-05T10:02:00.000Z","compactMetadata":{"trigger":"auto","preTokens":460643,"postTokens":19990}}`,
		`{"type":"pr-link","prNumber":198,"prUrl":"https://github.com/kloudkit/ws-docs/pull/198","prRepository":"kloudkit/ws-docs"}`,
		`{"type":"pr-link","prNumber":198,"prUrl":"https://github.com/kloudkit/ws-docs/pull/198","prRepository":"kloudkit/ws-docs"}`,
		`{"type":"continued-in","continuedInSessionId":"62615bd4-fe2c-4dff-9251-86f538657ab1"}`,
		`{"type":"cost-state","totalCostUSD":1.25,"totalLinesAdded":10,"totalLinesRemoved":2,"totalAPIDuration":5000,"totalToolDuration":700,"modelUsage":{"claude-opus-5-5":{"inputTokens":3,"outputTokens":40,"cacheReadInputTokens":1000,"cacheCreationInputTokens":500,"cacheCreationEphemeral5mInputTokens":100,"cacheCreationEphemeral1hInputTokens":400,"webSearchRequests":2,"costUSD":1.25}}}`,
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := NewFile(p, true)
	if err := f.Update(); err != nil {
		t.Fatal(err)
	}
	if f.CustomTitle != "Port the parser" || f.Version != "2.1.318" || f.Branch != "main" || f.ContinuedIn != "62615bd4-fe2c-4dff-9251-86f538657ab1" {
		t.Fatalf("title %q version %q branch %q continued %q", f.CustomTitle, f.Version, f.Branch, f.ContinuedIn)
	}
	key := Key("msg_1", "req_1")
	if fresh := f.Fresh(); !reflect.DeepEqual(fresh, []string{key}) || len(f.Fresh()) != 0 {
		t.Fatalf("fresh keys %v, want one message once", fresh)
	}
	want := &Msg{At: time.Date(2026, 11, 5, 10, 0, 5, 0, time.UTC), Model: "claude-opus-5-5", Speed: "fast", Main: true, Input: 3, Output: 40, CacheRead: 1000, Write5m: 100, Write1h: 400, WebSearch: 2, WebFetch: 1}
	if len(f.Msgs) != 1 || !reflect.DeepEqual(f.Msgs[key], want) {
		t.Fatalf("msgs %+v", f.Msgs)
	}
	if want := []protocol.LimitHit{{At: "2026-11-05T10:01:00.000Z", Type: "seven_day", ResetsAt: protocol.Time(time.Unix(1794117600, 0)), Status: "rejected"}}; !reflect.DeepEqual(f.Limits, want) {
		t.Fatalf("limits %+v", f.Limits)
	}
	if want := []protocol.Compaction{{At: "2026-11-05T10:02:00.000Z", Trigger: "auto", PreTokens: 460643, PostTokens: 19990}}; !reflect.DeepEqual(f.Compactions, want) {
		t.Fatalf("compactions %+v", f.Compactions)
	}
	if want := (&protocol.Context{At: "2026-11-05T10:02:00.000Z", Tokens: 19990, Model: "claude-opus-5-5", Requested: "claude-opus-5-5[1m]"}); !reflect.DeepEqual(f.Context, want) {
		t.Fatalf("context %+v", f.Context)
	}
	if len(f.PRs) != 1 || f.PRs[0].Number != 198 || f.PRs[0].Repo != "kloudkit/ws-docs" {
		t.Fatalf("prs %+v", f.PRs)
	}
	wantLedger := &protocol.Ledger{At: "2026-11-05T10:02:00.000Z", CostUSD: 1.25, LinesAdded: 10, LinesRemoved: 2, APIMs: 5000, ToolMs: 700, Models: []protocol.LedgerModel{
		{Model: "claude-opus-5-5", Input: 3, Output: 40, CacheRead: 1000, CacheWrite5m: 100, CacheWrite1h: 400, WebSearch: 2, CostUSD: 1.25},
	}}
	if !reflect.DeepEqual(f.Ledger, wantLedger) {
		t.Fatalf("ledger %+v", f.Ledger)
	}
	days, _, models := Aggregate([]*Msg{f.Msgs[key]}, time.Time{})
	if want := []protocol.DayTokens{{Day: "2026-11-05", Tokens: protocol.Tokens{Input: 3, Output: 40, CacheRead: 1000, CacheWrite: 500}}}; !reflect.DeepEqual(days, want) {
		t.Fatalf("days %+v", days)
	}
	if want := []protocol.ModelTokens{{Day: "2026-11-05", Model: "claude-opus-5-5", Speed: "fast", Input: 3, Output: 40, CacheRead: 1000, CacheWrite5m: 100, CacheWrite1h: 400, WebSearch: 2, WebFetch: 1}}; !reflect.DeepEqual(models, want) {
		t.Fatalf("models %+v", models)
	}
}
