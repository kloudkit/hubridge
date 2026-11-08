package usage

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kloudkit/hubridge/internal/home"
	"github.com/kloudkit/hubridge/internal/protocol"
)

var now = time.Date(2026, 11, 5, 18, 0, 0, 0, time.UTC)

const event = `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1794117600,"rateLimitType":"seven_day","unifiedWindows":{"seven_day":{"utilization":0.05,"resetsAt":1794117600,"status":"allowed"}}}}`

func TestStatusLineGivesFractions(t *testing.T) {
	r, ok := FromStatusLine([]byte(`{"rate_limits":{"five_hour":{"used_percentage":23.5,"resets_at":1738425600},"seven_day":{"used_percentage":41.2,"resets_at":1738857600}}}`), now)
	if !ok || r.Source != "statusline" || r.FiveHour.Utilization != 0.235 || r.SevenDay.ResetsAt != "2025-02-06T16:00:00.000Z" {
		t.Fatalf("reading %+v five %+v seven %+v", r, r.FiveHour, r.SevenDay)
	}
	if _, ok := FromStatusLine([]byte(`{"model":{"id":"x"}}`), now); ok {
		t.Fatal("a status line with no rate_limits gave a reading")
	}
	if r, ok := FromStatusLine([]byte(`{"rate_limits":{"seven_day":{"used_percentage":5,"resets_at":1}}}`), now); !ok || r.FiveHour != nil {
		t.Fatalf("an absent window must stay absent: %+v", r)
	}
}

func TestStreamGivesTheRateLimitEvent(t *testing.T) {
	in := strings.Join([]string{`{"type":"system","subtype":"init"}`, "not json", event, `{"type":"result"}`}, "\n")
	r, ok := FromStream(strings.NewReader(in), now)
	if !ok || r.Source != "probe" || r.FiveHour != nil || r.SevenDay.Utilization != 0.05 || r.SevenDay.ResetsAt != "2026-11-08T06:00:00.000Z" {
		t.Fatalf("reading %+v seven %+v", r, r.SevenDay)
	}
	if _, ok := FromStream(strings.NewReader(`{"type":"result"}`), now); ok {
		t.Fatal("no event gave a reading")
	}
}

func TestSaveSkipsAnUnchangedRecentReading(t *testing.T) {
	h, _ := home.Open(t.TempDir())
	r := protocol.Reading{SevenDay: &protocol.Window{Utilization: 0.5, ResetsAt: "2026-11-08T09:20:00.000Z"}, Source: "statusline"}
	save := func(after time.Duration) protocol.Reading {
		t.Helper()
		r.At = protocol.Time(now.Add(after))
		if err := Save(h, r, now.Add(after)); err != nil {
			t.Fatal(err)
		}
		got, _ := Load(h)
		return got
	}
	if got := save(0); got.At != protocol.Time(now) {
		t.Fatalf("first save %+v", got)
	}
	if got := save(10 * time.Second); got.At != protocol.Time(now) {
		t.Fatal("an unchanged reading 10 s later was written again")
	}
	if got := save(2 * time.Minute); got.At != r.At {
		t.Fatal("a reading older than a minute was not refreshed")
	}
	r.SevenDay.Utilization = 0.6
	if got := save(2*time.Minute + time.Second); got.SevenDay.Utilization != 0.6 {
		t.Fatalf("a changed reading was not written: %+v", got)
	}
	if st, _ := os.Stat(h.Path(File)); st.Mode().Perm() != 0o600 {
		t.Fatalf("usage.json is %o", st.Mode().Perm())
	}
}

func TestProbeRunsClaudeAndReadsItsStream(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "claude")
	os.WriteFile(fake, []byte("#!/bin/sh\necho \"$*\" > \"$(dirname \"$0\")/args\"\necho '"+event+"'\n"), 0o700)
	r, err := Probe(context.Background(), fake, dir)
	if err != nil || r.SevenDay == nil {
		t.Fatalf("probe %+v %v", r, err)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	for _, want := range []string{"--no-session-persistence", "--setting-sources project", "--strict-mcp-config", "--model haiku"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("probe args %q miss %q", args, want)
		}
	}
	os.WriteFile(fake, []byte("#!/bin/sh\necho '{\"type\":\"result\"}'\n"), 0o700)
	if _, err := Probe(context.Background(), fake, dir); err != ErrNoReading {
		t.Fatalf("no event gave %v", err)
	}
}

func TestWatchProbesOnlyWhileASessionIsLiveAndTheReadingIsStale(t *testing.T) {
	h, _ := home.Open(t.TempDir())
	var mu sync.Mutex
	var sent []protocol.Reading
	probes := 0
	live := false
	send := func(r protocol.Reading) { mu.Lock(); sent = append(sent, r); mu.Unlock() }
	isLive := func() bool { mu.Lock(); defer mu.Unlock(); return live }
	probe := func(context.Context) (protocol.Reading, error) {
		mu.Lock()
		probes++
		mu.Unlock()
		return protocol.Reading{SevenDay: &protocol.Window{Utilization: 0.1, ResetsAt: "2026-11-08T09:20:00.000Z"}, Source: "probe", At: protocol.Time(time.Now())}, nil
	}
	go Watch(t.Context(), h, send, isLive, probe, slog.New(slog.NewTextHandler(io.Discard, nil)), Intervals{Check: 5 * time.Millisecond, Probe: 5 * time.Millisecond, Stale: time.Hour})
	time.Sleep(40 * time.Millisecond)
	mu.Lock()
	if probes != 0 {
		t.Fatalf("probed %d times with no live session", probes)
	}
	live = true
	mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(sent)
		mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a live session never got a probed reading")
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(40 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if probes != 1 {
		t.Fatalf("want one probe, then a fresh reading stops more; got %d", probes)
	}
	if len(sent) != 1 || sent[0].Source != "probe" {
		t.Fatalf("sent %+v", sent)
	}
}
