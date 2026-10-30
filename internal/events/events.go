// Package events sends what happens to an incident to other systems as CloudEvents: opened, updated,
// acknowledged, silenced, unsilenced, resolved, reopened, ticketed and investigated. A workflow tool,
// a status page or an agent platform subscribes with a URL and needs nothing else from fogbell.
package events

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/webgrip/fogbell/internal/state"
)

// Types fogbell sends, prefixed with "dev.fogbell.incident.".
const (
	Opened       = "opened"
	Updated      = "updated"
	Acknowledged = "acknowledged"
	Silenced     = "silenced"
	Unsilenced   = "unsilenced"
	Resolved     = "resolved"
	Reopened     = "reopened"
	Ticketed     = "ticketed"
	Investigated = "investigated"
)

// Types is every event type, in lifecycle order.
var Types = []string{Opened, Updated, Acknowledged, Silenced, Unsilenced, Resolved, Reopened, Ticketed, Investigated}

// Known reports whether t is an event type.
func Known(t string) bool {
	for _, k := range Types {
		if k == t {
			return true
		}
	}
	return false
}

// Incident is the event's data: the incident's state and where its thread is.
type Incident struct {
	Channel string         `json:"channel"`
	TS      string         `json:"ts"`
	Link    string         `json:"link"`
	Actor   string         `json:"actor,omitempty"`
	State   state.Incident `json:"state"`
}

// Sink receives events.
type Sink interface {
	Send(ctx context.Context, kind string, data Incident)
}

// Discard drops every event.
type Discard struct{}

// Send implements Sink.
func (Discard) Send(context.Context, string, Incident) {}

// Webhook posts each event as a structured-mode CloudEvent (specversion 1.0). With a secret, the
// body is signed: X-Fogbell-Signature is "sha256=" and the hex HMAC-SHA256 of the body.
type Webhook struct {
	URL        string
	SecretFile string
	Types      map[string]bool
	Source     string
	HTTP       *http.Client
	Log        *slog.Logger
	Now        func() time.Time

	seq atomic.Uint64
}

type cloudEvent struct {
	SpecVersion     string   `json:"specversion"`
	ID              string   `json:"id"`
	Source          string   `json:"source"`
	Type            string   `json:"type"`
	Subject         string   `json:"subject"`
	Time            string   `json:"time"`
	DataContentType string   `json:"datacontenttype"`
	Data            Incident `json:"data"`
}

// Send implements Sink. A failed delivery is logged and dropped: an event never holds up the incident.
func (w *Webhook) Send(ctx context.Context, kind string, data Incident) {
	if len(w.Types) > 0 && !w.Types[kind] {
		return
	}
	ev := cloudEvent{SpecVersion: "1.0", ID: data.TS + "-" + kind + "-" + strconv.FormatUint(w.seq.Add(1), 10),
		Source: w.Source, Type: "dev.fogbell.incident." + kind, Subject: data.State.GroupKey,
		Time: w.Now().UTC().Format(time.RFC3339Nano), DataContentType: "application/json", Data: data}
	body, err := json.Marshal(ev)
	if err != nil {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		w.Log.Warn("event not sent", "type", ev.Type, "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/cloudevents+json")
	if secret := w.secret(); secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		req.Header.Set("X-Fogbell-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := w.HTTP.Do(req)
	if err != nil {
		w.Log.Warn("event not delivered", "type", ev.Type, "error", err)
		return
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 300 {
		w.Log.Warn("event refused", "type", ev.Type, "status", fmt.Sprint(resp.StatusCode))
	}
}

func (w *Webhook) secret() string {
	if w.SecretFile == "" {
		return ""
	}
	b, err := os.ReadFile(w.SecretFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// Fanout sends every event to each sink.
type Fanout []Sink

// Send implements Sink.
func (f Fanout) Send(ctx context.Context, kind string, data Incident) {
	for _, s := range f {
		s.Send(ctx, kind, data)
	}
}
