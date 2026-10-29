// Package upstream holds the clients for the services fogbell reads and writes besides Slack:
// Alertmanager, a Prometheus-compatible query API, the ruler, VictoriaLogs, the Kubernetes API and
// ClickUp. Each is plain HTTP and JSON.
package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// StatusError is an answer outside 2xx.
type StatusError struct {
	URL  string
	Code int
}

func (e *StatusError) Error() string { return fmt.Sprintf("%s: HTTP %d", e.URL, e.Code) }

func do(ctx context.Context, c *http.Client, method, target string, headers map[string]string, body []byte, out any) error {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, r)
	if err != nil {
		return err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &StatusError{URL: redactURL(target), Code: resp.StatusCode}
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func redactURL(target string) string {
	u, err := url.Parse(target)
	if err != nil {
		return "request"
	}
	u.RawQuery = ""
	u.User = nil
	return u.String()
}

func join(base, path string) string { return strings.TrimRight(base, "/") + path }

// Alertmanager is the Alertmanager v2 API.
type Alertmanager struct {
	URL  string
	HTTP *http.Client
}

// Matcher is a silence matcher.
type Matcher struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	IsRegex bool   `json:"isRegex"`
	IsEqual bool   `json:"isEqual"`
}

// Silence is a silence as the API lists it.
type Silence struct {
	ID        string    `json:"id"`
	Matchers  []Matcher `json:"matchers"`
	StartsAt  string    `json:"startsAt"`
	EndsAt    string    `json:"endsAt"`
	CreatedBy string    `json:"createdBy"`
	Comment   string    `json:"comment"`
	Status    struct {
		State string `json:"state"`
	} `json:"status"`
}

// CreateSilence posts a silence and returns its id.
func (a *Alertmanager) CreateSilence(ctx context.Context, s Silence) (string, error) {
	body, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	var out struct {
		ID string `json:"silenceID"`
	}
	err = do(ctx, a.HTTP, http.MethodPost, join(a.URL, "/api/v2/silences"), map[string]string{"Content-Type": "application/json"}, body, &out)
	return out.ID, err
}

// ExpireSilence ends a silence.
func (a *Alertmanager) ExpireSilence(ctx context.Context, id string) error {
	return do(ctx, a.HTTP, http.MethodDelete, join(a.URL, "/api/v2/silence/"+url.PathEscape(id)), nil, nil, nil)
}

// SilenceActive reports whether a silence is active.
func (a *Alertmanager) SilenceActive(ctx context.Context, id string) (bool, error) {
	var s Silence
	if err := do(ctx, a.HTTP, http.MethodGet, join(a.URL, "/api/v2/silence/"+url.PathEscape(id)), nil, nil, &s); err != nil {
		return false, err
	}
	return s.Status.State == "active", nil
}

// Silences lists every silence.
func (a *Alertmanager) Silences(ctx context.Context) ([]Silence, error) {
	var out []Silence
	err := do(ctx, a.HTTP, http.MethodGet, join(a.URL, "/api/v2/silences"), nil, nil, &out)
	return out, err
}

// FiringAlert is an alert as the alerts API lists it.
type FiringAlert struct {
	Fingerprint string            `json:"fingerprint"`
	Labels      map[string]string `json:"labels"`
}

// Firing lists the active alerts that are neither silenced nor inhibited.
func (a *Alertmanager) Firing(ctx context.Context) ([]FiringAlert, error) {
	var out []FiringAlert
	err := do(ctx, a.HTTP, http.MethodGet, join(a.URL, "/api/v2/alerts?active=true&silenced=false&inhibited=false"), nil, nil, &out)
	return out, err
}
