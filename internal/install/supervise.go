package install

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/cenkalti/backoff/v5"

	"github.com/kloudkit/hubridge/internal/home"
)

const ExitRevoked = 3

var ErrUpgraded = errors.New("a new hubridge binary was installed")

type Supervisor struct {
	TokenPoll  time.Duration
	BinaryPoll time.Duration
	Home       *home.Home
	Log        *slog.Logger
	Self       string
	MinBackoff time.Duration
	MaxBackoff time.Duration
	Healthy    time.Duration
	Window     time.Duration
	Crashes    int
}

func NewSupervisor(h *home.Home, self string, log *slog.Logger) *Supervisor {
	return &Supervisor{Home: h, Log: log, Self: self, MinBackoff: time.Second, MaxBackoff: time.Minute, Healthy: time.Minute, Window: 5 * time.Minute, Crashes: 3, TokenPoll: time.Minute, BinaryPoll: 15 * time.Second}
}

func (s *Supervisor) replaced(started os.FileInfo) bool {
	if started == nil {
		return false
	}
	now, err := os.Stat(s.bin())
	return err == nil && !os.SameFile(started, now)
}

func stop(cmd *exec.Cmd, exited <-chan error) {
	cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		<-exited
	}
}

func (s *Supervisor) bin() string { return Binary(s.Home.Dir) }

func installed(homeDir, self string) string {
	bin := Binary(homeDir)
	if _, err := os.Stat(bin); err != nil {
		return self
	}
	return bin
}

func (s *Supervisor) Run(ctx context.Context) error {
	lock, err := s.Home.LockFile("supervisor.lock")
	if err != nil {
		return err
	}
	defer lock.Release()
	wait := &backoff.ExponentialBackOff{InitialInterval: s.MinBackoff, RandomizationFactor: 0.5, Multiplier: 2, MaxInterval: s.MaxBackoff}
	wait.Reset()
	var crashes []time.Time
	for {
		began := time.Now()
		bin := installed(s.Home.Dir, s.Self)
		started, _ := os.Stat(bin)
		cmd := exec.Command(bin, "run", "--home", s.Home.Dir)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			s.Log.Error("start daemon", "err", err)
		} else {
			exit, done, err := s.await(ctx, cmd, started)
			if done {
				return err
			}
			if ee, ok := errors.AsType[*exec.ExitError](exit); ok && ee.ExitCode() == ExitRevoked {
				if !s.awaitToken(ctx) {
					return nil
				}
				crashes = nil
				wait.Reset()
				continue
			}
			s.Log.Warn("daemon exited", "err", exit)
		}
		now := time.Now()
		if now.Sub(began) >= s.Healthy {
			wait.Reset()
		}
		crashes = slices.DeleteFunc(append(crashes, now), func(c time.Time) bool { return now.Sub(c) > s.Window })
		if len(crashes) >= s.Crashes && s.rollback() {
			crashes = nil
			wait.Reset()
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait.NextBackOff()):
		}
	}
}

func (s *Supervisor) await(ctx context.Context, cmd *exec.Cmd, started os.FileInfo) (exit error, done bool, err error) {
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	poll := time.NewTicker(s.BinaryPoll)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			stop(cmd, exited)
			return nil, true, nil
		case <-poll.C:
			if s.replaced(started) {
				s.Log.Info("a new binary was installed; restarting on it")
				stop(cmd, exited)
				return nil, true, ErrUpgraded
			}
		case exit = <-exited:
			return exit, false, nil
		}
	}
}

func (s *Supervisor) awaitToken(ctx context.Context) bool {
	before, _ := os.ReadFile(s.Home.Path("token"))
	s.Log.Error("daemon stopped: its token was revoked or the machine removed; waiting for a new token (rerun the installer)")
	t := time.NewTicker(s.TokenPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
			now, err := os.ReadFile(s.Home.Path("token"))
			if err == nil && len(bytes.TrimSpace(now)) > 0 && !bytes.Equal(now, before) {
				s.Log.Info("token changed; restarting the daemon")
				return true
			}
		}
	}
}

func (s *Supervisor) rollback() bool {
	cur, prev := s.bin(), s.bin()+".prev"
	if _, err := os.Stat(prev); err != nil {
		return false
	}
	tmp := filepath.Join(filepath.Dir(cur), ".hubridge.swap")
	if err := os.Rename(cur, tmp); err != nil {
		s.Log.Error("rollback", "err", err)
		return false
	}
	if err := os.Rename(prev, cur); err != nil {
		if rerr := os.Rename(tmp, cur); rerr != nil {
			s.Log.Error("rollback could not put the current binary back", "path", tmp, "err", rerr)
		}
		s.Log.Error("rollback", "err", err)
		return false
	}
	if err := os.Rename(tmp, prev); err != nil {
		s.Log.Error("rollback kept the crashing binary aside", "path", tmp, "err", err)
	}
	s.Log.Error("daemon crashed repeatedly; rolled back to the previous binary", "crashes", s.Crashes, "window", s.Window)
	return true
}

func Detach(h *home.Home, self string) error {
	logf, err := os.OpenFile(h.Path("daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer devnull.Close()
	cmd := exec.Command(installed(h.Dir, self), "supervise", "--home", h.Dir)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Env = withoutToken(os.Environ())
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func withoutToken(env []string) []string {
	return slices.DeleteFunc(slices.Clone(env), func(e string) bool { return strings.HasPrefix(e, "HUBD_TOKEN=") })
}
