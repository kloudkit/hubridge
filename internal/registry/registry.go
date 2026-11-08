package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kloudkit/hubridge/internal/proc"
)

const (
	Busy    = "busy"
	Waiting = "waiting"
	Idle    = "idle"

	Interactive = "interactive"
	Background  = "background"
)

var statuses = map[string]string{"busy": Busy, "shell": Busy, "waiting": Waiting, "idle": Idle}

type File struct {
	PID        int    `json:"pid"`
	SessionID  string `json:"sessionId"`
	ProcStart  string `json:"procStart"`
	Version    string `json:"version"`
	Kind       string `json:"kind"`
	PIDDomain  string `json:"pidDomain"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	WaitingFor string `json:"waitingFor"`
	JobID      string `json:"jobId"`
}

type Agent struct {
	ID         string `json:"id"`
	PID        int    `json:"pid"`
	Kind       string `json:"kind"`
	SessionID  string `json:"sessionId"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	WaitingFor string `json:"waitingFor"`
	State      string `json:"state"`
}

type Session struct {
	PID        int
	Kind       string
	Name       string
	Status     string
	WaitingFor string
	JobID      string
	JobState   string
	Version    string
}

func (s Session) Running() bool { return s.PID > 0 }

type Registry struct {
	Dir       string
	Claude    string
	Fresh     time.Duration
	Domain    string
	Now       func() time.Time
	StatStart func(int32) (string, bool)
	Running   func(int32) bool
	Run       func(ctx context.Context, bin string, args ...string) ([]byte, error)

	mu       sync.Mutex
	agents   []Agent
	agentsAt time.Time
	last     map[string]string
}

func claudePath() string {
	p, _ := proc.Claude()
	return p
}

func New(dir string) *Registry {
	return &Registry{
		Dir:       dir,
		Claude:    claudePath(),
		Fresh:     3 * time.Minute,
		Domain:    proc.Domain(),
		Now:       time.Now,
		StatStart: proc.StatStart,
		Running:   proc.Running,
		Run:       run,
		last:      map[string]string{},
	}
}

func run(ctx context.Context, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = os.TempDir()
	return cmd.Output()
}

func (r *Registry) Available() bool {
	st, err := os.Stat(r.Dir)
	return err == nil && st.IsDir()
}

func (r *Registry) Files() (map[string]File, map[string]error) {
	files, errs := map[string]File{}, map[string]error{}
	entries, err := os.ReadDir(r.Dir)
	if err != nil {
		return files, errs
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		stem := strings.TrimSuffix(name, ".json")
		b, err := os.ReadFile(filepath.Join(r.Dir, name))
		if err != nil {
			if !os.IsNotExist(err) {
				errs[stem] = err
			}
			continue
		}
		var f File
		if err := json.Unmarshal(b, &f); err != nil {
			errs[stem] = err
			continue
		}
		files[stem] = f
	}
	return files, errs
}

func (r *Registry) Alive(stem string, f File) bool {
	if f.PID <= 0 || strconv.Itoa(f.PID) != stem {
		return false
	}
	if f.PIDDomain != "" && r.Domain != "" && f.PIDDomain != r.Domain {
		return false
	}
	if start, ok := r.StatStart(int32(f.PID)); ok {
		return f.ProcStart == "" || start == f.ProcStart
	}
	return r.Running(int32(f.PID))
}

func (r *Registry) Reconcile(ctx context.Context) error {
	out, err := r.List(ctx)
	if err != nil {
		return fmt.Errorf("claude agents --json: %w", err)
	}
	var list []Agent
	if err := json.Unmarshal(out, &list); err != nil {
		return fmt.Errorf("claude agents --json: %w", err)
	}
	r.Use(list)
	return nil
}

func (r *Registry) List(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return r.Run(ctx, r.Claude, "agents", "--json", "--all")
}

func (r *Registry) Use(list []Agent) {
	r.mu.Lock()
	r.agents, r.agentsAt = list, r.Now()
	r.mu.Unlock()
}

func Normalize(raw string) (string, bool) {
	s, ok := statuses[raw]
	return s, ok
}

func (r *Registry) status(id, raw string) string {
	if s, ok := statuses[raw]; ok {
		r.last[id] = s
	}
	return r.last[id]
}

func (r *Registry) Sessions() map[string]Session {
	files, _ := r.Files()
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]Session{}
	for _, stem := range slices.Sorted(maps.Keys(files)) {
		f := files[stem]
		if f.SessionID == "" || !r.Alive(stem, f) {
			continue
		}
		kind := f.Kind
		if kind == "" {
			kind = Interactive
		}
		out[f.SessionID] = Session{
			PID: f.PID, Kind: kind, Name: f.Name, Status: r.status(f.SessionID, f.Status),
			WaitingFor: f.WaitingFor, JobID: f.JobID, Version: f.Version,
		}
	}
	if r.agentsAt.IsZero() || r.Now().Sub(r.agentsAt) > r.Fresh {
		return out
	}
	for _, a := range r.agents {
		if a.SessionID == "" {
			continue
		}
		s, ok := out[a.SessionID]
		switch {
		case ok:
			if a.Name != "" {
				s.Name = a.Name
			}
			if a.Kind != "" {
				s.Kind = a.Kind
			}
			if a.ID != "" {
				s.JobID = a.ID
			}
			if a.State != "" {
				s.JobState = a.State
			}
			if statuses[a.Status] == Waiting && s.Status == Waiting && a.WaitingFor != "" {
				s.WaitingFor = a.WaitingFor
			}
			out[a.SessionID] = s
		case a.Kind == Background && a.PID == 0:
			out[a.SessionID] = Session{Kind: Background, Name: a.Name, JobID: a.ID, JobState: a.State}
		}
	}
	return out
}
