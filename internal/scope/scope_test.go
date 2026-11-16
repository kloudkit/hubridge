package scope

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func ptr(s string) *string { return &s }

func TestRenderSaysNothingForAnEmptyProfile(t *testing.T) {
	if got := Render(Profile{Scopes: []string{"general"}}); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderListsClaimsThenStyleThenVoice(t *testing.T) {
	got := Render(Profile{
		Scopes: []string{"general", "work"},
		Claims: []Claim{
			{Slug: "never-print-a-secret", Type: "Rule", Strength: ptr("must"), Statement: "Never print a secret value.", Detail: "Names are fine.\nValues are not.", Scope: "general"},
			{Slug: "shell-is-bash", Type: "Trap", Statement: "The shell is bash.", Scope: "work"},
		},
		Note:  ptr("Where a claim of “work” and a claim of general disagree, follow “work”."),
		Style: ptr("short, numbered"), Voice: ptr("business English"),
	})
	for _, want := range []string{
		"hubd profile in force: general + work\n",
		"The scope “work” is active",
		"- [Rule, must] Never print a secret value. Names are fine. Values are not. (hubd:claim/never-print-a-secret, general)\n",
		"- [Trap] The shell is bash. (hubd:claim/shell-is-bash, work)\n",
		"follow “work”.\n",
		"Style for answers to the user:\nshort, numbered\n",
		"Voice for text written on the user's behalf:\nbusiness English\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Index(got, "Style for") > strings.Index(got, "Voice for") {
		t.Error("style comes before voice")
	}
}

func TestClientSetsAScopeAndReadsTheProfile(t *testing.T) {
	var posted map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer hubd_x" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "POST /api/estate/scope":
			b, _ := io.ReadAll(r.Body)
			json.Unmarshal(b, &posted)
			if posted["scope"] == "nowhere" {
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, `{"error":"No scope “nowhere”."}`)
				return
			}
			io.WriteString(w, `{"client":"s1","scope":"work"}`)
		case "GET /api/estate/profile":
			if r.URL.Query().Get("session") != "s 1" {
				t.Errorf("session %q", r.URL.Query().Get("session"))
			}
			io.WriteString(w, `{"scopes":["general","work"],"voice":null,"style":"formal","claims":[],"note":null}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := Client{URL: srv.URL + "/", Token: "hubd_x", HTTP: srv.Client()}
	ctx := context.Background()
	if err := c.Set(ctx, "s1", "work", "ws"); err != nil {
		t.Fatal(err)
	}
	if posted["client"] != "s1" || posted["scope"] != "work" || posted["machine"] != "ws" {
		t.Fatalf("posted %v", posted)
	}
	if err := c.Set(ctx, "s1", "", "ws"); err != nil || posted["scope"] != nil {
		t.Fatalf("clearing posted %v, err %v", posted, err)
	}
	if err := c.Set(ctx, "s1", "nowhere", "ws"); err == nil || err.Error() != "No scope “nowhere”." {
		t.Fatalf("err %v", err)
	}
	p, err := c.Profile(ctx, "s 1")
	if err != nil || p.Active() != "work" || p.Style == nil || *p.Style != "formal" {
		t.Fatalf("profile %+v, err %v", p, err)
	}
}

func TestClientSaysWhenHubdHasNoScopes(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	c := Client{URL: srv.URL, HTTP: srv.Client()}
	if _, err := c.Profile(context.Background(), "s1"); !errors.Is(err, ErrAbsent) {
		t.Fatalf("err %v", err)
	}
}
