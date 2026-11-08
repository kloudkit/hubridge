package home

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const QueueCap = 1000

type Queued struct {
	ID   string
	Data []byte
}

type Queue struct {
	Cap  int
	dir  string
	mu   sync.Mutex
	last int64
}

func (h *Home) Queue() *Queue { return &Queue{Cap: QueueCap, dir: h.Path("queue")} }

func (q *Queue) NewID(key string) string {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := time.Now().UnixNano()
	if n <= q.last {
		n = q.last + 1
	}
	q.last = n
	return fmt.Sprintf("%019d-%s", n, key)
}

func keyOf(id string) string {
	if _, k, ok := strings.Cut(id, "-"); ok {
		return k
	}
	return ""
}

func (q *Queue) Put(id string, data []byte) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	key := keyOf(id)
	for _, old := range q.names() {
		if key != "" && keyOf(old) == key && old != id {
			os.Remove(filepath.Join(q.dir, old+".json"))
		}
	}
	if err := WriteFile(filepath.Join(q.dir, id+".json"), data); err != nil {
		return err
	}
	names := q.names()
	for len(names) > q.Cap {
		os.Remove(filepath.Join(q.dir, names[0]+".json"))
		names = names[1:]
	}
	return nil
}

func (q *Queue) Ack(id string) {
	if strings.ContainsAny(id, `/\`) || id == "" {
		return
	}
	os.Remove(filepath.Join(q.dir, id+".json"))
}

func (q *Queue) Pending() []Queued {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []Queued
	for _, n := range q.names() {
		b, err := os.ReadFile(filepath.Join(q.dir, n+".json"))
		if err == nil {
			out = append(out, Queued{ID: n, Data: b})
		}
	}
	return out
}

func (q *Queue) names() []string {
	entries, _ := os.ReadDir(q.dir)
	var out []string
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() && strings.HasSuffix(n, ".json") && !strings.HasPrefix(n, ".") {
			out = append(out, strings.TrimSuffix(n, ".json"))
		}
	}
	slices.Sort(out)
	return out
}
