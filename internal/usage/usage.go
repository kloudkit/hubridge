package usage

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"reflect"
	"time"

	"github.com/tidwall/gjson"

	"github.com/kloudkit/hubridge/internal/home"
	"github.com/kloudkit/hubridge/internal/protocol"
)

const (
	File         = "usage.json"
	refresh      = time.Minute
	probeTimeout = 60 * time.Second
)

var probeArgs = []string{"-p", "reply ok", "--output-format", "stream-json", "--verbose", "--model", "haiku",
	"--no-session-persistence", "--setting-sources", "project", "--strict-mcp-config"}

func window(w gjson.Result, value string, scale float64, resets string) *protocol.Window {
	v, r := w.Get(value), w.Get(resets)
	if !v.Exists() || v.Type != gjson.Number || !r.Exists() || r.Type != gjson.Number {
		return nil
	}
	return &protocol.Window{Utilization: v.Float() / scale, ResetsAt: protocol.Time(time.Unix(r.Int(), 0))}
}

func FromStatusLine(raw []byte, now time.Time) (protocol.Reading, bool) {
	rl := gjson.GetBytes(raw, "rate_limits")
	if !rl.IsObject() {
		return protocol.Reading{}, false
	}
	r := protocol.Reading{
		FiveHour: window(rl.Get("five_hour"), "used_percentage", 100, "resets_at"),
		SevenDay: window(rl.Get("seven_day"), "used_percentage", 100, "resets_at"),
		Source:   "statusline", At: protocol.Time(now),
	}
	return r, r.FiveHour != nil || r.SevenDay != nil
}

func FromStream(r io.Reader, now time.Time) (protocol.Reading, bool) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		ev := gjson.ParseBytes(sc.Bytes())
		if ev.Get("type").String() != "rate_limit_event" {
			continue
		}
		info := ev.Get("rate_limit_info")
		out := protocol.Reading{Source: "probe", At: protocol.Time(now)}
		if w := info.Get("unifiedWindows"); w.IsObject() {
			out.FiveHour = window(w.Get("five_hour"), "utilization", 1, "resetsAt")
			out.SevenDay = window(w.Get("seven_day"), "utilization", 1, "resetsAt")
		}
		if out.FiveHour == nil && out.SevenDay == nil {
			switch info.Get("rateLimitType").String() {
			case "five_hour":
				out.FiveHour = window(info, "utilization", 1, "resetsAt")
			case "seven_day":
				out.SevenDay = window(info, "utilization", 1, "resetsAt")
			}
		}
		if out.FiveHour != nil || out.SevenDay != nil {
			return out, true
		}
	}
	return protocol.Reading{}, false
}

func Load(h *home.Home) (protocol.Reading, bool) {
	b, err := os.ReadFile(h.Path(File))
	if err != nil {
		return protocol.Reading{}, false
	}
	var r protocol.Reading
	if json.Unmarshal(b, &r) != nil || r.At == "" {
		return protocol.Reading{}, false
	}
	return r, true
}

func Age(r protocol.Reading, now time.Time) time.Duration {
	at, err := time.Parse(time.RFC3339Nano, r.At)
	if err != nil {
		return math.MaxInt64
	}
	return now.Sub(at)
}

func Save(h *home.Home, r protocol.Reading, now time.Time) (bool, error) {
	if old, ok := Load(h); ok && reflect.DeepEqual(old.FiveHour, r.FiveHour) && reflect.DeepEqual(old.SevenDay, r.SevenDay) && Age(old, now) < refresh {
		return false, nil
	}
	b, err := json.Marshal(r)
	if err != nil {
		return false, err
	}
	return true, home.WriteFile(h.Path(File), b)
}

var ErrNoReading = errors.New("the probe printed no rate_limit_event")

func Probe(ctx context.Context, claude, dir string, now func() time.Time) (protocol.Reading, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, claude, probeArgs...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return protocol.Reading{}, err
	}
	if r, ok := FromStream(&out, now()); ok {
		return r, nil
	}
	return protocol.Reading{}, ErrNoReading
}
