package sessions

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/kloudkit/hubridge/internal/registry"
)

type Intervals struct {
	Debounce  time.Duration
	Liveness  time.Duration
	Rescan    time.Duration
	Reconcile time.Duration
	Reason    time.Duration
}

var DefaultIntervals = Intervals{Debounce: 300 * time.Millisecond, Liveness: 5 * time.Second, Rescan: time.Minute, Reconcile: time.Minute, Reason: 5 * time.Second}

func (t *Tracker) Watch(ctx context.Context, sink Sink, iv Intervals) error {
	if err := os.MkdirAll(t.Root, 0o700); err != nil {
		return err
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()
	addTree := func(root string) {
		filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				if err := w.Add(p); err != nil {
					t.Log.Warn("watch", "path", p, "err", err)
				}
			}
			return nil
		})
	}
	addTree(t.Root)
	regDir, regWatched := "", false
	watchRegistry := func() {
		if t.Registry == nil || regWatched || !t.Registry.Available() {
			return
		}
		if err := w.Add(t.Registry.Dir); err != nil {
			t.Log.Warn("watch", "path", t.Registry.Dir, "err", err)
			return
		}
		regDir, regWatched = filepath.Clean(t.Registry.Dir), true
	}
	watchRegistry()

	reconciled := make(chan error, 1)
	inFlight, warned := false, false
	var lastReconcile time.Time
	reconcile := func() {
		if t.Registry == nil || inFlight {
			return
		}
		inFlight, lastReconcile = true, time.Now()
		go func() { reconciled <- t.Registry.Reconcile(ctx) }()
	}
	reconcile()
	var reconcileC <-chan time.Time
	if t.Registry != nil && iv.Reconcile > 0 {
		tick := time.NewTicker(iv.Reconcile)
		defer tick.Stop()
		reconcileC = tick.C
	}
	t.Emit(sink, t.Scan())

	dirty := map[string]bool{}
	regDirty := false
	pending := false
	debounce := time.NewTimer(time.Hour)
	debounce.Stop()
	liveness := time.NewTicker(iv.Liveness)
	defer liveness.Stop()
	rescan := time.NewTicker(iv.Rescan)
	defer rescan.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			if ev.Has(fsnotify.Create) {
				if st, err := os.Stat(ev.Name); err == nil && st.IsDir() {
					addTree(ev.Name)
					filepath.WalkDir(ev.Name, func(p string, d fs.DirEntry, err error) error {
						if err == nil && !d.IsDir() && strings.HasSuffix(p, ".jsonl") {
							dirty[p] = true
						}
						return nil
					})
				}
			}
			if strings.HasSuffix(ev.Name, ".jsonl") && (ev.Has(fsnotify.Create) || ev.Has(fsnotify.Write)) {
				dirty[ev.Name] = true
			}
			if regWatched && filepath.Dir(ev.Name) == regDir && strings.HasSuffix(ev.Name, ".json") {
				regDirty = true
			}
			if (len(dirty) > 0 || regDirty) && !pending {
				pending = true
				debounce.Reset(iv.Debounce)
			}
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			t.Log.Warn("watcher", "err", err)
		case <-debounce.C:
			var ids []string
			for p := range dirty {
				if id, ok := t.Touch(p); ok {
					ids = append(ids, id)
				}
			}
			dirty = map[string]bool{}
			pending = false
			t.Emit(sink, ids)
			if regDirty {
				regDirty = false
				t.EmitLive(sink)
				if t.NeedsReason() && time.Since(lastReconcile) >= iv.Reason {
					reconcile()
				}
			}
		case err := <-reconciled:
			inFlight = false
			if err != nil && (!errors.Is(err, registry.ErrNoClaude) || !warned) {
				warned = warned || errors.Is(err, registry.ErrNoClaude)
				t.Log.Warn("claude agents --json", "err", err)
			}
			t.EmitLive(sink)
		case <-reconcileC:
			reconcile()
		case <-liveness.C:
			if t.Registry != nil {
				t.EmitLive(sink)
			} else {
				t.EmitAll(sink)
			}
		case <-rescan.C:
			addTree(t.Root)
			watchRegistry()
			t.Emit(sink, t.Scan())
		}
	}
}
