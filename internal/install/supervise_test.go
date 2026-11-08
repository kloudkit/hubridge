package install

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kloudkit/hubridge/internal/home"
)

func TestSuperviseRollsBackAfterRepeatedCrashes(t *testing.T) {
	h, _ := home.Open(t.TempDir())
	bin := h.Path("bin", "hubridge")
	os.MkdirAll(filepath.Dir(bin), 0o700)
	marker := filepath.Join(h.Dir, "prev-ran")
	crashing := "#!/bin/sh\nexit 1\n"
	good := "#!/bin/sh\ntouch '" + marker + "'\nexec sleep 30\n"
	os.WriteFile(bin, []byte(crashing), 0o700)
	os.WriteFile(bin+".prev", []byte(good), 0o700)

	s := NewSupervisor(h, "/nonexistent", slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.MinBackoff, s.MaxBackoff = 5*time.Millisecond, 10*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("previous binary never ran")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if strings.TrimSpace(readFile(h.Path("supervisor.lock"))) != strconv.Itoa(os.Getpid()) {
		t.Fatal("supervisor lock not held")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if readFile(bin) != good || readFile(bin+".prev") != crashing {
		t.Fatal("binaries not swapped")
	}
}

func TestSuperviseHandsOverWhenANewBinaryIsInstalled(t *testing.T) {
	h, _ := home.Open(t.TempDir())
	bin := h.Path("bin", "hubridge")
	os.MkdirAll(filepath.Dir(bin), 0o700)
	os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 30\n"), 0o700)

	s := NewSupervisor(h, "/nonexistent", slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.BinaryPoll = 10 * time.Millisecond
	done := make(chan error, 1)
	go func() { done <- s.Run(context.Background()) }()

	time.Sleep(50 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("supervisor returned before a new binary: %v", err)
	default:
	}
	os.WriteFile(bin+".new", []byte("#!/bin/sh\nexec sleep 30\n"), 0o700)
	os.Rename(bin+".new", bin)

	select {
	case err := <-done:
		if !errors.Is(err, ErrUpgraded) {
			t.Fatalf("want ErrUpgraded, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor kept running the old binary")
	}
	if h.Held("supervisor.lock") {
		t.Fatal("supervisor lock still held after the hand-over")
	}
}

func readFile(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}

func TestSuperviseWaitsForNewTokenAfterRevoke(t *testing.T) {
	h, _ := home.Open(t.TempDir())
	h.WriteToken("hubd_old")
	bin := h.Path("bin", "hubridge")
	os.MkdirAll(filepath.Dir(bin), 0o700)
	runs := filepath.Join(h.Dir, "runs")
	os.WriteFile(bin, []byte("#!/bin/sh\necho run >> '"+runs+"'\nexit 3\n"), 0o700)
	os.WriteFile(bin+".prev", []byte("#!/bin/sh\nexit 0\n"), 0o700)

	s := NewSupervisor(h, "/nonexistent", slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.MinBackoff, s.MaxBackoff, s.TokenPoll = time.Millisecond, time.Millisecond, 20*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	count := func() int { return strings.Count(readFile(runs), "run") }
	waitFor := func(n int) {
		deadline := time.Now().Add(5 * time.Second)
		for count() < n {
			if time.Now().After(deadline) {
				t.Fatalf("want %d runs, got %d", n, count())
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	waitFor(1)
	time.Sleep(200 * time.Millisecond)
	if n := count(); n != 1 {
		t.Fatalf("revoked daemon restarted %d times without a new token", n)
	}
	if strings.Contains(readFile(bin), "exit 0") {
		t.Fatal("a revoke was counted as a crash and rolled back")
	}
	h.WriteToken("hubd_new")
	waitFor(2)
	cancel()
	<-done
}
