package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRejection(t *testing.T) {
	body, ts, secret := []byte("payload=x"), "1793089000", "s3cret"
	h := http.Header{}
	h.Set("X-Slack-Request-Timestamp", ts)
	h.Set("X-Slack-Signature", Sign(secret, ts, body))
	if got := Rejection(secret, h, body, 1793089010); got != "" {
		t.Fatal(got)
	}
	for _, c := range []struct {
		secret string
		body   string
		now    float64
		want   string
	}{
		{secret, "payload=x", 1793089010 + 600, "timestamp"},
		{secret, "payload=y", 1793089010, "signature mismatch"},
		{"", "payload=x", 1793089010, "no signing secret"},
		{"other", "payload=x", 1793089010, "signature mismatch"},
	} {
		got := Rejection(c.secret, h, []byte(c.body), c.now)
		if !strings.Contains(got, c.want) {
			t.Errorf("Rejection = %q, want %q", got, c.want)
		}
		if c.secret != "" && strings.Contains(got, c.secret) {
			t.Error("the reason names the secret")
		}
	}
}

func TestElementTextHasTwoShapes(t *testing.T) {
	els := []Element{TextElement("*hi*"), Button("ack", "👀 I'm on it", "")}
	raw, err := json.Marshal(els)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"text":"*hi*"`) || !strings.Contains(string(raw), `"text":{"type":"plain_text","text":"👀 I'm on it"}`) {
		t.Fatal(string(raw))
	}
	var back []Element
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back[0].Content != "*hi*" || back[1].Label.Text != "👀 I'm on it" {
		t.Fatal(back)
	}
}

func TestCallRetriesARateLimitAndCounts(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer xoxb-t" {
			t.Error("no bearer token")
		}
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"ok": false, "error": "not_in_channel"}`))
	}))
	defer srv.Close()
	var results []string
	c := New("xoxb-t", srv.Client())
	c.BaseURL = srv.URL
	c.Sleep = func(time.Duration) {}
	c.Record = func(method, result string) { results = append(results, method+"="+result) }
	err := c.Call(context.Background(), "chat.postMessage", map[string]string{"channel": "C1"}, nil)
	if !Is(err, "not_in_channel") || calls.Load() != 2 {
		t.Fatal(err, calls.Load())
	}
	if strings.Join(results, ",") != "chat.postMessage=not_in_channel" {
		t.Fatal(results)
	}
}

func TestChannelIDFallsBackToTheName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok": false, "error": "missing_scope"}`))
	}))
	defer srv.Close()
	c := New("t", srv.Client())
	c.BaseURL = srv.URL
	if got := c.ChannelID(context.Background(), "#alerts"); got != "#alerts" {
		t.Fatal(got)
	}
	if got := c.ChannelID(context.Background(), "C0123456789"); got != "C0123456789" {
		t.Fatal(got)
	}
	c.Learn("alerts", "C1")
	if got := c.ChannelID(context.Background(), "alerts"); got != "C1" {
		t.Fatal(got)
	}
	if msgs, err := c.History(context.Background(), "#alerts", 0); err != nil || msgs != nil {
		t.Fatal("history by name is empty")
	}
}
