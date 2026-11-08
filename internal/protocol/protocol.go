package protocol

import (
	"encoding/json"
	"time"

	hubd "github.com/kloudkit/hubd/protocol"
)

const (
	Version    = hubd.Version
	timeFormat = "2006-01-02T15:04:05.000Z"
)

func Time(t time.Time) string { return t.UTC().Format(timeFormat) }

type Tokens struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
}

func (t Tokens) Plus(o Tokens) Tokens {
	return Tokens{t.Input + o.Input, t.Output + o.Output, t.CacheRead + o.CacheRead, t.CacheWrite + o.CacheWrite}
}

type DayTokens struct {
	Day string `json:"day"`
	Tokens
}

type HourTokens struct {
	Hour string `json:"hour"`
	Tokens
}

type ModelTokens struct {
	Day          string `json:"day"`
	Model        string `json:"model"`
	Speed        string `json:"speed,omitempty"`
	Input        int64  `json:"input"`
	Output       int64  `json:"output"`
	CacheRead    int64  `json:"cacheRead"`
	CacheWrite5m int64  `json:"cacheWrite5m"`
	CacheWrite1h int64  `json:"cacheWrite1h"`
	WebSearch    int64  `json:"webSearch,omitempty"`
	WebFetch     int64  `json:"webFetch,omitempty"`
}

type LedgerModel struct {
	Model        string  `json:"model"`
	Input        int64   `json:"input"`
	Output       int64   `json:"output"`
	CacheRead    int64   `json:"cacheRead"`
	CacheWrite5m int64   `json:"cacheWrite5m"`
	CacheWrite1h int64   `json:"cacheWrite1h"`
	WebSearch    int64   `json:"webSearch,omitempty"`
	CostUSD      float64 `json:"costUSD"`
}

type Ledger struct {
	At           string        `json:"at,omitempty"`
	CostUSD      float64       `json:"costUSD"`
	LinesAdded   int64         `json:"linesAdded"`
	LinesRemoved int64         `json:"linesRemoved"`
	APIMs        int64         `json:"apiMs"`
	ToolMs       int64         `json:"toolMs"`
	Models       []LedgerModel `json:"models"`
}

type Context struct {
	At        string `json:"at"`
	Tokens    int64  `json:"tokens"`
	Model     string `json:"model,omitempty"`
	Requested string `json:"requestedModel,omitempty"`
}

type Compaction struct {
	At         string `json:"at"`
	Trigger    string `json:"trigger,omitempty"`
	PreTokens  int64  `json:"preTokens"`
	PostTokens int64  `json:"postTokens"`
}

type LimitHit struct {
	At       string `json:"at"`
	Type     string `json:"type,omitempty"`
	ResetsAt string `json:"resetsAt,omitempty"`
	Status   string `json:"status,omitempty"`
}

type PR struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	Repo   string `json:"repo,omitempty"`
}

type Background struct {
	Tasks map[string]int `json:"tasks,omitempty"`
	Crons int            `json:"crons,omitempty"`
}

type Job struct {
	ID    string `json:"id"`
	State string `json:"state,omitempty"`
}

type Summary struct {
	ID          string        `json:"id"`
	Folder      string        `json:"folder"`
	Root        string        `json:"root"`
	Remote      string        `json:"remote,omitempty"`
	Title       string        `json:"title"`
	Excerpt     string        `json:"excerpt"`
	State       string        `json:"state"`
	StartedAt   string        `json:"startedAt"`
	LastEventAt string        `json:"lastEventAt"`
	EndedAt     *string       `json:"endedAt"`
	Tokens      []DayTokens   `json:"tokens"`
	Hours       []HourTokens  `json:"hours,omitempty"`
	TodoWrites  []string      `json:"todoWrites"`
	Status      string        `json:"status,omitempty"`
	WaitingFor  string        `json:"waitingFor,omitempty"`
	Name        string        `json:"name,omitempty"`
	CustomTitle string        `json:"customTitle,omitempty"`
	Kind        string        `json:"kind,omitempty"`
	Job         *Job          `json:"job,omitempty"`
	Version     string        `json:"version,omitempty"`
	Branch      string        `json:"branch,omitempty"`
	Topics      []string      `json:"topics,omitempty"`
	ContinuedIn string        `json:"continuedIn,omitempty"`
	Models      []ModelTokens `json:"models,omitempty"`
	Ledger      *Ledger       `json:"ledger,omitempty"`
	Context     *Context      `json:"context,omitempty"`
	Compactions []Compaction  `json:"compactions,omitempty"`
	LimitHits   []LimitHit    `json:"limitHits,omitempty"`
	PRs         []PR          `json:"prs,omitempty"`
	Background  *Background   `json:"background,omitempty"`
}

type Hello struct {
	Type     string `json:"type"`
	Machine  string `json:"machine"`
	Protocol int    `json:"protocol"`
	Version  string `json:"version"`
	Channel  string `json:"channel"`
}

type Ping struct {
	Type string `json:"type"`
}

type Session struct {
	Type    string  `json:"type"`
	ID      string  `json:"id"`
	Session Summary `json:"session"`
}

type Live struct {
	Type    string   `json:"type"`
	Live    []string `json:"live"`
	Present []string `json:"present"`
}

type Window struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    string  `json:"resetsAt"`
}

type Reading struct {
	FiveHour *Window `json:"fiveHour,omitempty"`
	SevenDay *Window `json:"sevenDay,omitempty"`
	Source   string  `json:"source"`
	At       string  `json:"at"`
}

type Usage struct {
	Type  string  `json:"type"`
	Usage Reading `json:"usage"`
}

func NewUsage(r Reading) Usage { return Usage{Type: "usage", Usage: r} }

type Result struct {
	Type  string `json:"type"`
	Of    string `json:"of"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type Server struct {
	Type        string          `json:"type"`
	Protocol    int             `json:"protocol,omitempty"`
	Reason      string          `json:"reason,omitempty"`
	MinProtocol int             `json:"minProtocol,omitempty"`
	MaxProtocol int             `json:"maxProtocol,omitempty"`
	Of          string          `json:"of,omitempty"`
	Error       string          `json:"error,omitempty"`
	ID          string          `json:"id,omitempty"`
	Command     string          `json:"command,omitempty"`
	Args        json.RawMessage `json:"args,omitempty"`
}

func NewSession(id string, s Summary) Session { return Session{Type: "session", ID: id, Session: s} }

func NewLive(live, present []string) Live {
	if live == nil {
		live = []string{}
	}
	if present == nil {
		present = []string{}
	}
	return Live{Type: "live", Live: live, Present: present}
}
