package install

import (
	"cmp"
	"encoding/json"
	"io"
	"time"

	"github.com/kloudkit/hubridge/internal/home"
)

const (
	StdinWait = 30 * time.Millisecond
	StopWait  = 500 * time.Millisecond
)

type HookInput struct {
	SessionID       string `json:"session_id"`
	BackgroundTasks *[]struct {
		Type string `json:"type"`
	} `json:"background_tasks"`
	SessionCrons []json.RawMessage `json:"session_crons"`
}

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

func SessionStart(h *home.Home, start func() error) error {
	if h.Held("daemon.lock") || h.Held("supervisor.lock") {
		return nil
	}
	return start()
}

// A missing background_tasks array means Claude Code could not read its task registry.
func Stop(h *home.Home, in HookInput, now time.Time) error {
	if in.BackgroundTasks == nil {
		h.ClearBackground(in.SessionID)
		return nil
	}
	b := home.Background{At: now.UTC(), Tasks: map[string]int{}, Crons: len(in.SessionCrons)}
	for _, t := range *in.BackgroundTasks {
		b.Tasks[cmp.Or(t.Type, "task")]++
	}
	return h.WriteBackground(in.SessionID, b)
}
