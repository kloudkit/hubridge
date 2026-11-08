package transcript

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/kloudkit/hubridge/internal/protocol"
)

const (
	hourFormat   = "2006-01-02T15:04:05Z"
	Synthetic    = "<synthetic>"
	maxMarkers   = 20
	maxPRs       = 20
	keySeparator = "|"
)

var ReadTypes = map[string]bool{
	"user": true, "assistant": true, "system": true, "ai-title": true, "custom-title": true, "agent-name": true,
	"last-prompt": true, "pr-link": true, "continued-in": true, "cost-state": true,
}

type Msg struct {
	At        time.Time
	Model     string
	Speed     string
	Main      bool
	Input     int64
	Output    int64
	CacheRead int64
	Write5m   int64
	Write1h   int64
	WebSearch int64
	WebFetch  int64
}

type File struct {
	Path   string
	Main   bool
	Offset int64
	Line   int

	Folder      string
	Title       string
	CustomTitle string
	AgentName   string
	ContinuedIn string
	Branch      string
	Version     string
	LastPrompt  string
	Working     bool
	First       time.Time
	Last        time.Time
	Headless    bool
	Edits       map[string]*Edit
	Touches     map[string]*Edit
	Msgs        map[string]*Msg
	PRs         []protocol.PR
	Compactions []protocol.Compaction
	Limits      []protocol.LimitHit
	Context     *protocol.Context
	Ledger      *protocol.Ledger
	Todos       []string
	Errors      []string

	fresh     []string
	pending   map[string]string
	model     string
	requested string
}

func NewFile(path string, main bool) *File {
	f := &File{Path: path, Main: main}
	f.reset()
	return f
}

func (f *File) reset() {
	*f = File{Path: f.Path, Main: f.Main}
	f.Edits = map[string]*Edit{}
	f.Touches = map[string]*Edit{}
	f.Msgs = map[string]*Msg{}
	f.pending = map[string]string{}
}

func (f *File) Fresh() []string {
	out := f.fresh
	f.fresh = nil
	return out
}

func Key(messageID, requestID string) string { return messageID + keySeparator + requestID }

type Edit struct {
	Count int
	Last  time.Time
}

var editTools = map[string]bool{"Edit": true, "Write": true, "MultiEdit": true, "NotebookEdit": true}

type quota struct {
	Status        string `json:"status"`
	ResetsAt      int64  `json:"resetsAt"`
	RateLimitType string `json:"rateLimitType"`
}

type compactMeta struct {
	Trigger    string `json:"trigger"`
	PreTokens  int64  `json:"preTokens"`
	PostTokens int64  `json:"postTokens"`
}

type ledgerModel struct {
	InputTokens  int64   `json:"inputTokens"`
	OutputTokens int64   `json:"outputTokens"`
	CacheRead    int64   `json:"cacheReadInputTokens"`
	CacheWrite   int64   `json:"cacheCreationInputTokens"`
	CacheWrite5m int64   `json:"cacheCreationEphemeral5mInputTokens"`
	CacheWrite1h int64   `json:"cacheCreationEphemeral1hInputTokens"`
	WebSearch    int64   `json:"webSearchRequests"`
	CostUSD      float64 `json:"costUSD"`
}

type entry struct {
	Type           string       `json:"type"`
	Entrypoint     string       `json:"entrypoint"`
	Subtype        string       `json:"subtype"`
	Timestamp      string       `json:"timestamp"`
	Cwd            string       `json:"cwd"`
	Version        string       `json:"version"`
	GitBranch      string       `json:"gitBranch"`
	AITitle        string       `json:"aiTitle"`
	CustomTitle    string       `json:"customTitle"`
	AgentName      string       `json:"agentName"`
	LastPrompt     string       `json:"lastPrompt"`
	ContinuedIn    string       `json:"continuedInSessionId"`
	PRNumber       int          `json:"prNumber"`
	PRURL          string       `json:"prUrl"`
	PRRepository   string       `json:"prRepository"`
	IsSidechain    bool         `json:"isSidechain"`
	IsAPIError     bool         `json:"isApiErrorMessage"`
	RequestID      string       `json:"requestId"`
	RequestedModel string       `json:"requestedModel"`
	QuotaLimits    *quota       `json:"quotaLimits"`
	Compact        *compactMeta `json:"compactMetadata"`
	Message        *message     `json:"message"`

	TotalCostUSD      *float64               `json:"totalCostUSD"`
	TotalLinesAdded   float64                `json:"totalLinesAdded"`
	TotalLinesRemoved float64                `json:"totalLinesRemoved"`
	TotalAPIDuration  float64                `json:"totalAPIDuration"`
	TotalToolDuration float64                `json:"totalToolDuration"`
	ModelUsage        map[string]ledgerModel `json:"modelUsage"`
}

type message struct {
	ID      string          `json:"id"`
	Model   string          `json:"model"`
	Usage   *usage          `json:"usage"`
	Content json.RawMessage `json:"content"`
}

type usage struct {
	Input         int64  `json:"input_tokens"`
	Output        int64  `json:"output_tokens"`
	CacheRead     int64  `json:"cache_read_input_tokens"`
	CacheWrite    int64  `json:"cache_creation_input_tokens"`
	Speed         string `json:"speed"`
	CacheCreation *struct {
		Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
		Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
	ServerToolUse *struct {
		WebSearch int64 `json:"web_search_requests"`
		WebFetch  int64 `json:"web_fetch_requests"`
	} `json:"server_tool_use"`
}

type block struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Text      string          `json:"text"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

func (f *File) Update() error {
	fh, err := os.Open(f.Path)
	if err != nil {
		return err
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		return err
	}
	if st.Size() < f.Offset {
		f.reset()
	}
	if st.Size() == f.Offset {
		return nil
	}
	if _, err := fh.Seek(f.Offset, io.SeekStart); err != nil {
		return err
	}
	r := bufio.NewReaderSize(fh, 64*1024)
	for {
		line, err := r.ReadBytes('\n')
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		f.Offset += int64(len(line))
		f.Line++
		f.consume(bytes.TrimSpace(line))
	}
}

func (f *File) stamp(ts string) (time.Time, bool) {
	if ts == "" {
		return time.Time{}, true
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		f.fail(err)
		return time.Time{}, false
	}
	at := t.UTC()
	if f.First.IsZero() || at.Before(f.First) {
		f.First = at
	}
	if at.After(f.Last) {
		f.Last = at
	}
	return at, true
}

func (f *File) consume(line []byte) {
	if len(line) == 0 {
		return
	}
	// Most lines are of types the daemon never reads, some of them large, so only their
	// timestamp and start folder are taken, without decoding the rest.
	if !ReadTypes[gjson.GetBytes(line, "type").String()] {
		if !gjson.ValidBytes(line) {
			var v struct{}
			f.fail(json.Unmarshal(line, &v))
			return
		}
		got := gjson.GetManyBytes(line, "timestamp", "isSidechain", "cwd")
		if _, ok := f.stamp(got[0].String()); ok && f.Main && !got[1].Bool() && f.Folder == "" {
			f.Folder = got[2].String()
		}
		return
	}
	var e entry
	if err := json.Unmarshal(line, &e); err != nil {
		f.fail(err)
		return
	}
	main := f.Main && !e.IsSidechain
	at, ok := f.stamp(e.Timestamp)
	if !ok {
		return
	}
	set(main && f.Folder == "", &f.Folder, e.Cwd)
	set(main, &f.Version, e.Version)
	set(main, &f.Branch, e.GitBranch)
	if (e.Type == "user" || e.Type == "assistant") && e.Entrypoint == "sdk-cli" {
		f.Headless = true
	}
	switch e.Type {
	case "ai-title":
		set(main, &f.Title, e.AITitle)
	case "custom-title":
		set(main, &f.CustomTitle, e.CustomTitle)
	case "agent-name":
		set(main, &f.AgentName, e.AgentName)
	case "continued-in":
		set(main, &f.ContinuedIn, e.ContinuedIn)
	case "pr-link":
		if main && e.PRURL != "" {
			f.pr(protocol.PR{Number: e.PRNumber, URL: e.PRURL, Repo: e.PRRepository})
		}
	case "cost-state":
		if main && e.TotalCostUSD != nil {
			f.Ledger = ledger(e, f.Last)
		}
	case "last-prompt":
		set(main, &f.LastPrompt, e.LastPrompt)
	case "system":
		if !main {
			break
		}
		switch e.Subtype {
		case "turn_duration":
			f.Working = false
		case "compact_boundary":
			if e.Compact != nil && !at.IsZero() {
				f.Compactions = keep(append(f.Compactions, protocol.Compaction{At: protocol.Time(at), Trigger: e.Compact.Trigger, PreTokens: e.Compact.PreTokens, PostTokens: e.Compact.PostTokens}), maxMarkers)
				f.Context = &protocol.Context{At: protocol.Time(at), Tokens: e.Compact.PostTokens, Model: f.model, Requested: f.requested}
			}
		}
	case "user":
		if e.Message != nil {
			f.user(e.Message.Content, main)
		}
	case "assistant":
		if e.Message != nil {
			f.assistant(e, main, at)
		}
	}
}

func set(ok bool, dst *string, v string) {
	if ok && v != "" {
		*dst = v
	}
}

func keep[T any](list []T, n int) []T {
	if len(list) <= n {
		return list
	}
	return append([]T(nil), list[len(list)-n:]...)
}

func (f *File) pr(p protocol.PR) {
	if slices.ContainsFunc(f.PRs, func(have protocol.PR) bool { return have.URL == p.URL }) {
		return
	}
	f.PRs = keep(append(f.PRs, p), maxPRs)
}

func ledger(e entry, at time.Time) *protocol.Ledger {
	l := &protocol.Ledger{
		CostUSD: *e.TotalCostUSD, LinesAdded: int64(e.TotalLinesAdded), LinesRemoved: int64(e.TotalLinesRemoved),
		APIMs: int64(e.TotalAPIDuration), ToolMs: int64(e.TotalToolDuration), Models: []protocol.LedgerModel{},
	}
	if !at.IsZero() {
		l.At = protocol.Time(at)
	}
	for _, name := range slices.Sorted(maps.Keys(e.ModelUsage)) {
		m := e.ModelUsage[name]
		w5, w1 := m.CacheWrite5m, m.CacheWrite1h
		if w5 == 0 && w1 == 0 {
			w5 = m.CacheWrite
		}
		l.Models = append(l.Models, protocol.LedgerModel{
			Model: name, Input: m.InputTokens, Output: m.OutputTokens, CacheRead: m.CacheRead,
			CacheWrite5m: w5, CacheWrite1h: w1, WebSearch: m.WebSearch, CostUSD: m.CostUSD,
		})
	}
	return l
}

func (f *File) fail(err error) {
	if len(f.Errors) < 20 {
		f.Errors = append(f.Errors, fmt.Sprintf("%s:%d: %v", f.Path, f.Line, err))
	}
}

func blocks(raw json.RawMessage) ([]block, string, bool) {
	if len(raw) == 0 {
		return nil, "", true
	}
	if raw[0] == '"' {
		var s string
		err := json.Unmarshal(raw, &s)
		return nil, s, err == nil
	}
	var bs []block
	err := json.Unmarshal(raw, &bs)
	return bs, "", err == nil
}

func (f *File) user(content json.RawMessage, main bool) {
	bs, text, ok := blocks(content)
	if !ok {
		f.fail(errors.New("user content is neither text nor blocks"))
		return
	}
	for _, b := range bs {
		switch b.Type {
		case "text":
			text = b.Text
		case "tool_result":
			f.toolResult(b)
		}
	}
	if !main || text == "" || strings.HasPrefix(text, "<") {
		return
	}
	f.Working = !strings.HasPrefix(text, "[Request interrupted")
}

func (f *File) assistant(e entry, main bool, at time.Time) {
	m := e.Message
	if main && e.IsAPIError && e.QuotaLimits != nil && !at.IsZero() {
		hit := protocol.LimitHit{At: protocol.Time(at), Type: e.QuotaLimits.RateLimitType, Status: e.QuotaLimits.Status}
		if e.QuotaLimits.ResetsAt > 0 {
			hit.ResetsAt = protocol.Time(time.Unix(e.QuotaLimits.ResetsAt, 0))
		}
		f.Limits = keep(append(f.Limits, hit), maxMarkers)
	}
	if u := m.Usage; u != nil && m.ID != "" && m.Model != Synthetic && !at.IsZero() {
		key := Key(m.ID, e.RequestID)
		if _, seen := f.Msgs[key]; !seen {
			msg := &Msg{At: at, Model: m.Model, Speed: u.Speed, Main: main, Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, Write5m: u.CacheWrite}
			if c := u.CacheCreation; c != nil && c.Ephemeral5m+c.Ephemeral1h > 0 {
				msg.Write5m, msg.Write1h = c.Ephemeral5m, c.Ephemeral1h
			}
			if s := u.ServerToolUse; s != nil {
				msg.WebSearch, msg.WebFetch = s.WebSearch, s.WebFetch
			}
			f.Msgs[key] = msg
			f.fresh = append(f.fresh, key)
		}
		if main && !e.IsAPIError {
			f.model, f.requested = m.Model, e.RequestedModel
			f.Context = &protocol.Context{At: protocol.Time(at), Tokens: u.Input + u.CacheWrite + u.CacheRead, Model: m.Model, Requested: e.RequestedModel}
		}
	}
	bs, _, ok := blocks(m.Content)
	if !ok {
		f.fail(errors.New("assistant content is neither text nor blocks"))
		return
	}
	for _, b := range bs {
		if b.Type != "tool_use" {
			continue
		}
		if editTools[b.Name] {
			f.edit(b.Input, at)
		} else {
			f.touch(b.Name, b.Input, e.Cwd, at)
		}
		if IsTodoWrite(b.Name) && b.ID != "" {
			f.pending[b.ID] = gjson.GetBytes(b.Input, "id").String()
		}
	}
}

func (f *File) edit(input json.RawMessage, at time.Time) {
	var in struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
	}
	if json.Unmarshal(input, &in) != nil {
		return
	}
	p := in.FilePath
	if p == "" {
		p = in.NotebookPath
	}
	f.bump(f.Edits, p, at)
}

func (f *File) toolResult(b block) {
	inputID, ok := f.pending[b.ToolUseID]
	if !ok {
		return
	}
	delete(f.pending, b.ToolUseID)
	if b.IsError {
		return
	}
	id := inputID
	if id == "" {
		id = resultID(b.Content)
	}
	if id != "" && !slices.Contains(f.Todos, id) {
		f.Todos = append(f.Todos, id)
	}
}

func resultID(raw json.RawMessage) string {
	bs, text, ok := blocks(raw)
	if !ok {
		return ""
	}
	for _, b := range bs {
		if b.Type == "text" {
			text = b.Text
			break
		}
	}
	var out struct {
		ID string `json:"id"`
	}
	if json.Unmarshal([]byte(text), &out) != nil {
		return ""
	}
	return out.ID
}

func IsTodoWrite(name string) bool {
	i := strings.LastIndex(name, "__todo_")
	if !strings.HasPrefix(name, "mcp__") || i < 0 || !strings.HasSuffix(name[:i], "hubd") {
		return false
	}
	switch name[i+len("__todo_"):] {
	case "", "list", "get":
		return false
	}
	return true
}

func Aggregate(msgs []*Msg, since time.Time) ([]protocol.DayTokens, []protocol.HourTokens, []protocol.ModelTokens) {
	days := map[string]protocol.DayTokens{}
	hours := map[string]protocol.HourTokens{}
	models := map[[3]string]protocol.ModelTokens{}
	for _, m := range msgs {
		day := m.At.Format("2006-01-02")
		t := protocol.Tokens{Input: m.Input, Output: m.Output, CacheRead: m.CacheRead, CacheWrite: m.Write5m + m.Write1h}
		days[day] = protocol.DayTokens{Day: day, Tokens: days[day].Plus(t)}
		if hourStart := m.At.Truncate(time.Hour); hourStart.Add(time.Hour).After(since) {
			hour := hourStart.Format(hourFormat)
			hours[hour] = protocol.HourTokens{Hour: hour, Tokens: hours[hour].Plus(t)}
		}
		k := [3]string{day, m.Model, m.Speed}
		mt := models[k]
		mt.Day, mt.Model, mt.Speed = day, m.Model, m.Speed
		mt.Input += m.Input
		mt.Output += m.Output
		mt.CacheRead += m.CacheRead
		mt.CacheWrite5m += m.Write5m
		mt.CacheWrite1h += m.Write1h
		mt.WebSearch += m.WebSearch
		mt.WebFetch += m.WebFetch
		models[k] = mt
	}
	dayList := slices.SortedFunc(maps.Values(days), func(a, b protocol.DayTokens) int { return strings.Compare(a.Day, b.Day) })
	hourList := slices.DeleteFunc(slices.SortedFunc(maps.Values(hours), func(a, b protocol.HourTokens) int { return strings.Compare(a.Hour, b.Hour) }),
		func(h protocol.HourTokens) bool { return h.Tokens == protocol.Tokens{} })
	modelList := slices.SortedFunc(maps.Values(models), func(a, b protocol.ModelTokens) int {
		return cmp.Or(strings.Compare(a.Day, b.Day), strings.Compare(a.Model, b.Model), strings.Compare(a.Speed, b.Speed))
	})
	return dayList, hourList, modelList
}
