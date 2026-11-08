package protocol_test

import (
	"encoding/json"
	"testing"

	"github.com/kloudkit/hubridge/internal/protocol"
	"github.com/kloudkit/hubridge/internal/schematest"
)

func TestDaemonMessagesMatchSchema(t *testing.T) {
	s := schematest.Load(t)
	end := "2026-11-04T10:00:00.000Z"
	sum := protocol.Summary{
		ID: "s1", Folder: "/w", Root: "/w", State: "ended", StartedAt: "2026-11-04T09:00:00.000Z", LastEventAt: end, EndedAt: &end,
		Tokens: []protocol.DayTokens{{Day: "2026-11-04", Input: 1}}, TodoWrites: []string{},
		Hours: []protocol.HourTokens{{Hour: "2026-11-04T13:00:00Z", Output: 2}},
	}
	withRemote := sum
	withRemote.Remote = "git@github.com:kloudkit/ws-cli.git"
	full := sum
	full.State, full.EndedAt = "waiting", nil
	full.Status, full.WaitingFor, full.Name, full.CustomTitle = "waiting", "permission prompt", "ws-c4", "Port the parser"
	full.Kind, full.Job, full.Version, full.Branch = "background", &protocol.Job{ID: "62615bd4", State: "blocked"}, "2.1.318", "main"
	full.ContinuedIn = "62615bd4-fe2c-4dff-9251-86f538657ab1"
	full.Models = []protocol.ModelTokens{{Day: "2026-11-04", Model: "claude-opus-5-5", Speed: "fast", Input: 1, CacheWrite1h: 4, WebSearch: 2}}
	full.Ledger = &protocol.Ledger{At: end, CostUSD: 1.25, LinesAdded: 3, Models: []protocol.LedgerModel{{Model: "claude-opus-5-5", CostUSD: 1.25}}}
	full.Context = &protocol.Context{At: end, Tokens: 19990, Model: "claude-opus-5-5", Requested: "claude-opus-5-5[1m]"}
	full.Compactions = []protocol.Compaction{{At: end, Trigger: "auto", PreTokens: 460643, PostTokens: 19990}}
	full.LimitHits = []protocol.LimitHit{{At: end, Type: "seven_day", ResetsAt: end, Status: "rejected"}}
	full.PRs = []protocol.PR{{Number: 198, URL: "https://github.com/kloudkit/ws-docs/pull/198", Repo: "kloudkit/ws-docs"}}
	full.Background = &protocol.Background{Tasks: map[string]int{"subagent": 2}, Crons: 1}
	for _, m := range []any{
		protocol.Hello{Type: "hello", Machine: "ws", Protocol: protocol.Version, Version: "dev", Channel: "bundled"},
		protocol.Ping{Type: "ping"},
		protocol.NewSession("m1", sum),
		protocol.NewSession("m2", withRemote),
		protocol.NewSession("m3", full),
		protocol.NewUsage(protocol.Reading{SevenDay: &protocol.Window{Utilization: 0.05, ResetsAt: "2026-11-08T06:00:00.000Z"}, Source: "probe", At: end}),
		protocol.NewUsage(protocol.Reading{FiveHour: &protocol.Window{Utilization: 0.2, ResetsAt: end}, SevenDay: &protocol.Window{Utilization: 0.4, ResetsAt: end}, Source: "statusline", At: end}),
		protocol.NewLive(nil, nil),
		protocol.Result{Type: "result", Of: "c1", OK: true},
		protocol.Result{Type: "result", Of: "c1", OK: false, Error: "nope"},
	} {
		b, _ := json.Marshal(m)
		schematest.Check(t, s.Client, b)
	}
}

func TestSchemaRejects(t *testing.T) {
	s := schematest.Load(t)
	for _, frame := range []string{
		`{"type":"ping","extra":1}`,
		`{"type":"session","session":{}}`,
		`{"type":"result","of":"c1","ok":false}`,
		`{"type":"hello","machine":"ws","protocol":1}`,
		`{"type":"session","id":"m1","session":{"id":"s","folder":"/w","repo":"/w","title":"","excerpt":"","state":"ended","startedAt":"2026-11-04T09:00:00.000Z","lastEventAt":"2026-11-04T09:00:00.000Z","endedAt":null,"tokens":[],"todoWrites":[],"hours":[{"hour":"2026-11-04T13:30:00Z","input":1,"output":0,"cacheRead":0,"cacheWrite":0}]}}`,
	} {
		v := map[string]any{}
		json.Unmarshal([]byte(frame), &v)
		if s.Client.Validate(v) == nil {
			t.Errorf("schema accepted %s", frame)
		}
	}
	for _, frame := range []string{
		`{"type":"welcome","protocol":1}`,
		`{"type":"refused","reason":"too old","minProtocol":2,"maxProtocol":2}`,
		`{"type":"ack","of":"m1"}`,
		`{"type":"error","error":"bad"}`,
		`{"type":"command","id":"c1","command":"rotate","args":{"token":"hubd_abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG"}}`,
		`{"type":"command","id":"c2","command":"sync","args":{}}`,
	} {
		schematest.Check(t, s.Server, []byte(frame))
	}
}
