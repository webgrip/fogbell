package events

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/webgrip/fogbell/internal/state"
)

func TestWebhookSendsASignedCloudEvent(t *testing.T) {
	var got map[string]any
	var sig, ctype string
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		sig, ctype = r.Header.Get("X-Fogbell-Signature"), r.Header.Get("Content-Type")
		_ = json.Unmarshal(body, &got)
	}))
	defer srv.Close()
	secret := filepath.Join(t.TempDir(), "s")
	if err := os.WriteFile(secret, []byte("k3y\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w := &Webhook{URL: srv.URL, SecretFile: secret, Types: map[string]bool{Opened: true}, Source: "fogbell/test",
		HTTP: srv.Client(), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return time.Unix(1793089000, 0) }}
	w.Send(context.Background(), Resolved, Incident{TS: "1.2"})
	if got != nil {
		t.Fatal("a type outside the filter was sent")
	}
	w.Send(context.Background(), Opened, Incident{Channel: "C1", TS: "1.2", State: state.Incident{GroupKey: "k", Alertname: "X"}})
	if got["specversion"] != "1.0" || got["type"] != "dev.fogbell.incident.opened" || got["subject"] != "k" || ctype != "application/cloudevents+json" {
		t.Fatal(got, ctype)
	}
	if got["data"].(map[string]any)["state"].(map[string]any)["alertname"] != "X" {
		t.Fatal(got["data"])
	}
	mac := hmac.New(sha256.New, []byte("k3y"))
	mac.Write(body)
	if sig != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
		t.Fatal(sig)
	}
	if !Known(Investigated) || Known("exploded") {
		t.Fatal("Known")
	}
}
