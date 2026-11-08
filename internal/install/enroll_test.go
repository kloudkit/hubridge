package install

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kloudkit/hubridge/internal/home"
)

var (
	issued = "hubd_" + strings.Repeat("c", 43)
	other  = "hubd_" + strings.Repeat("d", 43)
)

func fakeHubd(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/client/enroll":
			var b struct{ Code string }
			json.NewDecoder(r.Body).Decode(&b)
			if b.Code != "good" {
				w.WriteHeader(401)
				w.Write([]byte(`{"error":"This install code is unknown, used or expired."}`))
				return
			}
			w.Write([]byte(`{"token":"` + issued + `","name":"laptop"}`))
		case "/api/client/hello":
			if r.Header.Get("Authorization") != "Bearer "+issued {
				w.WriteHeader(401)
				return
			}
			w.Write([]byte(`{"protocol":1}`))
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestEnroll(t *testing.T) {
	ts := fakeHubd(t)
	defer ts.Close()
	h, _ := home.Open(filepath.Join(t.TempDir(), "home"))

	err := Enroll(h, EnrollOptions{URL: ts.URL, Code: "stale"})
	if err == nil || !strings.Contains(err.Error(), "unknown, used or expired") {
		t.Fatalf("bad code: %v", err)
	}
	if _, err := h.Token(); err == nil {
		t.Fatal("a token was written for a refused code")
	}

	if err := Enroll(h, EnrollOptions{URL: ts.URL + "/", Code: "good"}); err != nil {
		t.Fatal(err)
	}
	if tok, _ := h.Token(); tok != issued {
		t.Fatal("token not written")
	}
	cfg, _ := h.Config()
	if cfg.URL != ts.URL || cfg.Machine != "laptop" {
		t.Fatalf("config %+v", cfg)
	}
	for _, f := range []string{"token", "config.json"} {
		if st, _ := os.Stat(filepath.Join(h.Dir, f)); st.Mode().Perm() != 0o600 {
			t.Errorf("%s is %o", f, st.Mode().Perm())
		}
	}

	if err := Enroll(h, EnrollOptions{Machine: "ws"}); err != nil {
		t.Fatalf("rerun with the installed token: %v", err)
	}
	if cfg, _ := h.Config(); cfg.Machine != "ws" || cfg.URL != ts.URL {
		t.Fatalf("config after rerun %+v", cfg)
	}

	tf := filepath.Join(t.TempDir(), "tok")
	os.WriteFile(tf, []byte(other+"\n"), 0o600)
	err = Enroll(h, EnrollOptions{TokenFile: tf})
	if err == nil || !strings.Contains(err.Error(), "kept") {
		t.Fatalf("rejected token file: %v", err)
	}
	if tok, _ := h.Token(); tok != issued {
		t.Fatal("a working token was replaced by a rejected one")
	}

	os.WriteFile(filepath.Join(h.Dir, "token"), []byte(other+"\n"), 0o600)
	if err := Enroll(h, EnrollOptions{}); err == nil {
		t.Fatal("a broken installed token passed")
	}
}
