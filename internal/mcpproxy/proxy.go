package mcpproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/cenkalti/backoff/v5"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kloudkit/hubridge/internal/home"
	"github.com/kloudkit/hubridge/internal/mask"
	"github.com/kloudkit/hubridge/internal/status"
)

const (
	ToolsCache       = "mcp-tools.json"
	DefaultBudget    = 8 * time.Second
	DefaultStartWait = 3 * time.Second
)

type Proxy struct {
	Home      *home.Home
	Endpoint  string
	Version   string
	Status    *status.Writer
	Log       *slog.Logger
	Budget    time.Duration
	StartWait time.Duration
	Transport http.RoundTripper

	server  *mcp.Server
	client  *mcp.Client
	mu      sync.Mutex
	session *mcp.ClientSession
	names   map[string]bool

	lastMu   sync.Mutex
	lastCode int
	lastErr  error
}

func New(h *home.Home, endpoint, version string, st *status.Writer, log *slog.Logger) *Proxy {
	return &Proxy{Home: h, Endpoint: endpoint, Version: version, Status: st, Log: log, Budget: DefaultBudget, StartWait: DefaultStartWait, Transport: http.DefaultTransport}
}

func (p *Proxy) Serve(ctx context.Context, t mcp.Transport) error {
	p.server = mcp.NewServer(&mcp.Implementation{Name: "hubd", Version: p.Version}, nil)
	p.client = mcp.NewClient(&mcp.Implementation{Name: "hubridge", Version: p.Version}, nil)
	p.names = map[string]bool{}
	if tools, err := p.loadCache(); err == nil {
		p.install(tools)
	}
	startCtx, cancel := context.WithTimeout(ctx, p.StartWait)
	tools, err := p.listTools(startCtx)
	cancel()
	if err == nil {
		p.install(tools)
		p.saveCache(tools)
	} else {
		p.Log.Warn("hubd tools", "err", err)
		go p.refresh(ctx)
	}
	return p.server.Run(ctx, t)
}

func (p *Proxy) refresh(ctx context.Context) {
	wait := &backoff.ExponentialBackOff{InitialInterval: time.Second, RandomizationFactor: 0.5, Multiplier: 2, MaxInterval: time.Minute}
	wait.Reset()
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait.NextBackOff()):
		}
		callCtx, cancel := context.WithTimeout(ctx, p.Budget)
		tools, err := p.listTools(callCtx)
		cancel()
		if err == nil {
			p.install(tools)
			p.saveCache(tools)
			return
		}
	}
}

func (p *Proxy) listTools(ctx context.Context) ([]*mcp.Tool, error) {
	var tools []*mcp.Tool
	err := p.withSession(ctx, func(cs *mcp.ClientSession) error {
		tools = tools[:0]
		cursor := ""
		for {
			r, err := cs.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
			if err != nil {
				return err
			}
			tools = append(tools, r.Tools...)
			if r.NextCursor == "" {
				return nil
			}
			cursor = r.NextCursor
		}
	})
	return tools, err
}

func (p *Proxy) install(tools []*mcp.Tool) {
	seen := map[string]bool{}
	for _, t := range tools {
		seen[t.Name] = true
		name := t.Name
		p.server.AddTool(&mcp.Tool{
			Name:         t.Name,
			Title:        t.Title,
			Description:  t.Description,
			Annotations:  t.Annotations,
			InputSchema:  raw(t.InputSchema, true),
			OutputSchema: raw(t.OutputSchema, false),
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return p.call(ctx, name, req.Params.Arguments)
		})
	}
	var stale []string
	for n := range p.names {
		if !seen[n] {
			stale = append(stale, n)
		}
	}
	if len(stale) > 0 {
		p.server.RemoveTools(stale...)
	}
	p.names = seen
}

func raw(v any, required bool) any {
	if v == nil {
		if required {
			return json.RawMessage(`{"type":"object"}`)
		}
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		if required {
			return json.RawMessage(`{"type":"object"}`)
		}
		return nil
	}
	return json.RawMessage(b)
}

func (p *Proxy) call(ctx context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error) {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	callCtx, cancel := context.WithTimeout(ctx, p.Budget)
	defer cancel()
	var res *mcp.CallToolResult
	err := p.withSession(callCtx, func(cs *mcp.ClientSession) error {
		var err error
		res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		return err
	})
	var down *Unreachable
	if errors.As(err, &down) {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "hubd is unreachable (" + down.Reason + "); try again in a moment"}}}, nil
	}
	if err != nil {
		return nil, err
	}
	return res, nil
}

type Unreachable struct {
	Reason string
	Err    error
}

func (u *Unreachable) Error() string { return "hubd is unreachable: " + u.Reason }

func (u *Unreachable) Unwrap() error { return u.Err }

func (p *Proxy) withSession(ctx context.Context, fn func(*mcp.ClientSession) error) error {
	wait := &backoff.ExponentialBackOff{InitialInterval: 200 * time.Millisecond, RandomizationFactor: 0.3, Multiplier: 2, MaxInterval: 2 * time.Second}
	wait.Reset()
	for {
		cs, err := p.connect(ctx)
		if err == nil {
			err = fn(cs)
		}
		if err == nil {
			p.Status.Connected()
			return nil
		}
		var rpc *jsonrpc.Error
		if errors.As(err, &rpc) && p.code() < 400 {
			return err
		}
		p.drop()
		reason := p.reason(err)
		p.Status.Offline(reason)
		select {
		case <-ctx.Done():
			return &Unreachable{Reason: reason, Err: err}
		case <-time.After(wait.NextBackOff()):
		}
	}
}

func (p *Proxy) connect(ctx context.Context) (*mcp.ClientSession, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.session != nil {
		return p.session, nil
	}
	t := &mcp.StreamableClientTransport{Endpoint: p.Endpoint, HTTPClient: &http.Client{Transport: &auth{p}}, MaxRetries: -1, DisableStandaloneSSE: true}
	cs, err := p.client.Connect(ctx, t, nil)
	if err != nil {
		return nil, err
	}
	p.session = cs
	return cs, nil
}

func (p *Proxy) drop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.session != nil {
		_ = p.session.Close()
		p.session = nil
	}
}

type auth struct{ p *Proxy }

func (a *auth) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := a.p.Home.Token()
	if err != nil {
		a.p.record(0, err)
		return nil, err
	}
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "Bearer "+token)
	res, err := a.p.Transport.RoundTrip(r)
	if err != nil {
		a.p.record(0, err)
		return nil, err
	}
	a.p.record(res.StatusCode, nil)
	return res, nil
}

func (p *Proxy) record(code int, err error) {
	p.lastMu.Lock()
	defer p.lastMu.Unlock()
	p.lastCode, p.lastErr = code, err
}

func (p *Proxy) code() int {
	p.lastMu.Lock()
	defer p.lastMu.Unlock()
	return p.lastCode
}

func (p *Proxy) reason(err error) string {
	p.lastMu.Lock()
	code, last := p.lastCode, p.lastErr
	p.lastMu.Unlock()
	if code >= 400 {
		return fmt.Sprintf("HTTP %d", code)
	}
	if last != nil {
		err = last
	}
	return describe(err)
}

func describe(err error) string {
	var dns *net.DNSError
	switch {
	case err == nil:
		return "unknown"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	case errors.As(err, &dns):
		return "no such host"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded):
		return "timeout"
	case errors.Is(err, os.ErrNotExist):
		return "no token"
	}
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return "timeout"
	}
	s := mask.String(err.Error())
	if i := strings.LastIndex(s, ": "); i >= 0 && i < len(s)-2 {
		s = s[i+2:]
	}
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

func (p *Proxy) loadCache() ([]*mcp.Tool, error) {
	b, err := os.ReadFile(p.Home.Path(ToolsCache))
	if err != nil {
		return nil, err
	}
	var tools []*mcp.Tool
	if err := json.Unmarshal(b, &tools); err != nil {
		return nil, err
	}
	return tools, nil
}

func (p *Proxy) saveCache(tools []*mcp.Tool) {
	b, err := json.MarshalIndent(tools, "", "  ")
	if err == nil {
		err = home.WriteFile(p.Home.Path(ToolsCache), append(b, '\n'))
	}
	if err != nil {
		p.Log.Warn("write the tools cache", "err", err)
	}
}
