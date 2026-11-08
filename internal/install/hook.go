package install

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/kloudkit/hubridge/internal/home"
	"github.com/kloudkit/hubridge/internal/proc"
)

const (
	StdinWait = 30 * time.Millisecond
	StopWait  = 500 * time.Millisecond
)

type Lookup func(pid int32) (proc.Info, bool)

type HookInput struct {
	SessionID        string `json:"session_id"`
	NotificationType string `json:"notification_type"`
	Type             string `json:"type"`
	BackgroundTasks  *[]struct {
		Type string `json:"type"`
	} `json:"background_tasks"`
	SessionCrons []json.RawMessage `json:"session_crons"`
}

func ReadInput(r io.Reader) HookInput { return ReadInputWait(r, StdinWait) }

func ReadInputWait(r io.Reader, wait time.Duration) HookInput {
	done := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(io.LimitReader(r, 1<<20))
		done <- b
	}()
	var in HookInput
	select {
	case b := <-done:
		json.Unmarshal(b, &in)
	case <-time.After(wait):
	}
	return in
}

func ClaudePID(start int32, lookup Lookup) int32 {
	pid := start
	for range 16 {
		info, ok := lookup(pid)
		if !ok {
			break
		}
		if info.Name == "claude" || filepath.Base(info.Argv0) == "claude" {
			return pid
		}
		if info.PPID <= 1 {
			break
		}
		pid = info.PPID
	}
	return start
}

func SessionStart(h *home.Home, in HookInput, parent int32, lookup Lookup, start func() error) error {
	if in.SessionID == "" {
		return nil
	}
	if err := h.WriteHandover(in.SessionID, ClaudePID(parent, lookup)); err != nil {
		return err
	}
	if h.Held("daemon.lock") || h.Held("supervisor.lock") {
		return nil
	}
	return start()
}

func Notification(h *home.Home, in HookInput, now time.Time) error {
	if in.SessionID == "" {
		return nil
	}
	return h.MarkWaiting(in.SessionID, now)
}

// A missing background_tasks array means Claude Code could not read its task registry.
func Stop(h *home.Home, in HookInput, now time.Time) error {
	if in.SessionID == "" {
		return nil
	}
	if in.BackgroundTasks == nil {
		h.ClearBackground(in.SessionID)
		return nil
	}
	b := home.Background{At: now.UTC(), Crons: len(in.SessionCrons)}
	for _, t := range *in.BackgroundTasks {
		kind := t.Type
		if kind == "" {
			kind = "task"
		}
		if b.Tasks == nil {
			b.Tasks = map[string]int{}
		}
		b.Tasks[kind]++
	}
	return h.WriteBackground(in.SessionID, b)
}

func Parent() int32 { return int32(os.Getppid()) }
