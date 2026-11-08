package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kloudkit/hubridge/internal/home"
	"github.com/kloudkit/hubridge/internal/install"
)

type release struct {
	version  string
	protocol int
	binary   string
}

type fixture struct {
	t        *testing.T
	srv      *httptest.Server
	key      ed25519.PrivateKey
	files    map[string][]byte
	protocol int
	u        *Updater
}

func newFixture(t *testing.T, current string) *fixture {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	fx := &fixture{t: t, key: priv, files: map[string][]byte{}, protocol: 1}
	fx.srv = httptest.NewServer(http.HandlerFunc(fx.serve))
	t.Cleanup(fx.srv.Close)
	h, err := home.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := h.WriteConfig(home.Config{URL: fx.srv.URL, Machine: "ws", Channel: "bundled"}); err != nil {
		t.Fatal(err)
	}
	if err := h.WriteToken("hubd_test"); err != nil {
		t.Fatal(err)
	}
	bin := install.Binary(h.Dir)
	os.MkdirAll(filepath.Dir(bin), 0o700)
	os.WriteFile(bin, []byte("current"), 0o700)
	fx.u = &Updater{Home: h, Current: current, Key: pub, Releases: fx.srv.URL + "/releases", HTTP: fx.srv.Client(), OS: "linux", Arch: "amd64"}
	return fx
}

func (fx *fixture) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/api/client/hello":
		if r.Header.Get("Authorization") != "Bearer hubd_test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]int{"protocol": fx.protocol})
	case strings.HasPrefix(r.URL.Path, "/releases/latest/download/"):
		http.Redirect(w, r, "/releases/download/"+string(fx.files["latest"])+"/"+strings.TrimPrefix(r.URL.Path, "/releases/latest/download/"), http.StatusFound)
	default:
		b, ok := fx.files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}
}

func (fx *fixture) publish(prefix string, rel release) {
	sum := sha256.Sum256([]byte(rel.binary))
	m, _ := json.Marshal(Manifest{Version: rel.version, Protocol: rel.protocol, Assets: map[string]string{"hubridge-linux-amd64": hex.EncodeToString(sum[:])}})
	fx.files[prefix+"/manifest.json"] = m
	fx.files[prefix+"/manifest.json.sig"] = []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(fx.key, m)) + "\n")
	fx.files[prefix+"/hubridge-linux-amd64"] = []byte(rel.binary)
}

func (fx *fixture) bundle(rel release) {
	fx.publish("/install", rel)
	fx.files["/install/files/hubridge-linux-amd64"] = fx.files["/install/hubridge-linux-amd64"]
}

func (fx *fixture) github(rel release, latest bool) {
	fx.publish("/releases/download/"+rel.version, rel)
	if latest {
		fx.files["latest"] = []byte(rel.version)
	}
}

func (fx *fixture) binary() string {
	b, _ := os.ReadFile(install.Binary(fx.u.Home.Dir))
	return string(b)
}

func (fx *fixture) prev() string {
	b, _ := os.ReadFile(install.Binary(fx.u.Home.Dir) + ".prev")
	return string(b)
}

func TestBundledUpgradeSwapsTheBinary(t *testing.T) {
	fx := newFixture(t, "v0.1.0")
	fx.bundle(release{"v0.2.0", 1, "v0.2.0 build"})
	res, err := fx.u.Run(context.Background(), "bundled", false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || res.Downgrade || res.To != "v0.2.0" {
		t.Fatalf("result %+v", res)
	}
	if fx.binary() != "v0.2.0 build" || fx.prev() != "current" {
		t.Fatalf("binary %q, prev %q", fx.binary(), fx.prev())
	}
}

func TestBundledFollowsHubdDown(t *testing.T) {
	fx := newFixture(t, "v0.3.0")
	fx.bundle(release{"v0.2.0", 1, "v0.2.0 build"})
	res, err := fx.u.Run(context.Background(), "bundled", false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || !res.Downgrade || fx.binary() != "v0.2.0 build" {
		t.Fatalf("result %+v, binary %q", res, fx.binary())
	}
}

func TestSameVersionChangesNothing(t *testing.T) {
	fx := newFixture(t, "v0.2.0")
	fx.bundle(release{"v0.2.0", 1, "v0.2.0 build"})
	res, err := fx.u.Run(context.Background(), "bundled", true)
	if err != nil || res.Changed || fx.binary() != "current" {
		t.Fatalf("result %+v, err %v, binary %q", res, err, fx.binary())
	}
}

func TestLatestFollowsTheRedirect(t *testing.T) {
	fx := newFixture(t, "v0.1.0")
	fx.github(release{"v0.4.0", 1, "v0.4.0 build"}, true)
	res, err := fx.u.Run(context.Background(), "latest", false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || res.To != "v0.4.0" || fx.binary() != "v0.4.0 build" {
		t.Fatalf("result %+v, binary %q", res, fx.binary())
	}
}

func TestLatestNeverGoesDownOnItsOwn(t *testing.T) {
	fx := newFixture(t, "v0.5.0")
	fx.github(release{"v0.4.0", 1, "v0.4.0 build"}, true)
	res, err := fx.u.Run(context.Background(), "latest", false)
	if err != nil || res.Changed || fx.binary() != "current" {
		t.Fatalf("auto: result %+v, err %v, binary %q", res, err, fx.binary())
	}
	res, err = fx.u.Run(context.Background(), "latest", true)
	if err != nil || !res.Changed || !res.Downgrade || fx.binary() != "v0.4.0 build" {
		t.Fatalf("explicit: result %+v, err %v, binary %q", res, err, fx.binary())
	}
}

func TestPinnedTag(t *testing.T) {
	fx := newFixture(t, "v0.4.0")
	fx.github(release{"v0.3.0", 1, "v0.3.0 build"}, false)
	res, err := fx.u.Run(context.Background(), "v0.3.0", true)
	if err != nil || !res.Changed || !res.Downgrade || fx.binary() != "v0.3.0 build" {
		t.Fatalf("result %+v, err %v, binary %q", res, err, fx.binary())
	}
}

func TestRefusesAProtocolHubdDoesNotSpeak(t *testing.T) {
	fx := newFixture(t, "v0.1.0")
	fx.github(release{"v1.0.0", 2, "v1.0.0 build"}, true)
	_, err := fx.u.Run(context.Background(), "latest", true)
	if err == nil || !strings.Contains(err.Error(), "v1.0.0 speaks protocol 2; this hubd speaks 1") {
		t.Fatalf("err %v", err)
	}
	if fx.binary() != "current" {
		t.Fatalf("binary %q", fx.binary())
	}
}

func TestRefusesABadSignature(t *testing.T) {
	fx := newFixture(t, "v0.1.0")
	fx.bundle(release{"v0.2.0", 1, "v0.2.0 build"})
	_, other, _ := ed25519.GenerateKey(nil)
	fx.files["/install/manifest.json.sig"] = []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(other, fx.files["/install/manifest.json"])))
	_, err := fx.u.Run(context.Background(), "bundled", true)
	if err == nil || !strings.Contains(err.Error(), "signature does not verify") || fx.binary() != "current" {
		t.Fatalf("err %v, binary %q", err, fx.binary())
	}
}

func TestRefusesAChecksumMismatch(t *testing.T) {
	fx := newFixture(t, "v0.1.0")
	fx.bundle(release{"v0.2.0", 1, "v0.2.0 build"})
	fx.files["/install/files/hubridge-linux-amd64"] = []byte("tampered")
	_, err := fx.u.Run(context.Background(), "bundled", true)
	if err == nil || !strings.Contains(err.Error(), "wrong checksum") || fx.binary() != "current" {
		t.Fatalf("err %v, binary %q", err, fx.binary())
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(install.Binary(fx.u.Home.Dir)), ".hubridge.new")); !os.IsNotExist(err) {
		t.Fatal("the download was left behind")
	}
}

func TestRejectsAnUnknownChannel(t *testing.T) {
	fx := newFixture(t, "v0.1.0")
	if _, err := fx.u.Run(context.Background(), "stable", true); err == nil {
		t.Fatal("stable was accepted")
	}
}

func TestDevBuildUpgrades(t *testing.T) {
	fx := newFixture(t, "dev")
	fx.bundle(release{"v0.2.0", 1, "v0.2.0 build"})
	res, err := fx.u.Run(context.Background(), "bundled", true)
	if err != nil || !res.Changed || res.Downgrade {
		t.Fatalf("result %+v, err %v", res, err)
	}
}
