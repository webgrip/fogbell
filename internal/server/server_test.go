package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/webgrip/fogbell/internal/alert"
	"github.com/webgrip/fogbell/internal/config"
	"github.com/webgrip/fogbell/internal/engine"
	"github.com/webgrip/fogbell/internal/metrics"
	"github.com/webgrip/fogbell/internal/slack"
	"github.com/webgrip/fogbell/internal/slacktest"
	"github.com/webgrip/fogbell/internal/upstream"
)

type fixture struct {
	srv      *httptest.Server
	slack    *slacktest.Server
	silences []upstream.Silence
}

func setup(t *testing.T) *fixture {
	f := &fixture{slack: slacktest.New()}
	t.Cleanup(f.slack.Close)
	f.slack.Channels = map[string]string{"teams": "CTEAMS", "team-b-alerts": "CTEAMB"}
	am := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var s upstream.Silence
		_ = json.NewDecoder(r.Body).Decode(&s)
		f.silences = append(f.silences, s)
		_, _ = io.WriteString(w, `{"silenceID": "sid"}`)
	}))
	t.Cleanup(am.Close)
	cfg, err := config.Parse([]byte(fmt.Sprintf("alertmanager: {url: %s}\nchannels:\n  alerts: [teams]\n  teams: {team-b: team-b-alerts}\n", am.URL)))
	if err != nil {
		t.Fatal(err)
	}
	e := engine.New(cfg, f.slack.Client(), &upstream.Alertmanager{URL: am.URL, HTTP: am.Client()}, metrics.New())
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	e.Log, e.Interactive, e.Spawn = quiet, func() bool { return true }, func(fn func()) { fn() }
	s := &Server{Engine: e, SigningSecret: func() string { return "s3cret" }, Log: quiet, Background: func(fn func()) { fn() }}
	f.srv = httptest.NewServer(s.Handler())
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fixture) post(t *testing.T, path string, body []byte, h http.Header) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL+path, strings.NewReader(string(body)))
	for k, v := range h {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(b))
}

func signed(payload any, secret string) ([]byte, http.Header) {
	raw, _ := json.Marshal(payload)
	body := []byte(url.Values{"payload": {string(raw)}}.Encode())
	ts := fmt.Sprint(time.Now().Unix())
	h := http.Header{}
	h.Set("X-Slack-Request-Timestamp", ts)
	h.Set("X-Slack-Signature", slack.Sign(secret, ts, body))
	h.Set("Content-Type", "application/x-www-form-urlencoded")
	return body, h
}

func TestWebhookPressAndModal(t *testing.T) {
	f := setup(t)
	g := alert.Group{GroupKey: "k", Receiver: "teams", Status: "firing", GroupLabels: map[string]string{"alertname": "X"},
		CommonLabels: map[string]string{"alertname": "X", "team": "team-b", "severity": "warning"},
		Alerts: []alert.Alert{{Status: "firing", StartsAt: "2026-10-27T08:15:00Z", Labels: map[string]string{"alertname": "X", "namespace": "ns", "pod": "p0"},
			Annotations: map[string]string{"summary": "A thing broke"}}}}
	raw, _ := json.Marshal(g)
	if code, done := f.post(t, "/alertmanager?channel=teams&by_team=1", raw, nil); code != 200 || done != "posted" {
		t.Fatal(code, done)
	}
	if strings.Join(f.slack.Posted, ",") != "CTEAMB" {
		t.Fatal(f.slack.Posted)
	}
	ts := f.slack.Newest().TS
	press := map[string]any{"type": "block_actions", "trigger_id": "T", "channel": map[string]string{"id": "CTEAMB"},
		"container": map[string]string{"message_ts": ts}, "user": map[string]string{"id": "U1", "username": "pat"},
		"actions": []map[string]any{{"action_id": "silence", "selected_option": map[string]string{"value": "1h|0"}}}}
	body, h := signed(press, "s3cret")
	if code, _ := f.post(t, "/slack/interactions", body, h); code != 200 || len(f.slack.Views) != 1 {
		t.Fatal(code, len(f.slack.Views))
	}
	var view map[string]any
	_ = json.Unmarshal(f.slack.Views[0], &view)
	view["state"] = map[string]any{"values": map[string]any{"reason": map[string]any{"reason": map[string]string{"value": "known"}}}}
	body, h = signed(map[string]any{"type": "view_submission", "user": map[string]string{"id": "U1", "username": "pat"}, "view": view}, "s3cret")
	if code, answer := f.post(t, "/slack/interactions", body, h); code != 200 || answer != "" {
		t.Fatal(code, answer)
	}
	if len(f.silences) != 1 || !strings.HasPrefix(f.silences[0].Comment, "known (from Slack by pat") {
		t.Fatal(f.silences)
	}
	body, h = signed(press, "wrong")
	if code, _ := f.post(t, "/slack/interactions", body, h); code != 401 {
		t.Fatal(code)
	}
}

func TestWebhookNeedsAChannelAndJSON(t *testing.T) {
	f := setup(t)
	if code, _ := f.post(t, "/alertmanager", []byte(`{}`), nil); code != 400 {
		t.Fatal(code)
	}
	if code, _ := f.post(t, "/alertmanager?channel=teams", []byte(`not json`), nil); code != 400 {
		t.Fatal(code)
	}
	resp, err := http.Get(f.srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(b), "fogbell_interactive 1") || !strings.Contains(string(b), `fogbell_notifications_total{result="error"} 2`) {
		t.Fatal(string(b))
	}
	if resp, _ := http.Get(f.srv.URL + "/anything"); resp.StatusCode != 404 {
		t.Fatal(resp.StatusCode)
	}
}
