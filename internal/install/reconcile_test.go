package install

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeClaudeFleet(t *testing.T, installed string) string {
	dir := t.TempDir()
	log := filepath.Join(dir, "argv")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\037' \"$a\" >> '" + log + "'; done\necho >> '" + log + "'\n" +
		"if [ \"$1\" = plugin ] && [ \"$2\" = list ]; then printf '%b\\n' '" + installed + "'; exit 0; fi\n" +
		"if [ \"$1\" = plugin ]; then echo '{\"outcome\":\"success\"}'; exit 0; fi\n"
	os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o700)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func TestReconcileInstallsEnablesDisablesAndReports(t *testing.T) {
	var reported map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/client/plugins":
			if r.URL.Query().Get("machine") != "ws" {
				t.Errorf("machine %q", r.URL.Query().Get("machine"))
			}
			io.WriteString(w, `{"plugins":[{"name":"hubd","version":"0.4.0","core":true,"enabled":true},{"name":"renovate","version":"1.2.0","core":false,"enabled":true},{"name":"print","version":"0.4.0","core":false,"enabled":false},{"name":"cloudflare","version":"0.9.3","core":false,"enabled":true}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/client/plugins":
			b, _ := io.ReadAll(r.Body)
			json.Unmarshal(b, &reported)
			io.WriteString(w, `{"ok":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	have := `[{"id":"hubd@hubd","version":"0.4.0","enabled":true},{"id":"print@hubd","version":"0.4.0","enabled":true},{"id":"cloudflare@hubd","version":"0.9.2","enabled":false},{"id":"gopls-lsp@claude-plugins-official","version":"1.0.0","enabled":true}]`
	log := fakeClaudeFleet(t, have)
	var out bytes.Buffer
	if err := Reconcile(context.Background(), srv.Client(), srv.URL, "ws", "hubd_tok", &out); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer hubd_tok" {
		t.Fatalf("auth %q", auth)
	}
	var acts []string
	for _, c := range calls(t, log) {
		if len(c) > 2 && c[1] != "list" {
			acts = append(acts, c[1]+" "+c[2])
		}
	}
	want := []string{"install renovate@hubd", "disable print@hubd", "enable cloudflare@hubd", "update cloudflare@hubd"}
	if strings.Join(acts, ",") != strings.Join(want, ",") {
		t.Fatalf("actions %q, want %q", acts, want)
	}
	if !strings.Contains(out.String(), "installed renovate@hubd") {
		t.Fatalf("out %q", out.String())
	}
	plugins, _ := reported["plugins"].([]any)
	if reported["machine"] != "ws" || len(plugins) != 3 {
		t.Fatalf("reported %v", reported)
	}
	for _, p := range plugins {
		if p.(map[string]any)["name"] == "gopls-lsp" {
			t.Fatal("another marketplace's plugin was reported")
		}
	}
}

func TestReconcileNeedsHubd(t *testing.T) {
	fakeClaudeFleet(t, "[]")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
	defer srv.Close()
	if err := Reconcile(context.Background(), srv.Client(), srv.URL, "ws", "t", io.Discard); err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("got %v", err)
	}
}
