package home

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/gofrs/flock"
)

type Lock struct{ f *flock.Flock }

func (h *Home) Held(name string) bool {
	f := flock.New(h.Path(name))
	got, err := f.TryLock()
	if err != nil {
		return false
	}
	if got {
		f.Unlock()
	}
	return !got
}

func (h *Home) LockFile(name string) (*Lock, error) {
	path := h.Path(name)
	f := flock.New(path, flock.SetFlag(os.O_CREATE|os.O_RDWR))
	got, err := f.TryLock()
	if err != nil {
		return nil, err
	}
	if !got {
		return nil, fmt.Errorf("another daemon holds the lock (pid %s)", holder(path))
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		f.Unlock()
		return nil, err
	}
	return &Lock{f: f}, nil
}

func holder(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(b))
}

func (l *Lock) Release() { l.f.Unlock() }
