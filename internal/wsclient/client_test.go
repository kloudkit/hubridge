package wsclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/kloudkit/hubridge/internal/home"
	"github.com/kloudkit/hubridge/internal/protocol"
	"github.com/kloudkit/hubridge/internal/schematest"
	"github.com/kloudkit/hubridge/internal/status"
)

var (
	oldToken = "hubd_" + strings.Repeat("a", 43)
	newToken = "hubd_" + strings.Repeat("b", 43)
)

type frame struct {
	Type    string           `json:"type"`
	Usage   protocol.Reading `json:"usage"`
	ID      string           `json:"id"`
	Of      string           `json:"of"`
	OK      bool             `json:"ok"`
	Error   string           `json:"error"`
	Session protocol.Summary `json:"session"`
	Live    []string         `json:"live"`
}

type peer struct {
	t      *testing.T
	conn   *websocket.Conn
	auth   string
	frames chan frame
}

type fakeServer struct {
	t       *testing.T
	schemas schematest.Schemas
	peers   chan *peer
}

func (s *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/client/ws" {
		http.NotFound(w, r)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	p := &peer{t: s.t, conn: conn, auth: r.Header.Get("Authorization"), frames: make(chan frame, 100)}
	s.peers <- p
	for {
		_, b, err := conn.Read(context.Background())
		if err != nil {
			close(p.frames)
			return
		}
		schematest.Check(s.t, s.schemas.Client, b)
		var f frame
		json.Unmarshal(b, &f)
		p.frames <- f
	}
}

func (p *peer) send(s schematest.Schemas, v string) {
	p.t.Helper()
	schematest.Check(p.t, s.Server, []byte(v))
	if err := p.conn.Write(context.Background(), websocket.MessageText, []byte(v)); err != nil {
		p.t.Fatal(err)
	}
}

func (p *peer) next(skipPings bool) frame {
	p.t.Helper()
	for {
		select {
		case f, ok := <-p.frames:
			if !ok {
				p.t.Fatal("connection closed")
			}
			if skipPings && f.Type == "ping" {
				continue
			}
			return f
		case <-time.After(5 * time.Second):
			p.t.Fatal("timed out waiting for a frame")
		}
	}
}

func (s *fakeServer) accept(t *testing.T) *peer {
	t.Helper()
	select {
	case p := <-s.peers:
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not connect")
		return nil
	}
}

func summary(id, state string) protocol.Summary {
	return protocol.Summary{
		ID: id, Folder: "/w", Root: "/w", State: state, StartedAt: "2026-11-04T09:00:00.000Z", LastEventAt: "2026-11-04T09:01:00.000Z",
		Tokens: []protocol.DayTokens{}, TodoWrites: []string{},
	}
}

func statusFile(t *testing.T, url string) *status.Writer {
	t.Helper()
	return status.New(filepath.Join(t.TempDir(), "claude", "hubd", "status.json"), "ws", url, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func tokenHome(t *testing.T) *home.Home {
	t.Helper()
	h, err := home.Open(filepath.Join(t.TempDir(), "home"))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.WriteToken(oldToken); err != nil {
		t.Fatal(err)
	}
	return h
}

func readStatus(t *testing.T, w *status.Writer) status.Status {
	t.Helper()
	s, err := status.Read(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func wantStatus(t *testing.T, w *status.Writer, state, reason string) status.Status {
	t.Helper()
	s := readStatus(t, w)
	if s.State != state || s.Error != reason {
		t.Fatalf("status %s %q, want %s %q", s.State, s.Error, state, reason)
	}
	return s
}

func TestClientLifecycle(t *testing.T) {
	schemas := schematest.Load(t)
	srv := &fakeServer{t: t, schemas: schemas, peers: make(chan *peer, 4)}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	h, err := home.Open(filepath.Join(t.TempDir(), "home"))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.WriteToken(oldToken); err != nil {
		t.Fatal(err)
	}
	snap := func() ([]protocol.Summary, protocol.Live) {
		return []protocol.Summary{summary("live-1", "working")}, protocol.NewLive([]string{"live-1"}, []string{"live-1"})
	}
	c := New(ts.URL, "ws", "test", "bundled", h, snap, slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.Ping = 50 * time.Millisecond
	c.MinBackoff = 10 * time.Millisecond
	c.MaxBackoff = 20 * time.Millisecond
	c.Status = statusFile(t, ts.URL)
	c.Session(summary("old-1", "ended"))

	done := make(chan error, 1)
	go func() { done <- c.Run(context.Background()) }()

	p := srv.accept(t)
	if p.auth != "Bearer "+oldToken {
		t.Fatal("first connection did not carry the token")
	}
	if f := p.next(false); f.Type != "hello" {
		t.Fatalf("first frame %q", f.Type)
	}
	p.send(schemas, `{"type":"welcome","protocol":1}`)
	sessions := map[string]string{}
	for len(sessions) < 2 {
		f := p.next(true)
		if f.Type != "session" {
			t.Fatalf("got %q before both sessions", f.Type)
		}
		sessions[f.Session.ID] = f.ID
	}
	if f := p.next(true); f.Type != "live" || len(f.Live) != 1 || f.Live[0] != "live-1" {
		t.Fatalf("want live after sessions, got %+v", f)
	}
	if f := p.next(false); f.Type != "ping" {
		t.Fatalf("want ping, got %q", f.Type)
	}
	p.send(schemas, `{"type":"ack","of":"`+sessions["live-1"]+`"}`)
	p.send(schemas, `{"type":"error","error":"ignored by the test"}`)
	p.conn.Close(websocket.StatusInternalError, "restart")

	p = srv.accept(t)
	p.next(false)
	p.send(schemas, `{"type":"welcome","protocol":1}`)
	resent := map[string]string{}
	for len(resent) < 2 {
		f := p.next(true)
		resent[f.Session.ID] = f.ID
	}
	if resent["old-1"] != sessions["old-1"] {
		t.Fatalf("unacked message re-sent with a new id: %s vs %s", resent["old-1"], sessions["old-1"])
	}
	p.next(true)
	p.send(schemas, `{"type":"ack","of":"`+resent["old-1"]+`"}`)
	p.send(schemas, `{"type":"ack","of":"`+resent["live-1"]+`"}`)

	p.send(schemas, `{"type":"command","id":"c1","command":"rotate","args":{"token":"`+newToken+`"}}`)
	if f := p.next(true); f.Type != "result" || f.Of != "c1" || !f.OK {
		t.Fatalf("rotate result %+v", f)
	}
	p = srv.accept(t)
	if p.auth != "Bearer "+newToken {
		t.Fatal("reconnect after rotate did not use the new token")
	}
	b, _ := os.ReadFile(filepath.Join(h.Dir, "token"))
	if strings.TrimSpace(string(b)) != newToken {
		t.Fatal("token file not rotated")
	}
	if st, _ := os.Stat(filepath.Join(h.Dir, "token")); st.Mode().Perm() != 0o600 {
		t.Fatalf("token mode %o", st.Mode().Perm())
	}
	if q := c.Queue.Pending(); len(q) != 0 {
		t.Fatalf("acked messages still queued: %d", len(q))
	}

	p.next(false)
	p.send(schemas, `{"type":"welcome","protocol":1}`)
	p.next(true)
	p.send(schemas, `{"type":"command","id":"c2","command":"restore","args":{"sessionId":"s1"}}`)
	for {
		f := p.next(true)
		if f.Type == "result" {
			if f.Of != "c2" || f.OK {
				t.Fatalf("unsupported command answered %+v", f)
			}
			break
		}
	}
	p.send(schemas, `{"type":"refused","reason":"too old","minProtocol":2,"maxProtocol":2}`)
	select {
	case err := <-done:
		if !errors.Is(err, ErrRefused) {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon kept running after refused")
	}
	wantStatus(t, c.Status, status.Offline, "refused: protocol 1 too old")
	select {
	case <-srv.peers:
		t.Fatal("daemon reconnected after refused")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRefusedByCloseCode(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		conn.Read(context.Background())
		conn.Close(CloseRefused, "refused")
	}))
	defer ts.Close()
	h, _ := home.Open(t.TempDir())
	h.WriteToken(oldToken)
	c := New(ts.URL, "ws", "test", "bundled", h, func() ([]protocol.Summary, protocol.Live) { return nil, protocol.NewLive(nil, nil) }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.MinBackoff = time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Run(ctx); !errors.Is(err, ErrRefused) {
		t.Fatalf("Run returned %v", err)
	}
}

func TestRevokedStopsReconnecting(t *testing.T) {
	for reason, want := range map[string]string{"token revoked": "token revoked: rerun the installer", "machine removed": "machine removed: rerun the installer"} {
		schemas := schematest.Load(t)
		srv := &fakeServer{t: t, schemas: schemas, peers: make(chan *peer, 4)}
		ts := httptest.NewServer(srv)
		h, _ := home.Open(t.TempDir())
		h.WriteToken(oldToken)
		c := New(ts.URL, "ws", "test", "bundled", h, func() ([]protocol.Summary, protocol.Live) { return nil, protocol.NewLive(nil, nil) }, slog.New(slog.NewTextHandler(io.Discard, nil)))
		c.MinBackoff, c.MaxBackoff = time.Millisecond, time.Millisecond
		c.Status = statusFile(t, ts.URL)
		done := make(chan error, 1)
		go func() { done <- c.Run(context.Background()) }()
		p := srv.accept(t)
		p.next(false)
		p.send(schemas, `{"type":"welcome","protocol":1}`)
		p.next(true)
		p.conn.Close(CloseRevoked, reason)
		select {
		case err := <-done:
			if !errors.Is(err, ErrRevoked) || err.Error() != want {
				t.Fatalf("%s: Run returned %v", reason, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: daemon kept running", reason)
		}
		wantStatus(t, c.Status, status.Offline, reason)
		select {
		case <-srv.peers:
			t.Fatalf("%s: daemon reconnected after 4003", reason)
		case <-time.After(50 * time.Millisecond):
		}
		ts.Close()
	}
}

func TestUpgradeStatus(t *testing.T) {
	for code, revoked := range map[int]bool{http.StatusUnauthorized: true, http.StatusServiceUnavailable: false, http.StatusForbidden: false} {
		hits := 0
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits++
			w.WriteHeader(code)
		}))
		h, _ := home.Open(t.TempDir())
		h.WriteToken(oldToken)
		c := New(ts.URL, "ws", "test", "bundled", h, func() ([]protocol.Summary, protocol.Live) { return nil, protocol.NewLive(nil, nil) }, slog.New(slog.NewTextHandler(io.Discard, nil)))
		c.MinBackoff, c.MaxBackoff = time.Millisecond, 2*time.Millisecond
		c.Status = statusFile(t, ts.URL)
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		err := c.Run(ctx)
		cancel()
		ts.Close()
		if revoked {
			if !errors.Is(err, ErrRevoked) || err.Error() != "token rejected: rerun the installer" || hits != 1 {
				t.Fatalf("401: err %v after %d tries", err, hits)
			}
			wantStatus(t, c.Status, status.Offline, "token rejected")
		} else if err != nil || hits < 2 {
			t.Fatalf("%d: want retries with backoff, got err %v after %d tries", code, err, hits)
		} else {
			wantStatus(t, c.Status, status.Offline, "stopped")
		}
	}
}

func TestUsageIsSentAfterConnectAndOnChange(t *testing.T) {
	schemas := schematest.Load(t)
	srv := &fakeServer{t: t, schemas: schemas, peers: make(chan *peer, 4)}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	h, _ := home.Open(filepath.Join(t.TempDir(), "home"))
	h.WriteToken(oldToken)
	snap := func() ([]protocol.Summary, protocol.Live) { return nil, protocol.NewLive(nil, nil) }
	c := New(ts.URL, "ws", "test", "bundled", h, snap, slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.Ping = time.Hour
	at := "2026-11-05T18:00:00.000Z"
	c.Usage(protocol.Reading{SevenDay: &protocol.Window{Utilization: 0.05, ResetsAt: at}, Source: "probe", At: at})
	go c.Run(t.Context())

	p := srv.accept(t)
	p.next(false)
	p.send(schemas, `{"type":"welcome","protocol":1}`)
	if f := p.next(true); f.Type != "live" {
		t.Fatalf("want live first, got %q", f.Type)
	}
	if f := p.next(true); f.Type != "usage" || f.Usage.SevenDay == nil || f.Usage.SevenDay.Utilization != 0.05 {
		t.Fatalf("want the usage reading, got %+v", f)
	}
	c.Usage(protocol.Reading{SevenDay: &protocol.Window{Utilization: 0.06, ResetsAt: at}, Source: "statusline", At: at})
	if f := p.next(true); f.Type != "usage" || f.Usage.Source != "statusline" {
		t.Fatalf("a new reading was not sent: %+v", f)
	}
}

type fakeRules struct {
	t    *testing.T
	fail func(n int) error

	mu       sync.Mutex
	inflight int
	at       []time.Time
	ctxs     []context.Context
	called   chan struct{}
}

func (r *fakeRules) refresh(ctx context.Context) error {
	r.mu.Lock()
	r.inflight++
	n := len(r.at)
	r.at = append(r.at, time.Now())
	r.ctxs = append(r.ctxs, ctx)
	overlap := r.inflight > 1
	r.mu.Unlock()
	if overlap {
		r.t.Error("two refreshes ran at once")
	}
	time.Sleep(time.Millisecond)
	r.mu.Lock()
	r.inflight--
	r.mu.Unlock()
	r.called <- struct{}{}
	return r.fail(n)
}

func (r *fakeRules) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.at)
}

func (r *fakeRules) wait(why string) {
	r.t.Helper()
	select {
	case <-r.called:
	case <-time.After(5 * time.Second):
		r.t.Fatalf("no refresh %s", why)
	}
}

func (p *peer) quiet(d time.Duration) {
	p.t.Helper()
	select {
	case f, ok := <-p.frames:
		if ok {
			p.t.Fatalf("want no frame, got %+v", f)
		}
	case <-time.After(d):
	}
}

func rulesClient(t *testing.T, retry, most time.Duration, fail func(n int) error) (*fakeServer, schematest.Schemas, *fakeRules) {
	t.Helper()
	schemas := schematest.Load(t)
	srv := &fakeServer{t: t, schemas: schemas, peers: make(chan *peer, 4)}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	h, err := home.Open(filepath.Join(t.TempDir(), "home"))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.WriteToken(oldToken); err != nil {
		t.Fatal(err)
	}
	c := New(ts.URL, "ws", "test", "bundled", h, func() ([]protocol.Summary, protocol.Live) { return nil, protocol.NewLive(nil, nil) }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.Ping = time.Hour
	c.MinBackoff, c.MaxBackoff = 10*time.Millisecond, 10*time.Millisecond
	c.RulesBackoff, c.RulesMaxBackoff = retry, most
	r := &fakeRules{t: t, fail: fail, called: make(chan struct{}, 1000)}
	c.Rules = r.refresh
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = c.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return srv, schemas, r
}

func TestRulesRefreshAfterWelcomeAndOnSync(t *testing.T) {
	srv, schemas, r := rulesClient(t, 10*time.Millisecond, 10*time.Millisecond, func(n int) error {
		if n == 2 {
			return errors.New("rules: HTTP 500")
		}
		return nil
	})
	p := srv.accept(t)
	p.next(false)
	p.send(schemas, `{"type":"welcome","protocol":1}`)
	r.wait("after welcome")
	p.next(true)

	p.send(schemas, `{"type":"command","id":"c1","command":"sync","args":{}}`)
	r.wait("on sync")
	if f := p.next(true); f.Type != "result" || f.Of != "c1" || !f.OK || f.Error != "" {
		t.Fatalf("sync result %+v", f)
	}

	p.send(schemas, `{"type":"command","id":"c2","command":"sync","args":{}}`)
	r.wait("on the second sync")
	if f := p.next(true); f.Type != "result" || f.Of != "c2" || f.OK || !strings.Contains(f.Error, "rules: HTTP 500") {
		t.Fatalf("failed sync result %+v", f)
	}
	r.wait("to retry the failed sync")
	p.quiet(100 * time.Millisecond)
	if n := r.calls(); n != 4 {
		t.Fatalf("%d refreshes, want 4: the retry did not stop once it succeeded", n)
	}

	_ = p.conn.Close(websocket.StatusInternalError, "restart")
	p = srv.accept(t)
	p.next(false)
	p.send(schemas, `{"type":"welcome","protocol":1}`)
	r.wait("after the second welcome")
}

func TestAFailedRefreshRetriesWithBackoff(t *testing.T) {
	srv, schemas, r := rulesClient(t, 20*time.Millisecond, 50*time.Millisecond, func(n int) error {
		if n < 4 {
			return errors.New("hubd unreachable")
		}
		return nil
	})
	p := srv.accept(t)
	p.next(false)
	p.send(schemas, `{"type":"welcome","protocol":1}`)
	for i := range 5 {
		r.wait(fmt.Sprintf("for attempt %d", i+1))
	}
	time.Sleep(150 * time.Millisecond)
	r.mu.Lock()
	at := slices.Clone(r.at)
	r.mu.Unlock()
	if len(at) != 5 {
		t.Fatalf("%d refreshes, want 4 failures and 1 success", len(at))
	}
	for i, least := range []time.Duration{20, 40, 50, 50} {
		if gap := at[i+1].Sub(at[i]); gap < least*time.Millisecond {
			t.Fatalf("retry %d came after %s, want at least %dms", i+1, gap, least)
		}
	}
}

func TestTheRetriesStopWithTheConnection(t *testing.T) {
	srv, schemas, r := rulesClient(t, 10*time.Millisecond, 10*time.Millisecond, func(int) error { return errors.New("hubd unreachable") })
	p := srv.accept(t)
	p.next(false)
	p.send(schemas, `{"type":"welcome","protocol":1}`)
	r.wait("after welcome")
	r.wait("on the first retry")
	r.mu.Lock()
	first := r.ctxs[0]
	r.mu.Unlock()

	_ = p.conn.Close(websocket.StatusInternalError, "restart")
	select {
	case <-first.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the refresh context outlived the connection")
	}
	p = srv.accept(t)
	p.next(false)
	before := r.calls()
	time.Sleep(100 * time.Millisecond)
	if n := r.calls(); n > before+1 {
		t.Fatalf("%d refreshes after the connection closed", n-before)
	}

	p.send(schemas, `{"type":"welcome","protocol":1}`)
	for r.calls() <= before+1 {
		r.wait("after the second welcome")
	}
	r.mu.Lock()
	last := r.ctxs[len(r.ctxs)-1]
	r.mu.Unlock()
	if last == first || last.Err() != nil {
		t.Fatal("the new connection did not get its own refresh context")
	}
}

func TestTheRulesBackoffStartsAt5sAndCapsAt5m(t *testing.T) {
	h, err := home.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b := New("", "ws", "test", "bundled", h, nil, nil).rulesBackoff()
	var got []time.Duration
	for range 9 {
		got = append(got, b.NextBackOff())
	}
	s := time.Second
	if want := []time.Duration{5 * s, 10 * s, 20 * s, 40 * s, 80 * s, 160 * s, 300 * s, 300 * s, 300 * s}; !slices.Equal(got, want) {
		t.Fatalf("backoff %v, want %v", got, want)
	}
}

func TestTheStatusFileFollowsTheConnection(t *testing.T) {
	schemas := schematest.Load(t)
	srv := &fakeServer{t: t, schemas: schemas, peers: make(chan *peer, 4)}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	h := tokenHome(t)
	c := New(ts.URL, "ws", "test", "bundled", h, func() ([]protocol.Summary, protocol.Live) { return nil, protocol.NewLive(nil, nil) }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.Ping = time.Hour
	c.MinBackoff, c.MaxBackoff = 10*time.Millisecond, 10*time.Millisecond
	c.Status = statusFile(t, ts.URL)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()

	p := srv.accept(t)
	p.next(false)
	start := wantStatus(t, c.Status, status.Offline, "starting")
	if s := readStatus(t, c.Status); s.Machine != "ws" || s.URL != ts.URL || s.Version != "test" {
		t.Fatalf("status names %+v", s)
	}

	p.send(schemas, `{"type":"welcome","protocol":1}`)
	p.next(true)
	up := wantStatus(t, c.Status, status.Connected, "")
	if up.Since.Before(start.Since) {
		t.Fatalf("connected since %s, before the start at %s", up.Since, start.Since)
	}

	_ = p.conn.Close(websocket.StatusInternalError, "restart")
	p = srv.accept(t)
	wantStatus(t, c.Status, status.Offline, "closed by hubd: restart")
	p.next(false)
	p.send(schemas, `{"type":"welcome","protocol":1}`)
	p.next(true)
	wantStatus(t, c.Status, status.Connected, "")

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned %v", err)
	}
	wantStatus(t, c.Status, status.Offline, "stopped")
	entries, _ := os.ReadDir(filepath.Dir(c.Status.Path))
	if len(entries) != 1 || entries[0].Name() != "status.json" {
		t.Fatalf("left behind next to the status file: %v", entries)
	}
}

func TestTheStatusFileIsNotRewrittenWhileTheStateHolds(t *testing.T) {
	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer ts.Close()
	h := tokenHome(t)
	c := New(ts.URL, "ws", "test", "bundled", h, func() ([]protocol.Summary, protocol.Live) { return nil, protocol.NewLive(nil, nil) }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.MinBackoff, c.MaxBackoff = time.Millisecond, 2*time.Millisecond
	c.Status = statusFile(t, ts.URL)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	tries := func(n int32) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); hits.Load() < n; time.Sleep(time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("%d tries, want %d", hits.Load(), n)
			}
		}
	}

	tries(3)
	wantStatus(t, c.Status, status.Offline, "HTTP 502")
	if err := os.WriteFile(c.Status.Path, []byte("sentinel"), 0o644); err != nil {
		t.Fatal(err)
	}
	tries(8)
	if b, _ := os.ReadFile(c.Status.Path); string(b) != "sentinel" {
		t.Fatalf("rewritten while the state held: %s", b)
	}
	cancel()
	<-done
	wantStatus(t, c.Status, status.Offline, "stopped")
}

func TestTheReasonIsShortAndCarriesNoURL(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	closed := strings.Replace(ts.URL, "http://", "http://op:hunter2@", 1)
	ts.Close()
	h := tokenHome(t)
	c := New(closed, "ws", "test", "bundled", h, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	_, dial := c.connect(context.Background())

	empty, _ := home.Open(t.TempDir())
	_, token := New(closed, "ws", "test", "bundled", empty, nil, nil).connect(context.Background())

	for err, want := range map[error]string{
		dial:  "connection refused",
		token: "token file: no such file or directory",
		fmt.Errorf("dial: %w", context.DeadlineExceeded):                                    "timed out",
		fmt.Errorf("failed to get reader: %w", io.EOF):                                      "connection lost",
		closeErr(websocket.CloseError{Code: websocket.StatusInternalError, Reason: "boom"}): "closed by hubd: boom",
		closeErr(websocket.CloseError{Code: websocket.StatusGoingAway}):                     "closed by hubd with code 1001",
		closeErr(websocket.CloseError{Code: CloseRefused, Reason: "retired"}):               "refused: retired",
		&RefusedError{}: "refused",
		&RefusedError{Reason: "too old", Min: 2, Max: 2}: "refused: protocol 1 too old",
		&RevokedError{Reason: "token revoked"}:           "token revoked",
		errRotated:                                       "token rotated",
	} {
		if got := reason(err); got != want {
			t.Errorf("reason(%v) = %q, want %q", err, got, want)
		}
		if strings.Contains(reason(err), "hunter2") || strings.Contains(reason(err), "127.0.0.1") {
			t.Errorf("reason(%v) carries the URL", err)
		}
	}
}
