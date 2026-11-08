package install

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kloudkit/hubridge/internal/home"
)

type EnrollOptions struct {
	URL       string
	Code      string
	TokenFile string
	Machine   string
}

var httpClient = &http.Client{Timeout: 15 * time.Second}

func Enroll(h *home.Home, o EnrollOptions) error {
	cfg, _ := h.Config()
	if o.URL != "" {
		cfg.URL = strings.TrimRight(o.URL, "/")
	}
	if o.Machine != "" {
		cfg.Machine = o.Machine
	}
	if cfg.Channel == "" {
		cfg.Channel = "bundled"
	}
	if cfg.URL == "" {
		return errors.New("no hubd url: pass --url")
	}
	var token string
	switch {
	case o.Code != "":
		t, name, err := redeem(cfg.URL, o.Code)
		if err != nil {
			return err
		}
		token = t
		if cfg.Machine == "" {
			cfg.Machine = name
		}
	case o.TokenFile != "":
		b, err := os.ReadFile(o.TokenFile)
		if err != nil {
			return fmt.Errorf("read token file: %w", err)
		}
		token = strings.TrimSpace(string(b))
		if err := checkToken(cfg.URL, token); err != nil {
			if old, oerr := h.Token(); oerr == nil && checkToken(cfg.URL, old) == nil {
				return fmt.Errorf("hubd rejects the given token (%v); the installed token still works and was kept", err)
			}
			return fmt.Errorf("hubd rejects the given token: %w", err)
		}
	default:
		old, err := h.Token()
		if err != nil {
			return errors.New("no token installed: pass --code with an install code from Settings › Access")
		}
		if err := checkToken(cfg.URL, old); err != nil {
			return fmt.Errorf("the installed token does not work (%v): get a new install code from Settings › Access", err)
		}
	}
	if cfg.Machine == "" {
		if hn, err := os.Hostname(); err == nil {
			cfg.Machine = hn
		}
	}
	if cfg.Machine == "" {
		return errors.New("no machine name: pass --machine")
	}
	if token != "" {
		if err := h.WriteToken(token); err != nil {
			return fmt.Errorf("write token: %w", err)
		}
	}
	return h.WriteConfig(cfg)
}

func redeem(url, code string) (string, string, error) {
	body, _ := json.Marshal(map[string]string{"code": code})
	resp, err := httpClient.Post(url+"/api/client/enroll", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", "", fmt.Errorf("reach hubd: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	var out struct {
		Token string `json:"token"`
		Name  string `json:"name"`
		Error string `json:"error"`
	}
	json.Unmarshal(b, &out)
	if resp.StatusCode != http.StatusOK {
		if out.Error != "" {
			return "", "", fmt.Errorf("hubd refused the code: %s", out.Error)
		}
		return "", "", fmt.Errorf("hubd refused the code: HTTP %d", resp.StatusCode)
	}
	if !strings.HasPrefix(out.Token, "hubd_") {
		return "", "", errors.New("hubd answered without a token")
	}
	return out.Token, out.Name, nil
}

func checkToken(url, token string) error {
	_, err := Get(context.Background(), httpClient, url+"/api/client/hello", token, 1<<16)
	return err
}

func Get(ctx context.Context, c *http.Client, url, token string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err := errors.Join(err, resp.Body.Close()); err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return b, nil
}
