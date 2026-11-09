package mcpproxy

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kloudkit/hubridge/internal/home"
	"github.com/kloudkit/hubridge/internal/status"
)

const token = "hubd_testtoken_0123456789abcdef"

type upstream struct {
	*httptest.Server
	down  atomic.Bool
	calls atomic.Int32
}

func newUpstream(t *testing.T) *upstream { return newUpstreamWith(t, false) }

func newUpstreamWith(t *testing.T, withSession bool) *upstream {
	s := mcp.NewServer(&mcp.Implementation{Name: "hubd", Version: "test"}, nil)
	s.AddTool(&mcp.Tool{Name: "echo", Description: "say it back", InputSchema: json.RawMessage(`{"type":"object","properties":{"x":{"type":"integer"}}}`)},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echo " + string(req.Params.Arguments)}}}, nil
		})
	if withSession {
		s.AddTool(&mcp.Tool{Name: "note", Description: "takes a session", InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"},"session":{"type":"string"}}}`)},
			func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(req.Params.Arguments)}}}, nil
			})
	}
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{Stateless: true})
	u := &upstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.calls.Add(1)
		if u.down.Load() {
			http.Error(w, "deploying", http.StatusServiceUnavailable)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, `{"error":"This route takes a hubd client token from Settings › Access."}`, http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(u.Close)
	return u
}

type rig struct {
	home   *home.Home
	proxy  *Proxy
	status string
	cs     *mcp.ClientSession
}

func start(t *testing.T, u *upstream, tok string) *rig {
	t.Helper()
	h, _ := home.Open(t.TempDir())
	h.WriteToken(tok)
	return startIn(t, u, h)
}

func startIn(t *testing.T, u *upstream, h *home.Home) *rig {
	t.Helper()
	statusPath := filepath.Join(h.Dir, "claude", "hubd", "mcp.json")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := New(h, u.URL+"/mcp", "test", status.New(statusPath, "ws", u.URL, "test", log), log)
	p.Budget, p.StartWait = 400*time.Millisecond, 400*time.Millisecond
	ct, st := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = p.Serve(ctx, st) }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "claude", Version: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return &rig{home: h, proxy: p, status: statusPath, cs: cs}
}

func (r *rig) tools(t *testing.T) []string {
	t.Helper()
	res, err := r.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func (r *rig) echo(t *testing.T) *mcp.CallToolResult {
	t.Helper()
	res, err := r.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"x": 1}})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func text(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func (r *rig) state(t *testing.T) status.Status {
	t.Helper()
	s, err := status.Read(r.status)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestForwardsToolsAndCalls(t *testing.T) {
	u := newUpstream(t)
	r := start(t, u, token)
	if got := r.tools(t); len(got) != 1 || got[0] != "echo" {
		t.Fatalf("tools %q", got)
	}
	res := r.echo(t)
	if res.IsError || text(res) != `echo {"x":1}` {
		t.Fatalf("got %v %q", res.IsError, text(res))
	}
	if s := r.state(t); s.State != status.Connected || s.Error != "" {
		t.Fatalf("status %+v", s)
	}
	b, err := os.ReadFile(r.home.Path(ToolsCache))
	if err != nil || !strings.Contains(string(b), `"echo"`) {
		t.Fatalf("cache: %v %s", err, b)
	}
}

func TestServesCachedToolsWhileHubdIsDown(t *testing.T) {
	u := newUpstream(t)
	cache := start(t, u, token)
	cache.tools(t)
	cached, _ := os.ReadFile(cache.home.Path(ToolsCache))

	u.down.Store(true)
	h, _ := home.Open(t.TempDir())
	h.WriteToken(token)
	home.WriteFile(h.Path(ToolsCache), cached)
	r2 := startIn(t, u, h)
	if got := r2.tools(t); len(got) != 1 || got[0] != "echo" {
		t.Fatalf("cached tools %q", got)
	}
	res := r2.echo(t)
	if !res.IsError || !strings.Contains(text(res), "HTTP 503") {
		t.Fatalf("got %v %q", res.IsError, text(res))
	}
	if s := r2.state(t); s.State != status.Offline || s.Error != "HTTP 503" {
		t.Fatalf("status %+v", s)
	}

	u.down.Store(false)
	res = r2.echo(t)
	if res.IsError || text(res) != `echo {"x":1}` {
		t.Fatalf("after recovery %v %q", res.IsError, text(res))
	}
	if s := r2.state(t); s.State != status.Connected {
		t.Fatalf("status %+v", s)
	}
}

func TestRereadsTheTokenOn401(t *testing.T) {
	u := newUpstream(t)
	r := start(t, u, "hubd_stale_token_0123456789abcdef")
	if got := r.tools(t); len(got) != 0 {
		t.Fatalf("no cache, no tools expected, got %q", got)
	}
	if s := r.state(t); s.State != status.Offline || s.Error != "HTTP 401" {
		t.Fatalf("status %+v", s)
	}
	r.home.WriteToken(token)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := r.tools(t); len(got) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("tools never arrived after the token was fixed")
		}
		time.Sleep(50 * time.Millisecond)
	}
	res := r.echo(t)
	if res.IsError {
		t.Fatalf("got %q", text(res))
	}
	if s := r.state(t); s.State != status.Connected {
		t.Fatalf("status %+v", s)
	}
}

func TestDescribe(t *testing.T) {
	cases := map[string]string{
		"dial tcp 127.0.0.1:1: connect: connection refused":         "connection refused",
		"Get \"https://x/mcp\": Bearer hubd_abcdefghijklmnop: nope": "nope",
	}
	for in, want := range cases {
		if got := describe(errString(in)); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestStampsTheSessionOnCallsThatTakeOne(t *testing.T) {
	u := newUpstreamWith(t, true)
	r := start(t, u, token)
	r.proxy.Session = "sess-1"
	r.tools(t)
	call := func(name string, args map[string]any) string {
		res, err := r.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || res.IsError {
			t.Fatalf("%s: %v %q", name, err, text(res))
		}
		return text(res)
	}
	var got map[string]any
	json.Unmarshal([]byte(call("note", map[string]any{"text": "hi"})), &got)
	if got["session"] != "sess-1" || got["text"] != "hi" {
		t.Fatalf("not stamped: %v", got)
	}
	json.Unmarshal([]byte(call("note", map[string]any{"text": "hi", "session": "mine"})), &got)
	if got["session"] != "mine" {
		t.Fatalf("overwrote the caller's session: %v", got)
	}
	if s := call("echo", map[string]any{"x": 1}); s != `echo {"x":1}` {
		t.Fatalf("stamped a tool with no session argument: %s", s)
	}
}
