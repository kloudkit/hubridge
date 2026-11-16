package scope

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type Claim struct {
	Slug      string  `json:"slug"`
	Type      string  `json:"type"`
	Strength  *string `json:"strength"`
	Statement string  `json:"statement"`
	Detail    string  `json:"detail"`
	Scope     string  `json:"scope"`
}

type Profile struct {
	Scopes []string `json:"scopes"`
	Voice  *string  `json:"voice"`
	Style  *string  `json:"style"`
	Claims []Claim  `json:"claims"`
	Note   *string  `json:"note"`
}

type Client struct {
	URL   string
	Token string
	HTTP  *http.Client
}

var ErrAbsent = errors.New("this hubd has no scopes yet")

func (c Client) do(ctx context.Context, method, path string, body any, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.URL, "/")+"/api/estate"+path, r)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err := errors.Join(err, resp.Body.Close()); err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		return ErrAbsent
	}
	if resp.StatusCode != http.StatusOK {
		var problem struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(b, &problem) == nil && problem.Error != "" {
			return errors.New(problem.Error)
		}
		return fmt.Errorf("hubd answered HTTP %d", resp.StatusCode)
	}
	return json.Unmarshal(b, out)
}

func (c Client) Profile(ctx context.Context, session string) (Profile, error) {
	var p Profile
	err := c.do(ctx, http.MethodGet, "/profile?session="+url.QueryEscape(session), nil, &p)
	return p, err
}

// Set makes scope the session's active scope; an empty scope leaves only general.
func (c Client) Set(ctx context.Context, session, scope, machine string) error {
	body := map[string]any{"client": session, "scope": nil, "machine": machine}
	if scope != "" {
		body["scope"] = scope
	}
	var out struct {
		Client string `json:"client"`
	}
	return c.do(ctx, http.MethodPost, "/scope", body, &out)
}

func (p Profile) Active() string {
	for _, s := range p.Scopes {
		if s != "general" {
			return s
		}
	}
	return ""
}

func (p Profile) Empty() bool {
	return len(p.Claims) == 0 && p.Voice == nil && p.Style == nil && p.Active() == ""
}

// Render is the text a session is given: what holds without asking, then how to answer and how to write.
func Render(p Profile) string {
	if p.Empty() {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "hubd profile in force: %s\n", strings.Join(p.Scopes, " + "))
	if active := p.Active(); active != "" {
		fmt.Fprintf(&b, "The scope “%s” is active for this session. Estate answers include it and put it first; new claims go to it unless they name another scope.\n", active)
	}
	if len(p.Claims) > 0 {
		b.WriteString("\nFollow these without being asked:\n")
		for _, c := range p.Claims {
			kind := c.Type
			if c.Strength != nil {
				kind += ", " + *c.Strength
			}
			fmt.Fprintf(&b, "- [%s] %s", kind, c.Statement)
			if d := strings.TrimSpace(c.Detail); d != "" {
				fmt.Fprintf(&b, " %s", strings.Join(strings.Fields(d), " "))
			}
			fmt.Fprintf(&b, " (hubd:claim/%s, %s)\n", c.Slug, c.Scope)
		}
	}
	if p.Note != nil {
		fmt.Fprintf(&b, "%s\n", *p.Note)
	}
	if p.Style != nil {
		fmt.Fprintf(&b, "\nStyle for answers to the user:\n%s\n", strings.TrimSpace(*p.Style))
	}
	if p.Voice != nil {
		fmt.Fprintf(&b, "\nVoice for text written on the user's behalf:\n%s\n", strings.TrimSpace(*p.Voice))
	}
	return b.String()
}
