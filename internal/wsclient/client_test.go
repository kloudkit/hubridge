package wsclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/kloudkit/hubridge/internal/home"
	"github.com/kloudkit/hubridge/internal/protocol"
	"github.com/kloudkit/hubridge/internal/schematest"
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
	p.send(schemas, `{"type":"command","id":"c2","command":"sync","args":{}}`)
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
		select {
		case <-srv.peers:
			t.Fatalf("%s: daemon reconnected after 4003", reason)
		case <-time.After(50 * time.Millisecond):
		}
		ts.Close()
	}
}

func TestUpgradeStatus(t *testing.T) {
	for status, revoked := range map[int]bool{http.StatusUnauthorized: true, http.StatusServiceUnavailable: false, http.StatusForbidden: false} {
		hits := 0
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits++
			w.WriteHeader(status)
		}))
		h, _ := home.Open(t.TempDir())
		h.WriteToken(oldToken)
		c := New(ts.URL, "ws", "test", "bundled", h, func() ([]protocol.Summary, protocol.Live) { return nil, protocol.NewLive(nil, nil) }, slog.New(slog.NewTextHandler(io.Discard, nil)))
		c.MinBackoff, c.MaxBackoff = time.Millisecond, 2*time.Millisecond
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		err := c.Run(ctx)
		cancel()
		ts.Close()
		if revoked {
			if !errors.Is(err, ErrRevoked) || err.Error() != "token rejected: rerun the installer" || hits != 1 {
				t.Fatalf("401: err %v after %d tries", err, hits)
			}
		} else if err != nil || hits < 2 {
			t.Fatalf("%d: want retries with backoff, got err %v after %d tries", status, err, hits)
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
