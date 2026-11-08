package doctor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/kloudkit/hubridge/internal/formats"
	"github.com/kloudkit/hubridge/internal/home"
	"github.com/kloudkit/hubridge/internal/registry"
	"github.com/kloudkit/hubridge/internal/sessions"
	"github.com/kloudkit/hubridge/internal/transcript"
)

type Level int

const (
	OK Level = iota
	Info
	Drift
	Fail
)

var labels = map[Level]string{OK: "ok", Info: "info", Drift: "drift", Fail: "fail"}

type Line struct {
	Level Level
	Area  string
	Text  string
}

type Report struct{ Lines []Line }

func (r *Report) add(l Level, area, format string, args ...any) {
	r.Lines = append(r.Lines, Line{Level: l, Area: area, Text: fmt.Sprintf(format, args...)})
}

func (r *Report) Worst() Level {
	worst := OK
	for _, l := range r.Lines {
		worst = max(worst, l.Level)
	}
	return worst
}

func (r *Report) Write(w io.Writer) {
	for _, l := range r.Lines {
		fmt.Fprintf(w, "%-5s  %-11s  %s\n", labels[l.Level], l.Area, l.Text)
	}
}

type Options struct {
	Projects string
	Registry *registry.Registry
	Days     int
	Now      func() time.Time
	Log      *slog.Logger
}

func Run(ctx context.Context, o Options) *Report {
	r := &Report{}
	r.add(Info, "pinned", "formats pinned against Claude Code %s", formats.SeenIn)
	checkRegistry(r, o.Registry)
	agents := checkAgents(ctx, r, o.Registry)
	start := time.Now()
	checkTranscripts(r, o.Projects, o.Now().Add(-time.Duration(o.Days)*24*time.Hour), o.Days)
	r.add(Info, "timing", "reading %d days of transcripts took %s", o.Days, time.Since(start).Round(100*time.Millisecond))
	if agents != nil {
		start = time.Now()
		checkStates(r, o, agents)
		r.add(Info, "timing", "the daemon's own start-up scan of 30 days took %s", time.Since(start).Round(100*time.Millisecond))
	}
	return r
}

func checkRegistry(r *Report, reg *registry.Registry) {
	if !reg.Available() {
		r.add(Fail, "registry", "%s is missing: live state needs Claude Code 2.1.145 or later, under the same CLAUDE_CONFIG_DIR", reg.Dir)
		return
	}
	files, errs := reg.Files()
	live := 0
	for _, stem := range slices.Sorted(maps.Keys(files)) {
		f := files[stem]
		if reg.Alive(stem, f) {
			live++
		}
		raw, err := os.ReadFile(filepath.Join(reg.Dir, stem+".json"))
		if err != nil {
			continue
		}
		for _, field := range formats.RegistryFields {
			if !gjson.GetBytes(raw, field).Exists() {
				r.add(Drift, "registry", "%s.json has no %q", stem, field)
			}
		}
		if f.Status != "" && !formats.RegistryStatuses[f.Status] {
			r.add(Drift, "registry", "%s.json has status %q, not in the pinned list", stem, f.Status)
		}
	}
	for _, stem := range slices.Sorted(maps.Keys(errs)) {
		r.add(Info, "registry", "%s.json did not parse (%v); it is read again on its next change", stem, errs[stem])
	}
	if reg.Domain == "" {
		r.add(Info, "registry", "this host's pid namespace is unknown, so pidDomain is not compared")
	}
	r.add(OK, "registry", "%d files, %d of them live, %d left by sessions that ended or crashed", len(files), live, len(files)-live)
}

func checkAgents(ctx context.Context, r *Report, reg *registry.Registry) []map[string]any {
	if reg.Claude == "" {
		r.add(Drift, "agents", "claude is not on PATH: no waiting reasons and no background sessions")
		return nil
	}
	start := time.Now()
	raw, err := reg.List(ctx)
	if err != nil {
		r.add(Fail, "agents", "claude agents --json --all: %v", err)
		return nil
	}
	took := time.Since(start)
	var list []map[string]any
	if err := json.Unmarshal(raw, &list); err != nil {
		r.add(Fail, "agents", "claude agents --json --all is not a JSON array: %v", err)
		return nil
	}
	var typed []registry.Agent
	json.Unmarshal(raw, &typed)
	reg.Use(typed)
	kinds := map[string]int{}
	for i, a := range list {
		for _, field := range formats.AgentFields {
			if _, ok := a[field]; !ok {
				r.add(Drift, "agents", "entry %d has no %q", i, field)
			}
		}
		kind, _ := a["kind"].(string)
		kinds[kind]++
		if s, ok := a["status"].(string); ok && !formats.AgentStatuses[s] {
			r.add(Drift, "agents", "status %q is not in the pinned list", s)
		}
		if s, ok := a["state"].(string); ok && !formats.JobStates[s] {
			r.add(Drift, "agents", "background state %q is not in the pinned list", s)
		}
		if s, ok := a["waitingFor"].(string); ok && !formats.WaitingFor[s] {
			r.add(Drift, "agents", "waitingFor %q is not in the pinned list", s)
		}
	}
	r.add(OK, "agents", "claude agents --json --all lists %d interactive and %d background sessions, in %s", kinds[registry.Interactive], kinds[registry.Background], took.Round(10*time.Millisecond))
	return list
}

func checkTranscripts(r *Report, projects string, since time.Time, days int) {
	types, subtypes := map[string]int{}, map[string]int{}
	usage, ledger := map[string]int{}, map[string]int{}
	usageLines, ledgerLines, broken, files, lines := 0, 0, 0, 0, 0
	filepath.WalkDir(projects, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".jsonl") || filepath.Base(p) == "journal.jsonl" {
			return nil
		}
		if info, err := d.Info(); err != nil || info.ModTime().Before(since) {
			return nil
		}
		fh, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer fh.Close()
		files++
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 {
				continue
			}
			lines++
			if !gjson.ValidBytes(line) {
				broken++
				continue
			}
			t := gjson.GetBytes(line, "type").String()
			types[t]++
			switch t {
			case "system":
				subtypes[gjson.GetBytes(line, "subtype").String()]++
			case "assistant":
				u := gjson.GetBytes(line, "message.usage")
				if !u.Exists() || gjson.GetBytes(line, "message.model").String() == transcript.Synthetic {
					break
				}
				usageLines++
				for _, f := range formats.UsageFields {
					if u.Get(f).Exists() {
						usage[f]++
					}
				}
			case "cost-state":
				ledgerLines++
				for _, f := range formats.LedgerFields {
					if gjson.GetBytes(line, f).Exists() {
						ledger[f]++
					}
				}
			}
		}
		return nil
	})
	for _, t := range slices.Sorted(maps.Keys(types)) {
		if !formats.EntryTypes[t] {
			r.add(Drift, "transcripts", "entry type %q appears %d times and is not in the pinned list", t, types[t])
		}
	}
	for _, s := range slices.Sorted(maps.Keys(subtypes)) {
		if !formats.SystemSubtypes[s] {
			r.add(Drift, "transcripts", "system subtype %q appears %d times and is not in the pinned list", s, subtypes[s])
		}
	}
	for _, t := range slices.Sorted(maps.Keys(transcript.ReadTypes)) {
		if types[t] == 0 {
			r.add(Info, "transcripts", "no %q entry in the last %d days", t, days)
		}
	}
	if usageLines > 0 {
		for _, f := range formats.UsageFields {
			if usage[f] == 0 {
				r.add(Drift, "transcripts", "no reply's usage carries %s", f)
			}
		}
	}
	if ledgerLines > 0 {
		for _, f := range formats.LedgerFields {
			if ledger[f] == 0 {
				r.add(Drift, "transcripts", "no cost-state line carries %s", f)
			}
		}
	}
	if broken > 0 {
		r.add(Info, "transcripts", "%d lines are not JSON, usually a line still being written", broken)
	}
	r.add(OK, "transcripts", "%d files and %d lines from the last %d days read; %d replies with usage, %d cost-state lines", files, lines, days, usageLines, ledgerLines)
}

var wantState = map[string]string{"busy": "working", "waiting": "waiting", "idle": "waiting"}

func checkStates(r *Report, o Options, agents []map[string]any) {
	tr := sessions.New(o.Projects, func() map[string]home.Handover { return nil }, o.Log)
	tr.Registry = o.Registry
	tr.Now = o.Now
	tr.Scan()
	sums, _ := tr.Snapshot()
	by := map[string]string{}
	status := map[string]string{}
	for _, s := range sums {
		by[s.ID], status[s.ID] = s.State, s.Status
	}
	matched, missing := 0, 0
	for _, a := range agents {
		id, _ := a["sessionId"].(string)
		st, _ := a["status"].(string)
		if _, running := a["pid"]; !running || id == "" || st == "" {
			continue
		}
		state, ok := by[id]
		if !ok {
			missing++
			continue
		}
		norm, _ := registry.Normalize(st)
		switch {
		case status[id] == norm && state == wantState[norm]:
			matched++
		case status[id] != norm:
			r.add(Info, "states", "session %s changed while being checked: %s in the listing, %s in its file", id, st, status[id])
		default:
			r.add(Fail, "states", "session %s: the daemon says %s (%s), claude agents --json says %s", id, state, status[id], st)
		}
	}
	if missing > 0 {
		r.add(Info, "states", "%d running sessions have no transcript yet, or are headless", missing)
	}
	r.add(OK, "states", "%d running sessions have the state claude agents --json gives them", matched)
}
