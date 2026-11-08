package usage

import (
	"context"
	"log/slog"
	"time"

	"github.com/kloudkit/hubridge/internal/home"
	"github.com/kloudkit/hubridge/internal/protocol"
)

type Intervals struct{ Check, Probe, Stale time.Duration }

var DefaultIntervals = Intervals{Check: 30 * time.Second, Probe: 5 * time.Minute, Stale: 15 * time.Minute}

type Prober func(context.Context) (protocol.Reading, error)

func Watch(ctx context.Context, h *home.Home, send func(protocol.Reading), live func() bool, probe Prober, log *slog.Logger, iv Intervals, now func() time.Time) {
	last := ""
	push := func() {
		if r, ok := Load(h); ok && r.At != last {
			last = r.At
			send(r)
		}
	}
	push()
	check := time.NewTicker(iv.Check)
	defer check.Stop()
	tick := time.NewTicker(iv.Probe)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-check.C:
			push()
		case <-tick.C:
			if !live() {
				continue
			}
			if r, ok := Load(h); ok && Age(r, now()) < iv.Stale {
				continue
			}
			r, err := probe(ctx)
			if err != nil {
				log.Warn("usage probe", "err", err)
				continue
			}
			if _, err := Save(h, r, now()); err != nil {
				log.Warn("save usage", "err", err)
				continue
			}
			push()
		}
	}
}
