package proc

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestAliveComparesStartTime(t *testing.T) {
	pid := int32(os.Getpid())
	started, ok := StartTime(pid)
	if !ok {
		t.Fatal("no start time for self")
	}
	if !Alive(pid, started) {
		t.Error("self not alive with its own start time")
	}
	if !Alive(pid, started/1000) {
		t.Error("start time in seconds not accepted")
	}
	if Alive(pid, started-60_000) {
		t.Error("a reused pid with a different start time counted as alive")
	}
	dead := exec.Command("true")
	dead.Run()
	if Alive(int32(dead.Process.Pid), started) {
		t.Error("exited process counted as alive")
	}
}

func TestStatStartMatchesItself(t *testing.T) {
	a, ok := StatStart(int32(os.Getpid()))
	if !ok {
		t.Skip("no /proc on this platform")
	}
	if b, _ := StatStart(int32(os.Getpid())); a == "" || a != b {
		t.Fatalf("start = %q then %q, want one stable value", a, b)
	}
	if _, ok := StatStart(-1); ok {
		t.Fatal("a negative pid has no start")
	}
}

func TestClaudeFallsBackToLocalBin(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	if _, err := Claude(); err == nil {
		t.Fatal("found claude with an empty PATH and HOME")
	}
	bin := filepath.Join(home, ".local", "bin", "claude")
	os.MkdirAll(filepath.Dir(bin), 0o700)
	os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o700)
	if p, err := Claude(); err != nil || p != bin {
		t.Fatalf("Claude() = %q, %v; want %q", p, err, bin)
	}
}
