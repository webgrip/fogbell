package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/webgrip/fogbell/internal/alert"
	"github.com/webgrip/fogbell/internal/config"
	"github.com/webgrip/fogbell/internal/metrics"
	"github.com/webgrip/fogbell/internal/render"
	"github.com/webgrip/fogbell/internal/slack"
	"github.com/webgrip/fogbell/internal/slacktest"
	"github.com/webgrip/fogbell/internal/state"
	"github.com/webgrip/fogbell/internal/upstream"
)

const baseConfig = `
alertmanager:
  url: http://placeholder
objects:
  - name: pod
    labels: [namespace, pod]
    exclude: {pod: '^(kube-state-metrics|node-exporter)-'}
    button: Logs
    url: 'https://grafana.example/explore?pod={{ .Labels.namespace }}/{{ .Labels.pod }}&from={{ .FromMs }}'
  - name: node
    labels: [node]
    button: Node
    url: 'https://grafana.example/d/nodes?var-node={{ .Value | queryescape }}'
links:
  allAlerts: https://grafana.example/d/alerting-overview
  ask: 'https://chat.example/?model=ops&q={{ .Prompt | queryescape }}'
`

// fakeHTTP answers by path and, optionally, the start of the query parameter, and records requests.
type fakeHTTP struct {
	*httptest.Server
	mu       sync.Mutex
	routes   []route
	requests []request
}

type route struct {
	path, query string
	answers     []answer
}

type answer struct {
	body   string
	status int
}

type request struct {
	path  string
	query url.Values
	body  string
}

func newFakeHTTP(t *testing.T) *fakeHTTP {
	f := &fakeHTTP{}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeHTTP) on(path, query string, answers ...answer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes = append(f.routes, route{path, query, answers})
}

func ok(body string) answer { return answer{body: body, status: 200} }

func fail(status int) answer { return answer{status: status} }

func (f *fakeHTTP) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	q := r.URL.Query()
	if r.Method == http.MethodPost && strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		form, _ := url.ParseQuery(string(body))
		for k, v := range form {
			q[k] = v
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, request{r.URL.Path, q, string(body)})
	for i := range f.routes {
		rt := &f.routes[i]
		if r.URL.Path != rt.path || !strings.HasPrefix(q.Get("query"), rt.query) {
			continue
		}
		a := rt.answers[0]
		if len(rt.answers) > 1 {
			rt.answers = rt.answers[1:]
		}
		w.WriteHeader(a.status)
		_, _ = io.WriteString(w, a.body)
		return
	}
	w.WriteHeader(http.StatusTeapot)
}

func (f *fakeHTTP) reqs() []request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]request(nil), f.requests...)
}

// fakeAM is Alertmanager's silence and alert API.
type fakeAM struct {
	*httptest.Server
	mu       sync.Mutex
	created  []upstream.Silence
	expired  []string
	active   map[string]bool
	silences []upstream.Silence
	firing   []upstream.FiringAlert
	down     bool
}

func newFakeAM(t *testing.T) *fakeAM {
	a := &fakeAM{active: map[string]bool{}}
	a.Server = httptest.NewServer(http.HandlerFunc(a.serve))
	t.Cleanup(a.Close)
	return a
}

func (a *fakeAM) serve(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.down {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/v2/silences":
		var s upstream.Silence
		_ = json.NewDecoder(r.Body).Decode(&s)
		a.created = append(a.created, s)
		id := fmt.Sprintf("sid-%d", len(a.created))
		a.active[id] = true
		_ = json.NewEncoder(w).Encode(map[string]string{"silenceID": id})
	case r.Method == http.MethodDelete:
		a.expired = append(a.expired, strings.TrimPrefix(r.URL.Path, "/api/v2/silence/"))
	case r.URL.Path == "/api/v2/silences":
		_ = json.NewEncoder(w).Encode(a.silences)
	case strings.HasPrefix(r.URL.Path, "/api/v2/silence/"):
		state := "expired"
		if a.active[strings.TrimPrefix(r.URL.Path, "/api/v2/silence/")] {
			state = "active"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": map[string]string{"state": state}})
	case r.URL.Path == "/api/v2/alerts":
		_ = json.NewEncoder(w).Encode(a.firing)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (a *fakeAM) matchers(i int) map[string]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := map[string]string{}
	for _, m := range a.created[i].Matchers {
		out[m.Name] = m.Value
	}
	return out
}

type harness struct {
	wired Upstreams
	t     *testing.T
	slack *slacktest.Server
	am    *fakeAM
	up    *fakeHTTP
	e     *Engine
	now   float64
}

const t0 = 1793089000.0

func newHarness(t *testing.T, extra string) *harness {
	t.Helper()
	h := &harness{t: t, slack: slacktest.New(), am: newFakeAM(t), up: newFakeHTTP(t), now: t0}
	h.slack.Channels = map[string]string{"c": "CC", "warn": "CWARN", "hyg": "CHYG", "alerts": "CALERTS",
		"warnings": "CWARNINGS", "teams": "CTEAMS", "team-b-alerts": "CTEAMB", "team-d-alerts": "CTEAMD"}
	t.Cleanup(h.slack.Close)
	if !strings.Contains(extra, "channels:") {
		extra += "channels:\n  alerts: [c]\n"
	}
	cfg, err := config.Parse([]byte(strings.Replace(baseConfig, "http://placeholder", h.am.URL, 1) + extra))
	if err != nil {
		t.Fatal(err)
	}
	h.e = h.engine(cfg)
	return h
}

func (h *harness) engine(cfg *config.Config) *Engine {
	e := New(cfg, h.slack.Client(), &upstream.Alertmanager{URL: h.am.URL, HTTP: h.am.Client()}, metrics.New())
	e.Now = func() time.Time { return time.Unix(0, int64(h.now*1e9)) }
	e.Interactive = func() bool { return true }
	e.Spawn = func(f func()) { f() }
	e.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	e.Slack.Log = e.Log
	return e
}

// restart is a fresh engine on the same Slack, as after a pod restart.
func (h *harness) restart() {
	old := h.e
	h.e = h.engine(old.Config)
	h.e.Tracker, h.e.Investigator, h.e.Roster, h.e.Events = old.Tracker, old.Investigator, old.Roster, old.Events
	h.e.Wire(h.wired)
}

func (h *harness) withMetrics() {
	h.wired.Prom = &upstream.Prometheus{URL: h.up.URL + "/vm", HTTP: h.up.Client()}
	h.wired.Ruler = &upstream.Ruler{URL: h.up.URL + "/vmalert", HTTP: h.up.Client()}
	if host := h.e.Config.Enrich.Ruler.GeneratorHost; host != "" {
		h.wired.Ruler.GeneratorHost = regexp.MustCompile(host)
	}
	h.e.Wire(h.wired)
}

func (h *harness) notify(channel string, g *alert.Group) string {
	h.t.Helper()
	done, err := h.e.Notify(context.Background(), channel, g, false)
	if err != nil {
		h.t.Fatalf("notify: %v", err)
	}
	return done
}

func (h *harness) only() *slacktest.Stored {
	h.t.Helper()
	m, err := h.slack.Only()
	if err != nil {
		h.t.Fatal(err)
	}
	return m
}

func (h *harness) st(m *slacktest.Stored) state.Incident {
	h.t.Helper()
	var st state.Incident
	if !m.Payload(h.e.Config.Slack.Events.Incident, &st) {
		h.t.Fatalf("message %s carries no incident state", m.TS)
	}
	return st
}

func (h *harness) firstTS() string {
	h.t.Helper()
	return h.only().TS
}

func (h *harness) press(actionID, value, ts, user string) string {
	if ts == "" {
		ts = h.firstTS()
	}
	if user == "" {
		user = "U1"
	}
	raw := fmt.Sprintf(`{"channel":{"id":%q},"container":{"message_ts":%q},"user":{"id":%q,"username":"pat"},"actions":[{"action_id":%q`,
		h.channelOf(ts), ts, user, actionID)
	if value != "" {
		raw += fmt.Sprintf(`,"selected_option":{"value":%q}`, value)
	}
	raw += `}]}`
	var p Interaction
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		h.t.Fatal(err)
	}
	return h.e.Interact(context.Background(), &p)
}

func (h *harness) channelOf(ts string) string {
	h.slack.Lock()
	defer h.slack.Unlock()
	if m, ok := h.slack.Messages[ts]; ok {
		return m.Channel
	}
	return ""
}

func blockOf(m *slacktest.Stored, id string) *slack.Block {
	for _, b := range m.Attachments[0].Blocks {
		if b.BlockID == id {
			return &b
		}
	}
	return nil
}

func ids(m *slacktest.Stored) []string {
	var out []string
	if b := blockOf(m, "actions"); b != nil {
		for _, e := range render.RowElements(b.Elements) {
			out = append(out, e.ActionID)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func enriched(m *slacktest.Stored) []string {
	var out []string
	if b := blockOf(m, "enrich"); b != nil {
		for _, e := range b.Elements {
			out = append(out, e.Content)
		}
	}
	return out
}

func textOf(m *slacktest.Stored) string {
	var out []string
	for _, b := range m.Attachments[0].Blocks {
		if b.Text != nil {
			out = append(out, b.Text.Text)
		}
		for _, e := range b.Elements {
			if e.Label != nil {
				out = append(out, e.Label.Text)
			} else {
				out = append(out, e.Content)
			}
		}
	}
	return strings.Join(out, "\n")
}

func group(status string, n int, key, receiver string) *alert.Group {
	if status == "" {
		status = "firing"
	}
	if key == "" {
		key = `{}:{alertname="X"}`
	}
	if receiver == "" {
		receiver = "warnings"
	}
	g := &alert.Group{GroupKey: key, Receiver: receiver, Status: status,
		GroupLabels:       map[string]string{"alertname": "X", "namespace": "ns"},
		CommonLabels:      map[string]string{"alertname": "X", "severity": "warning", "namespace": "ns"},
		CommonAnnotations: map[string]string{"summary": "A thing broke", "action": "Fix it."}}
	for i := 0; i < n; i++ {
		end := "0001-01-01T00:00:00Z"
		if status == "resolved" {
			end = "2026-10-27T08:40:00Z"
		}
		g.Alerts = append(g.Alerts, alert.Alert{Status: status, StartsAt: "2026-10-27T08:15:00Z", EndsAt: end,
			Labels: map[string]string{"alertname": "X", "severity": "warning", "namespace": "ns", "pod": fmt.Sprintf("p%d", i)},
			Annotations: map[string]string{"summary": "A thing broke", "description": "It broke.", "impact": "Nobody can work.",
				"action": "Fix it.", "runbook_url": "https://r", "dashboard_url": "https://d"}})
	}
	return g
}

func firing() *alert.Group { return group("", 1, "", "") }

func counter(e *Engine, vec string, labels ...string) float64 {
	m := e.Metrics
	switch vec {
	case "enrichment":
		return testutil.ToFloat64(m.Enrichment.WithLabelValues(labels...))
	case "degraded":
		return testutil.ToFloat64(m.Degraded.WithLabelValues(labels...))
	case "incidents":
		return testutil.ToFloat64(m.Incidents.WithLabelValues(labels...))
	case "noise":
		return testutil.ToFloat64(m.Noise.WithLabelValues(labels...))
	}
	panic(vec)
}
