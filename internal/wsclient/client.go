package wsclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v5"
	"github.com/coder/websocket"

	"github.com/kloudkit/hubridge/internal/home"
	"github.com/kloudkit/hubridge/internal/protocol"
)

const (
	CloseRefused websocket.StatusCode = 4000
	CloseRevoked websocket.StatusCode = 4003
)

var (
	ErrRefused = errors.New("hubd refused this daemon")
	ErrRevoked = errors.New("hubd revoked this daemon")
)

type RevokedError struct{ Reason string }

func (e *RevokedError) Error() string {
	if strings.Contains(e.Reason, "machine") {
		return "machine removed: rerun the installer"
	}
	if e.Reason == "rejected" {
		return "token rejected: rerun the installer"
	}
	return "token revoked: rerun the installer"
}

func (e *RevokedError) Unwrap() error { return ErrRevoked }

type Snapshot func() ([]protocol.Summary, protocol.Live)

type Client struct {
	URL      string
	Machine  string
	Version  string
	Channel  string
	Home     *home.Home
	Queue    *home.Queue
	Snapshot Snapshot
	Rules    func(context.Context) error
	Log      *slog.Logger

	Ping            time.Duration
	MinBackoff      time.Duration
	MaxBackoff      time.Duration
	RulesBackoff    time.Duration
	RulesMaxBackoff time.Duration

	mu     sync.Mutex
	live   *protocol.Live
	usage  *protocol.Reading
	notify chan struct{}
}

func New(url, machine, version, channel string, h *home.Home, snap Snapshot, log *slog.Logger) *Client {
	return &Client{
		URL: url, Machine: machine, Version: version, Channel: channel, Home: h, Queue: h.Queue(), Snapshot: snap, Log: log,
		Ping: 30 * time.Second, MinBackoff: time.Second, MaxBackoff: time.Minute,
		RulesBackoff: 5 * time.Second, RulesMaxBackoff: 5 * time.Minute,
		notify: make(chan struct{}, 1),
	}
}

func (c *Client) wake() {
	select {
	case c.notify <- struct{}{}:
	default:
	}
}

func (c *Client) Session(s protocol.Summary) {
	id := c.Queue.NewID(s.ID)
	b, err := json.Marshal(protocol.NewSession(id, s))
	if err == nil {
		err = c.Queue.Put(id, b)
	}
	if err != nil {
		c.Log.Error("queue session", "session", s.ID, "err", err)
		return
	}
	c.wake()
}

func (c *Client) Live(l protocol.Live) {
	c.mu.Lock()
	c.live = &l
	c.mu.Unlock()
	c.wake()
}

func (c *Client) Usage(r protocol.Reading) {
	c.mu.Lock()
	c.usage = &r
	c.mu.Unlock()
	c.wake()
}

func (c *Client) Run(ctx context.Context) error {
	wait := &backoff.ExponentialBackOff{InitialInterval: c.MinBackoff, RandomizationFactor: 0.5, Multiplier: 2, MaxInterval: c.MaxBackoff}
	wait.Reset()
	for {
		welcomed, err := c.connect(ctx)
		if errors.Is(err, ErrRefused) || errors.Is(err, ErrRevoked) {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
		if welcomed {
			wait.Reset()
		}
		if err == nil {
			continue
		}
		next := wait.NextBackOff()
		c.Log.Warn("hubd connection", "err", err, "retry", next)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(next):
		}
	}
}

var errRotated = errors.New("token rotated")

func (c *Client) connect(ctx context.Context) (bool, error) {
	token, err := c.Home.Token()
	if err != nil {
		return false, err
	}
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	conn, resp, err := websocket.Dial(dctx, c.URL+"/api/client/ws", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	cancel()
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return false, &RevokedError{Reason: "rejected"}
		}
		return false, err
	}
	conn.SetReadLimit(1 << 20)
	defer conn.CloseNow()

	if err := c.write(ctx, conn, protocol.Hello{Type: "hello", Machine: c.Machine, Protocol: protocol.Version, Version: c.Version, Channel: c.Channel}); err != nil {
		return false, err
	}
	first, err := c.read(ctx, conn)
	if err != nil {
		return false, closeErr(err)
	}
	switch first.Type {
	case "welcome":
	case "refused":
		c.refused(first)
		return false, ErrRefused
	default:
		return false, fmt.Errorf("expected welcome, got %q", first.Type)
	}
	c.Log.Info("connected to hubd", "protocol", first.Protocol)

	sctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	var syncs chan chan error
	if c.Rules != nil {
		syncs = make(chan chan error)
		go c.refresh(sctx, syncs)
	}
	go c.reader(sctx, stop, conn, syncs)

	sums, live := c.Snapshot()
	for _, s := range sums {
		c.Session(s)
	}
	c.Live(live)

	sent := map[string]bool{}
	var sentLive *protocol.Live
	var sentUsage *protocol.Reading
	flush := func() error {
		for _, q := range c.Queue.Pending() {
			if sent[q.ID] {
				continue
			}
			if err := conn.Write(sctx, websocket.MessageText, q.Data); err != nil {
				return err
			}
			sent[q.ID] = true
		}
		c.mu.Lock()
		l, u := c.live, c.usage
		c.mu.Unlock()
		if l != nil && (sentLive == nil || !equalLive(*sentLive, *l)) {
			if err := c.write(sctx, conn, *l); err != nil {
				return err
			}
			sentLive = l
		}
		if u != nil && u != sentUsage {
			if err := c.write(sctx, conn, protocol.NewUsage(*u)); err != nil {
				return err
			}
			sentUsage = u
		}
		return nil
	}
	if err := flush(); err != nil {
		return true, err
	}
	ping := time.NewTicker(c.Ping)
	defer ping.Stop()
	for {
		select {
		case <-sctx.Done():
			cause := context.Cause(sctx)
			if errors.Is(cause, errRotated) {
				conn.Close(websocket.StatusNormalClosure, "token rotated")
				return true, nil
			}
			if ctx.Err() != nil {
				conn.Close(websocket.StatusGoingAway, "daemon stopping")
				return true, nil
			}
			return true, cause
		case <-ping.C:
			if err := c.write(sctx, conn, protocol.Ping{Type: "ping"}); err != nil {
				return true, err
			}
		case <-c.notify:
			if err := flush(); err != nil {
				return true, err
			}
		}
	}
}

func (c *Client) reader(ctx context.Context, stop context.CancelCauseFunc, conn *websocket.Conn, syncs chan<- chan error) {
	for {
		m, err := c.read(ctx, conn)
		if err != nil {
			stop(closeErr(err))
			return
		}
		switch m.Type {
		case "ack":
			c.Queue.Ack(m.Of)
		case "error":
			c.Log.Error("hubd rejected a message", "error", m.Error)
		case "refused":
			c.refused(m)
			stop(ErrRefused)
			return
		case "command":
			if c.command(ctx, conn, m, syncs) {
				stop(errRotated)
				return
			}
		default:
			c.Log.Warn("unknown message from hubd", "type", m.Type)
		}
	}
}

func (c *Client) refused(m protocol.Server) {
	c.Log.Error("hubd refused this daemon; it waits for an update", "reason", m.Reason, "minProtocol", m.MinProtocol, "maxProtocol", m.MaxProtocol)
}

func (c *Client) command(ctx context.Context, conn *websocket.Conn, m protocol.Server, syncs chan<- chan error) bool {
	res := protocol.Result{Type: "result", Of: m.ID, OK: true}
	rotated := false
	switch m.Command {
	case "rotate":
		var args struct {
			Token string `json:"token"`
		}
		if json.Unmarshal(m.Args, &args) != nil || !strings.HasPrefix(args.Token, "hubd_") {
			res.OK, res.Error = false, "rotate needs a hubd token in args.token"
		} else if err := c.Home.WriteToken(args.Token); err != nil {
			res.OK, res.Error = false, "write token: "+err.Error()
		} else {
			rotated = true
			c.Log.Info("token rotated; reconnecting")
		}
	case "sync":
		if syncs != nil {
			go c.sync(ctx, conn, res, syncs)
			return false
		}
		res.OK, res.Error = false, "unsupported command: sync"
	default:
		res.OK, res.Error = false, "unsupported command: "+m.Command
	}
	if err := c.write(ctx, conn, res); err != nil {
		c.Log.Warn("send result", "err", err)
	}
	return rotated
}

func (c *Client) rulesBackoff() *backoff.ExponentialBackOff {
	b := &backoff.ExponentialBackOff{InitialInterval: c.RulesBackoff, Multiplier: 2, MaxInterval: c.RulesMaxBackoff}
	b.Reset()
	return b
}

// refresh is the one goroutine per connection that calls Rules, so a retry and a sync never run at once.
func (c *Client) refresh(ctx context.Context, syncs <-chan chan error) {
	wait := c.rulesBackoff()
	var reply chan error
	for {
		err := c.Rules(ctx)
		if reply != nil {
			reply <- err
			reply = nil
		}
		var retry <-chan time.Time
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			next := wait.NextBackOff()
			c.Log.Warn("refresh the instructions", "err", err, "retry", next)
			retry = time.After(next)
		default:
			wait.Reset()
		}
		select {
		case <-ctx.Done():
			return
		case <-retry:
		case reply = <-syncs:
		}
	}
}

func (c *Client) sync(ctx context.Context, conn *websocket.Conn, res protocol.Result, syncs chan<- chan error) {
	reply := make(chan error, 1)
	select {
	case syncs <- reply:
	case <-ctx.Done():
		return
	}
	err := <-reply
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		res.OK, res.Error = false, "sync the instructions: "+err.Error()
	}
	if err := c.write(ctx, conn, res); err != nil {
		c.Log.Warn("send result", "err", err)
	}
}

func (c *Client) write(ctx context.Context, conn *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return conn.Write(wctx, websocket.MessageText, b)
}

func (c *Client) read(ctx context.Context, conn *websocket.Conn) (protocol.Server, error) {
	var m protocol.Server
	typ, b, err := conn.Read(ctx)
	if err != nil {
		return m, err
	}
	if typ != websocket.MessageText {
		return m, errors.New("binary frame from hubd")
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("bad frame from hubd: %w", err)
	}
	return m, nil
}

func closeErr(err error) error {
	switch websocket.CloseStatus(err) {
	case CloseRefused:
		return ErrRefused
	case CloseRevoked:
		ce, _ := errors.AsType[websocket.CloseError](err)
		return &RevokedError{Reason: ce.Reason}
	}
	return err
}

func equalLive(a, b protocol.Live) bool {
	return slices.Equal(a.Live, b.Live) && slices.Equal(a.Present, b.Present)
}
