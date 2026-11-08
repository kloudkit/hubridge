package sessions

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
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
	dirty := map[string]bool{}
	addTree := func(root string, fresh bool) {
		filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			switch {
			case err != nil:
			case d.IsDir():
				if err := w.Add(p); err != nil {
					t.Log.Warn("watch", "path", p, "err", err)
				}
			case fresh && strings.HasSuffix(p, ".jsonl"):
				dirty[p] = true
			}
			return nil
		})
	}
	addTree(t.Root, false)
	regDir, regWatched := "", false
	watchRegistry := func() {
		if regWatched || !t.Registry.Available() {
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
	inFlight := false
	var lastReconcile time.Time
	reconcile := func() {
		if t.Registry.Claude == "" || inFlight {
			return
		}
		inFlight, lastReconcile = true, time.Now()
		go func() { reconciled <- t.Registry.Reconcile(ctx) }()
	}
	reconcile()
	var reconcileC <-chan time.Time
	if iv.Reconcile > 0 {
		tick := time.NewTicker(iv.Reconcile)
		defer tick.Stop()
		reconcileC = tick.C
	}
	t.Emit(sink, t.Scan())

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
					addTree(ev.Name, true)
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
			if err != nil {
				t.Log.Warn("claude agents --json", "err", err)
			}
			t.EmitLive(sink)
		case <-reconcileC:
			reconcile()
		case <-liveness.C:
			t.EmitLive(sink)
		case <-rescan.C:
			addTree(t.Root, false)
			watchRegistry()
			t.Emit(sink, t.Scan())
		}
	}
}
