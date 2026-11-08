package sessions

import (
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/kloudkit/hubridge/internal/home"
	"github.com/kloudkit/hubridge/internal/mask"
	"github.com/kloudkit/hubridge/internal/proc"
	"github.com/kloudkit/hubridge/internal/protocol"
	"github.com/kloudkit/hubridge/internal/registry"
	"github.com/kloudkit/hubridge/internal/transcript"
)

const (
	maxAge      = 30 * 24 * time.Hour
	ExcerptLen  = 200
	hoursWindow = 48 * time.Hour
)

type Sink interface {
	Session(protocol.Summary)
	Live(protocol.Live)
}

type Alive func(home.Handover) bool

type session struct {
	main *transcript.File
	subs map[string]*transcript.File
}

type owner struct {
	session string
	main    bool
}

type Tracker struct {
	Root            string
	Handovers       func() map[string]home.Handover
	Waiting         func() map[string]time.Time
	Clear           func(id string)
	Alive           Alive
	Registry        *registry.Registry
	Background      func() map[string]home.Background
	ClearBackground func(id string)
	Log             *slog.Logger
	Now             func() time.Time
	Estate          string

	mu       sync.Mutex
	owners   map[string]owner
	dirty    map[string]bool
	regs     map[string]registry.Session
	bgs      map[string]home.Background
	sessions map[string]*session
	sent     map[string]protocol.Summary
	errs     map[string]int
	lastLive *protocol.Live
	waits    map[string]time.Time
	roots    map[string]string
	topics   map[string]claimTopics
	remotes  map[string]string
}

func New(root string, handovers func() map[string]home.Handover, log *slog.Logger) *Tracker {
	return &Tracker{
		Root:            root,
		Handovers:       handovers,
		Alive:           func(h home.Handover) bool { return proc.Alive(h.PID, h.Started) },
		Log:             log,
		Now:             time.Now,
		Estate:          estateDir(),
		Waiting:         func() map[string]time.Time { return nil },
		Clear:           func(string) {},
		Background:      func() map[string]home.Background { return nil },
		ClearBackground: func(string) {},
		sessions:        map[string]*session{},
		owners:          map[string]owner{},
		dirty:           map[string]bool{},
		sent:            map[string]protocol.Summary{},
		errs:            map[string]int{},
		roots:           map[string]string{},
		remotes:         map[string]string{},
	}
}

func (t *Tracker) classify(path string) (id string, main bool, ok bool) {
	rel, err := filepath.Rel(t.Root, path)
	if err != nil || !strings.HasSuffix(rel, ".jsonl") {
		return "", false, false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	switch {
	case len(parts) == 2:
		return strings.TrimSuffix(parts[1], ".jsonl"), true, true
	case len(parts) >= 4 && parts[2] == "subagents" && parts[len(parts)-1] != "journal.jsonl":
		return parts[1], false, true
	}
	return "", false, false
}

func (t *Tracker) Touch(path string) (string, bool) {
	id, main, ok := t.classify(path)
	if !ok {
		return "", false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.sessions[id]
	if s == nil {
		s = &session{subs: map[string]*transcript.File{}}
		t.sessions[id] = s
	}
	var f *transcript.File
	if main {
		if s.main == nil {
			s.main = transcript.NewFile(path, true)
		}
		f = s.main
	} else {
		if s.subs[path] == nil {
			s.subs[path] = transcript.NewFile(path, false)
		}
		f = s.subs[path]
	}
	if _, err := f.Update(); err != nil && !os.IsNotExist(err) {
		t.Log.Warn("read transcript", "path", path, "err", err)
	}
	if n := len(f.Errors); n > t.errs[path] {
		for _, e := range f.Errors[t.errs[path]:] {
			t.Log.Error("transcript parse failure", "at", e)
		}
		t.errs[path] = n
	}
	t.claim(id, f)
	return id, true
}

func (t *Tracker) claim(id string, f *transcript.File) {
	for _, key := range f.Fresh() {
		cand := owner{session: id, main: f.Msgs[key].Main}
		cur, ok := t.owners[key]
		if ok && (cur == cand || !t.better(cand, cur)) {
			continue
		}
		t.owners[key] = cand
		if ok && cur.session != id {
			t.dirty[cur.session] = true
		}
	}
}

func (t *Tracker) better(cand, cur owner) bool {
	if cand.main != cur.main {
		return cand.main
	}
	if cand.session == cur.session {
		return false
	}
	cs, us := t.sessions[cand.session], t.sessions[cur.session]
	if cs != nil && cs.main != nil && cs.main.ContinuedIn == cur.session {
		return true
	}
	if us != nil && us.main != nil && us.main.ContinuedIn == cand.session {
		return false
	}
	cf, uf := firstOf(cs), firstOf(us)
	if !cf.Equal(uf) {
		return !cf.IsZero() && (uf.IsZero() || cf.Before(uf))
	}
	return cand.session < cur.session
}

func firstOf(s *session) time.Time {
	if s == nil || s.main == nil {
		return time.Time{}
	}
	return s.main.First
}

func (s *session) files() []*transcript.File {
	var files []*transcript.File
	if s.main != nil {
		files = append(files, s.main)
	}
	for _, p := range slices.Sorted(maps.Keys(s.subs)) {
		files = append(files, s.subs[p])
	}
	return files
}

func (t *Tracker) reown() {
	owners := map[string]owner{}
	for _, id := range slices.Sorted(maps.Keys(t.sessions)) {
		for _, f := range t.sessions[id].files() {
			f.Fresh()
			for key, m := range f.Msgs {
				cand := owner{session: id, main: m.Main}
				if cur, ok := owners[key]; !ok || t.better(cand, cur) {
					owners[key] = cand
				}
			}
		}
	}
	for key, o := range owners {
		if old, ok := t.owners[key]; ok && old.session != o.session {
			t.dirty[old.session] = true
			t.dirty[o.session] = true
		}
	}
	t.owners = owners
}

func (t *Tracker) owned(id string, s *session) []*transcript.Msg {
	var out []*transcript.Msg
	counted := map[string]bool{}
	for _, f := range s.files() {
		for key, m := range f.Msgs {
			if counted[key] {
				continue
			}
			if o, ok := t.owners[key]; ok && o.session != id {
				continue
			}
			counted[key] = true
			out = append(out, m)
		}
	}
	return out
}

func (t *Tracker) registrySessions() map[string]registry.Session {
	if t.Registry == nil || !t.Registry.Available() {
		return nil
	}
	return t.Registry.Sessions()
}

func liveIn(r registry.Session) bool {
	if r.Running() {
		return true
	}
	if r.Kind != registry.Background {
		return false
	}
	switch r.JobState {
	case "failed", "stopped", "":
		return false
	}
	return true
}

func (t *Tracker) Scan() []string {
	cutoff := time.Now().Add(-maxAge)
	seen := map[string]bool{}
	filepath.WalkDir(t.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		if info, err := d.Info(); err != nil || info.ModTime().Before(cutoff) {
			return nil
		}
		if id, ok := t.Touch(path); ok {
			seen[id] = true
		}
		return nil
	})
	t.mu.Lock()
	t.reown()
	t.mu.Unlock()
	return slices.Sorted(maps.Keys(seen))
}

func (t *Tracker) Emit(sink Sink, ids []string) {
	hos, waits, regs, bgs := t.Handovers(), t.Waiting(), t.registrySessions(), t.Background()
	t.mu.Lock()
	t.waits, t.regs, t.bgs = waits, regs, bgs
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	for id := range t.dirty {
		if !want[id] {
			want[id] = true
			ids = append(ids, id)
		}
	}
	t.dirty = map[string]bool{}
	var out []protocol.Summary
	for _, id := range ids {
		sum, ok := t.summary(id, hos)
		if !ok || reflect.DeepEqual(t.sent[id], sum) {
			continue
		}
		t.sent[id] = sum
		out = append(out, sum)
	}
	live := t.live(hos)
	changed := t.lastLive == nil || !reflect.DeepEqual(*t.lastLive, live)
	t.lastLive = &live
	t.mu.Unlock()
	for _, s := range out {
		sink.Session(s)
	}
	if changed {
		sink.Live(live)
	}
}

func (t *Tracker) EmitAll(sink Sink) {
	t.mu.Lock()
	ids := slices.Sorted(maps.Keys(t.sessions))
	t.mu.Unlock()
	t.Emit(sink, ids)
}

func (t *Tracker) EmitLive(sink Sink) {
	regs := t.registrySessions()
	t.mu.Lock()
	ids := map[string]bool{}
	add := func(id string) {
		if _, ok := t.sessions[id]; ok {
			ids[id] = true
		}
	}
	if t.lastLive != nil {
		for _, id := range t.lastLive.Live {
			add(id)
		}
	}
	for id := range regs {
		add(id)
	}
	t.mu.Unlock()
	t.Emit(sink, slices.Sorted(maps.Keys(ids)))
}

func (t *Tracker) NeedsReason() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, r := range t.regs {
		if r.Running() && r.Status == registry.Waiting && r.WaitingFor == "" {
			return true
		}
	}
	return false
}

func (t *Tracker) Snapshot() ([]protocol.Summary, protocol.Live) {
	hos, waits, regs, bgs := t.Handovers(), t.Waiting(), t.registrySessions(), t.Background()
	t.mu.Lock()
	t.waits, t.regs, t.bgs = waits, regs, bgs
	defer t.mu.Unlock()
	live := t.live(hos)
	var sums []protocol.Summary
	for _, id := range live.Live {
		if s, ok := t.summary(id, hos); ok {
			t.sent[id] = s
			sums = append(sums, s)
		}
	}
	t.lastLive = &live
	return sums, live
}

func (t *Tracker) AnyLive() bool {
	hos, regs := t.Handovers(), t.registrySessions()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.regs = regs
	return len(t.live(hos).Live) > 0
}

func (t *Tracker) live(hos map[string]home.Handover) protocol.Live {
	var live, present []string
	for id, s := range t.sessions {
		if s.main == nil || s.main.First.IsZero() || s.main.Headless {
			continue
		}
		if t.regs != nil {
			if r, ok := t.regs[id]; ok && liveIn(r) {
				live = append(live, id)
			}
		} else if ho, ok := hos[id]; ok && t.Alive(ho) {
			live = append(live, id)
		}
		if _, err := os.Stat(s.main.Path); err != nil {
			continue
		}
		if st, err := os.Stat(s.main.Folder); err == nil && st.IsDir() {
			present = append(present, id)
		}
	}
	slices.Sort(live)
	slices.Sort(present)
	return protocol.NewLive(live, present)
}

func (t *Tracker) summary(id string, hos map[string]home.Handover) (protocol.Summary, bool) {
	s := t.sessions[id]
	if s == nil || s.main == nil || s.main.First.IsZero() || s.main.Headless {
		return protocol.Summary{}, false
	}
	m := s.main
	files := s.files()
	first, last := m.First, m.Last
	todos := append([]string{}, m.Todos...)
	seenTodo := map[string]bool{}
	for _, id := range todos {
		seenTodo[id] = true
	}
	for _, f := range files[1:] {
		if !f.First.IsZero() && f.First.Before(first) {
			first = f.First
		}
		if f.Last.After(last) {
			last = f.Last
		}
		for _, id := range f.Todos {
			if !seenTodo[id] {
				seenTodo[id] = true
				todos = append(todos, id)
			}
		}
	}
	root := t.root(m.Folder, files)
	days, hours, models := transcript.Aggregate(t.owned(id, s), t.Now().Add(-hoursWindow))
	sum := protocol.Summary{
		ID:          id,
		Folder:      m.Folder,
		Root:        root,
		Remote:      t.remote(root),
		Title:       m.Title,
		Excerpt:     Excerpt(m.LastPrompt),
		StartedAt:   protocol.Time(first),
		LastEventAt: protocol.Time(last),
		Tokens:      days,
		Hours:       hours,
		TodoWrites:  todos,
		CustomTitle: m.CustomTitle,
		Name:        m.AgentName,
		Version:     m.Version,
		Branch:      m.Branch,
		Topics:      t.readTopics(files),
		ContinuedIn: m.ContinuedIn,
		Models:      models,
		Ledger:      m.Ledger,
		Context:     m.Context,
		Compactions: m.Compactions,
		LimitHits:   m.Limits,
		PRs:         m.PRs,
	}
	if len(models) == 0 {
		sum.Models = nil
	}
	if t.regs != nil {
		t.registryState(&sum, id, m)
		return sum, true
	}
	ho, handed := hos[id]
	switch {
	case !handed || !t.Alive(ho):
		if _, ok := t.waits[id]; ok {
			delete(t.waits, id)
			t.Clear(id)
		}
		ended(&sum)
	case t.flagged(id, m):
		sum.State = "waiting"
	case m.Working:
		sum.State = "working"
	default:
		sum.State = "waiting"
	}
	return sum, true
}

func ended(sum *protocol.Summary) {
	sum.State = "ended"
	end := sum.LastEventAt
	sum.EndedAt = &end
}

var jobStatus = map[string]string{"working": registry.Busy, "blocked": registry.Waiting, "done": registry.Idle}

func (t *Tracker) registryState(sum *protocol.Summary, id string, m *transcript.File) {
	r, ok := t.regs[id]
	if !ok || !liveIn(r) {
		if _, had := t.bgs[id]; had {
			delete(t.bgs, id)
			t.ClearBackground(id)
		}
		ended(sum)
		return
	}
	sum.Kind = r.Kind
	if r.Name != "" {
		sum.Name = r.Name
	}
	if r.Version != "" {
		sum.Version = r.Version
	}
	if r.JobID != "" {
		sum.Job = &protocol.Job{ID: r.JobID, State: r.JobState}
	}
	status := r.Status
	if !r.Running() {
		status = jobStatus[r.JobState]
		if status == "" {
			status = registry.Waiting
		}
	}
	sum.Status = status
	switch status {
	case registry.Busy:
		sum.State = "working"
	case registry.Waiting:
		sum.State = "waiting"
		sum.WaitingFor = r.WaitingFor
	case registry.Idle:
		sum.State = "waiting"
		if bg, ok := t.bgs[id]; ok && r.Running() && (len(bg.Tasks) > 0 || bg.Crons > 0) {
			sum.Background = &protocol.Background{Tasks: bg.Tasks, Crons: bg.Crons}
			if len(bg.Tasks) > 0 {
				sum.State = "working"
			}
		}
	default:
		if m.Working {
			sum.State = "working"
		} else {
			sum.State = "waiting"
		}
	}
}

func (t *Tracker) flagged(id string, m *transcript.File) bool {
	at, ok := t.waits[id]
	if !ok {
		return false
	}
	if m.UserAt.After(at) {
		delete(t.waits, id)
		t.Clear(id)
		return false
	}
	return true
}

func Excerpt(prompt string) string {
	s := strings.Join(strings.Fields(mask.String(prompt)), " ")
	if utf8.RuneCountInString(s) <= ExcerptLen {
		return s
	}
	return string([]rune(s)[:ExcerptLen])
}

func (t *Tracker) root(folder string, files []*transcript.File) string {
	for _, edits := range []bool{true, false} {
		if root := t.mostHit(files, edits); root != "" {
			return root
		}
	}
	if root := t.gitRoot(folder); root != "" {
		return root
	}
	return folder
}

func (t *Tracker) mostHit(files []*transcript.File, edits bool) string {
	type stat struct {
		n    int
		last time.Time
	}
	by := map[string]*stat{}
	for _, f := range files {
		paths := f.Touches
		if edits {
			paths = f.Edits
		}
		for p, e := range paths {
			start := p
			if edits {
				start = filepath.Dir(p)
			}
			root := t.gitRoot(start)
			if root == "" {
				continue
			}
			s := by[root]
			if s == nil {
				s = &stat{}
				by[root] = s
			}
			s.n += e.Count
			if e.Last.After(s.last) {
				s.last = e.Last
			}
		}
	}
	best := ""
	for root, s := range by {
		b := by[best]
		if b == nil || s.n > b.n || (s.n == b.n && (s.last.After(b.last) || (s.last.Equal(b.last) && root < best))) {
			best = root
		}
	}
	return best
}

func (t *Tracker) remote(root string) string {
	if t.gitRoot(root) != root {
		return ""
	}
	if r, ok := t.remotes[root]; ok {
		return r
	}
	r := originURL(root)
	t.remotes[root] = r
	return r
}

func (t *Tracker) gitRoot(dir string) string {
	if dir == "" {
		return ""
	}
	if root, ok := t.roots[dir]; ok {
		return root
	}
	root := ""
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		root = dir
	} else if parent := filepath.Dir(dir); parent != dir {
		root = t.gitRoot(parent)
	}
	t.roots[dir] = root
	return root
}
