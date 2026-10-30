package investigate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// Ploeg admits investigations as manual-origin Work Items through Ploeg's operator API
// (POST /api/v1/operator/work-items) and reads their Runs from the Work Item's facts.
type Ploeg struct {
	URL       string
	TokenFile string
	Team      string
	Target    string
	BudgetUSD float64
	Model     string
	// Console is the base of a person-facing link to a Work Item, such as an Unfold URL; optional.
	Console string
	HTTP    *http.Client
}

// Name implements Investigator.
func (*Ploeg) Name() string { return "Ploeg" }

// Available implements Investigator: a token file with a token in it.
func (p *Ploeg) Available() bool { return p != nil && p.token() != "" }

func (p *Ploeg) token() string {
	b, err := os.ReadFile(p.TokenFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (p *Ploeg) do(ctx context.Context, method, path string, in, out any) (int, error) {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(p.URL, "/")+path, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+p.token())
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode == http.StatusPaymentRequired || resp.StatusCode == http.StatusConflict {
		return resp.StatusCode, fmt.Errorf("%w: Ploeg answered %d", ErrRefused, resp.StatusCode)
	}
	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("ploeg %s %s: HTTP %d", method, path, resp.StatusCode)
	}
	if out != nil {
		return resp.StatusCode, json.Unmarshal(raw, out)
	}
	return resp.StatusCode, nil
}

type admission struct {
	RequestID   string  `json:"requestId"`
	Team        string  `json:"team"`
	Target      string  `json:"target"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	BudgetUSD   float64 `json:"budgetUsd,omitempty"`
	Model       string  `json:"model,omitempty"`
}

// Start implements Investigator. The request id is the idempotency key, so a press repeated after a
// timeout admits nothing twice.
func (p *Ploeg) Start(ctx context.Context, r Request) (string, error) {
	var out struct {
		WorkItem struct {
			ID string `json:"id"`
		} `json:"workItem"`
	}
	_, err := p.do(ctx, http.MethodPost, "/api/v1/operator/work-items", admission{
		RequestID: requestID(r.ID), Team: p.Team, Target: p.Target, Title: cut(r.Title, 200),
		Description: cut(r.Description, 100000), BudgetUSD: p.BudgetUSD, Model: p.Model,
	}, &out)
	if err != nil {
		return "", err
	}
	if out.WorkItem.ID == "" {
		return "", fmt.Errorf("ploeg admitted no work item")
	}
	return out.WorkItem.ID, nil
}

var finished = map[string]bool{"done": true, "needs_human": true, "awaiting_review": true, "stale": true, "withdrawn": true}

// Status implements Investigator: done once the Work Item left the queue for good, with the findings
// of its last Run that reported any.
func (p *Ploeg) Status(ctx context.Context, id string) (Status, error) {
	var facts struct {
		WorkItem struct {
			State string `json:"state"`
			URL   string `json:"url"`
		} `json:"workItem"`
		Runs []struct {
			Role     string `json:"role"`
			Outcome  string `json:"outcome"`
			Summary  string `json:"summary"`
			Problem  string `json:"problem"`
			Solution string `json:"solution"`
			Verdict  string `json:"verdict"`
		} `json:"runs"`
	}
	if _, err := p.do(ctx, http.MethodGet, "/api/v1/operator/work-items/"+url.PathEscape(id)+"/facts", nil, &facts); err != nil {
		return Status{}, err
	}
	s := Status{State: facts.WorkItem.State, Done: finished[facts.WorkItem.State], URL: facts.WorkItem.URL}
	if p.Console != "" {
		s.URL = strings.TrimRight(p.Console, "/") + "/" + url.PathEscape(id)
	}
	for i := len(facts.Runs) - 1; i >= 0; i-- {
		r := facts.Runs[i]
		var parts []string
		for _, f := range []struct{ label, text string }{{"", r.Summary}, {"*Cause:* ", r.Problem}, {"*Fix:* ", r.Solution}} {
			if strings.TrimSpace(f.text) != "" {
				parts = append(parts, f.label+strings.TrimSpace(f.text))
			}
		}
		if len(parts) > 0 {
			s.Findings = strings.Join(parts, "\n")
			break
		}
	}
	return s, nil
}

func requestID(id string) string {
	var b strings.Builder
	for _, r := range "fogbell:" + id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == ':', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return cut(b.String(), 128)
}

func cut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
