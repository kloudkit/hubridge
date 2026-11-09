package install

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/tidwall/gjson"
)

type Wanted struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Core    bool   `json:"core"`
	Enabled bool   `json:"enabled"`
}

type Installed struct {
	Name    string  `json:"name"`
	Version *string `json:"version"`
	Enabled bool    `json:"enabled"`
}

// Reconcile makes Claude Code's plugins from hubd's marketplace match what hubd wants for this machine, then reports what the machine has.
func Reconcile(ctx context.Context, hc *http.Client, base, machine, token string, out io.Writer) error {
	wanted, err := fetchWanted(ctx, hc, base, machine, token)
	if err != nil {
		return err
	}
	have, err := installedFromMarketplace(ctx)
	if err != nil {
		return err
	}
	var errs []string
	for _, w := range wanted {
		cur, ok := have[w.Name]
		id := w.Name + "@" + Marketplace
		switch {
		case w.Enabled && !ok:
			if err := installID(ctx, id, out); err != nil {
				errs = append(errs, err.Error())
			}
		case w.Enabled && ok:
			if !cur.Enabled {
				if b, err := claudePlugin(ctx, "enable", id, "--scope", "user", "--json"); err != nil && !lastJSON(b).AlreadyInGoalState {
					errs = append(errs, fmt.Sprintf("enable %s: %s", id, firstLine(lastJSON(b).Message, b)))
				}
			}
			if cur.Version == nil || *cur.Version != w.Version {
				if b, err := claudePlugin(ctx, "update", id, "--yes", "--json"); err != nil && !lastJSON(b).AlreadyInGoalState {
					errs = append(errs, fmt.Sprintf("update %s: %s", id, firstLine(lastJSON(b).Message, b)))
				}
			}
		case !w.Enabled && ok && cur.Enabled:
			if b, err := claudePlugin(ctx, "disable", id, "--scope", "user", "--json"); err != nil && !lastJSON(b).AlreadyInGoalState {
				errs = append(errs, fmt.Sprintf("disable %s: %s", id, firstLine(lastJSON(b).Message, b)))
			}
		}
	}
	have, err = installedFromMarketplace(ctx)
	if err != nil {
		return err
	}
	list := make([]Installed, 0, len(have))
	for _, h := range have {
		list = append(list, h)
	}
	if err := report(ctx, hc, base, machine, token, list); err != nil {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		return fmt.Errorf("plugins: %s", strings.Join(errs, "; "))
	}
	return nil
}

func installID(ctx context.Context, id string, out io.Writer) error {
	b, err := claudePlugin(ctx, "install", id, "--scope", "user", "--yes", "--json")
	r := lastJSON(b)
	if err != nil && !r.AlreadyInGoalState {
		return fmt.Errorf("install %s: %s", id, firstLine(r.Message, b))
	}
	fmt.Fprintf(out, "installed %s\n", id)
	return nil
}

func fetchWanted(ctx context.Context, hc *http.Client, base, machine, token string) ([]Wanted, error) {
	u := strings.TrimRight(base, "/") + "/api/client/plugins?" + url.Values{"machine": {machine}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", u, resp.StatusCode)
	}
	var ans struct {
		Plugins []Wanted `json:"plugins"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&ans); err != nil {
		return nil, fmt.Errorf("%s: %w", u, err)
	}
	return ans.Plugins, nil
}

func report(ctx context.Context, hc *http.Client, base, machine, token string, list []Installed) error {
	body, _ := json.Marshal(map[string]any{"machine": machine, "plugins": list})
	u := strings.TrimRight(base, "/") + "/api/client/plugins"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("report plugins: HTTP %d", resp.StatusCode)
	}
	return nil
}

// installedFromMarketplace lists the plugins Claude Code has from hubd's marketplace, by plugin name.
func installedFromMarketplace(ctx context.Context) (map[string]Installed, error) {
	out, err := claudePlugin(ctx, "list", "--json")
	if err != nil {
		return nil, fmt.Errorf("claude plugin list: %v: %s", err, strings.TrimSpace(string(out)))
	}
	doc := bytes.TrimSpace(out)
	if i, j := bytes.IndexByte(doc, '['), bytes.LastIndexByte(doc, ']'); i >= 0 && j > i {
		doc = doc[i : j+1]
	}
	if !gjson.ValidBytes(doc) {
		return nil, fmt.Errorf("claude plugin list --json: not JSON: %s", firstLine("", out))
	}
	have := map[string]Installed{}
	gjson.ParseBytes(doc).ForEach(func(_, v gjson.Result) bool {
		id := v.Get("id").String()
		name, ok := strings.CutSuffix(id, "@"+Marketplace)
		if !ok {
			return true
		}
		var ver *string
		if s := v.Get("version").String(); s != "" {
			ver = &s
		}
		have[name] = Installed{Name: name, Version: ver, Enabled: v.Get("enabled").Bool()}
		return true
	})
	return have, nil
}
