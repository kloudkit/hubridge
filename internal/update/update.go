package update

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/minio/selfupdate"
	"golang.org/x/mod/semver"

	"github.com/kloudkit/hubridge/internal/home"
	"github.com/kloudkit/hubridge/internal/install"
)

const Releases = "https://github.com/kloudkit/hubridge/releases"

var PublicKey = "PUBLIC_KEY_PLACEHOLDER"

type Manifest struct {
	Version  string            `json:"version"`
	Protocol int               `json:"protocol"`
	Assets   map[string]string `json:"assets"`
}

type Updater struct {
	Home     *home.Home
	Current  string
	Key      ed25519.PublicKey
	Releases string
	HTTP     *http.Client
	OS, Arch string
}

type Result struct {
	From, To  string
	Changed   bool
	Downgrade bool
}

func New(h *home.Home, current string) (*Updater, error) {
	key, err := base64.StdEncoding.DecodeString(PublicKey)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, errors.New("this build has no release key")
	}
	return &Updater{
		Home: h, Current: current, Key: key, Releases: Releases,
		HTTP: &http.Client{Timeout: 5 * time.Minute}, OS: runtime.GOOS, Arch: runtime.GOARCH,
	}, nil
}

func ValidChannel(c string) bool {
	return c == "bundled" || c == "latest" || semver.IsValid(c)
}

func (u *Updater) Asset() string { return "hubridge-" + u.OS + "-" + u.Arch }

func (u *Updater) Run(ctx context.Context, channel string, explicit bool) (Result, error) {
	res := Result{From: u.Current}
	if !ValidChannel(channel) {
		return res, fmt.Errorf("unknown version %q: use bundled, latest or a release tag such as v0.1.0", channel)
	}
	cfg, err := u.Home.Config()
	if err != nil {
		return res, err
	}
	m, err := u.manifest(ctx, u.manifestURL(cfg.URL, channel))
	if err != nil {
		return res, err
	}
	res.To = m.Version
	speaks, err := u.hubdProtocol(ctx, cfg.URL)
	if err != nil {
		return res, err
	}
	if m.Protocol != speaks {
		return res, fmt.Errorf("%s speaks protocol %d; this hubd speaks %d: use --version bundled or update hubd", m.Version, m.Protocol, speaks)
	}
	if m.Version == u.Current {
		return res, nil
	}
	res.Downgrade = semver.Compare(m.Version, u.Current) < 0
	if res.Downgrade && channel == "latest" && !explicit {
		return res, nil
	}
	want, ok := m.Assets[u.Asset()]
	if !ok {
		return res, fmt.Errorf("%s has no build for %s/%s", m.Version, u.OS, u.Arch)
	}
	if err := u.install(ctx, u.assetURL(cfg.URL, channel, m.Version), want); err != nil {
		return res, err
	}
	res.Changed = true
	return res, nil
}

func (u *Updater) manifestURL(hubd, channel string) string {
	switch channel {
	case "bundled":
		return hubd + "/install/manifest.json"
	case "latest":
		return u.Releases + "/latest/download/manifest.json"
	}
	return u.Releases + "/download/" + channel + "/manifest.json"
}

func (u *Updater) assetURL(hubd, channel, version string) string {
	if channel == "bundled" {
		return hubd + "/install/files/" + u.Asset()
	}
	return u.Releases + "/download/" + version + "/" + u.Asset()
}

func (u *Updater) manifest(ctx context.Context, url string) (Manifest, error) {
	var m Manifest
	body, err := u.get(ctx, url, 1<<20)
	if err != nil {
		return m, err
	}
	sig, err := u.get(ctx, url+".sig", 1<<10)
	if err != nil {
		return m, err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || !ed25519.Verify(u.Key, body, raw) {
		return m, fmt.Errorf("%s: the signature does not verify", url)
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return m, fmt.Errorf("%s: %w", url, err)
	}
	if !semver.IsValid(m.Version) {
		return m, fmt.Errorf("%s: %q is not a release version", url, m.Version)
	}
	return m, nil
}

func (u *Updater) hubdProtocol(ctx context.Context, hubd string) (int, error) {
	token, err := u.Home.Token()
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, hubd+"/api/client/hello", nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := u.HTTP.Do(req)
	if err != nil {
		return 0, fmt.Errorf("reach hubd: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("hubd hello: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Protocol int `json:"protocol"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out); err != nil {
		return 0, fmt.Errorf("hubd hello: %w", err)
	}
	return out.Protocol, nil
}

func (u *Updater) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := u.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

func (u *Updater) install(ctx context.Context, url, want string) error {
	sum, err := hex.DecodeString(want)
	if err != nil {
		return fmt.Errorf("%s: the manifest has a malformed checksum", url)
	}
	body, err := u.get(ctx, url, 1<<28)
	if err != nil {
		return err
	}
	bin := install.Binary(u.Home.Dir)
	if err := selfupdate.Apply(bytes.NewReader(body), selfupdate.Options{TargetPath: bin, TargetMode: 0o700, Checksum: sum, OldSavePath: bin + ".prev"}); err != nil {
		return fmt.Errorf("%s: %w", url, err)
	}
	return nil
}

func (u *Updater) Watch(ctx context.Context, channel string, every time.Duration, kick <-chan struct{}, log *slog.Logger) {
	if channel != "bundled" && channel != "latest" {
		return
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		res, err := u.Run(ctx, channel, false)
		switch {
		case err != nil:
			log.Warn("auto-update failed; retrying later", "channel", channel, "err", err)
		case res.Changed:
			log.Info("auto-update installed a new binary", "from", res.From, "to", res.To, "channel", channel)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-kick:
		}
	}
}
