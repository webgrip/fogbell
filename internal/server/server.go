// Package server is fogbell's HTTP surface: the Alertmanager webhook, Slack's interactions, health and
// metrics.
package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/webgrip/fogbell/internal/alert"
	"github.com/webgrip/fogbell/internal/engine"
	"github.com/webgrip/fogbell/internal/slack"
)

const maxBody = 4 << 20

// Server routes requests to the engine.
type Server struct {
	Engine *engine.Engine
	// SigningSecret is read on every interaction: the secret can arrive after start-up.
	SigningSecret func() string
	Log           *slog.Logger
	// Background runs interaction work after the answer; tests run it inline.
	Background func(func())
}

// Handler is the routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	metrics := promhttp.HandlerFor(s.Engine.Metrics.Registry, promhttp.HandlerOpts{})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		v := 0.0
		if s.SigningSecret() != "" {
			v = 1
		}
		s.Engine.Metrics.Interactive.Set(v)
		metrics.ServeHTTP(w, r)
	})
	mux.HandleFunc("POST /alertmanager", s.alertmanager)
	mux.HandleFunc("POST /slack/interactions", s.interactions)
	return mux
}

func (s *Server) alertmanager(w http.ResponseWriter, r *http.Request) {
	m := s.Engine.Metrics
	channel := r.URL.Query().Get("channel")
	var g alert.Group
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&g); err != nil {
		m.Notifications.WithLabelValues("error").Inc()
		http.Error(w, "not an Alertmanager webhook: "+err.Error(), http.StatusBadRequest)
		return
	}
	if channel == "" {
		m.Notifications.WithLabelValues("error").Inc()
		http.Error(w, "the webhook URL needs ?channel=<name>", http.StatusBadRequest)
		return
	}
	done, err := s.Engine.Notify(r.Context(), channel, &g, r.URL.Query().Get("by_team") == "1")
	if err != nil {
		m.Notifications.WithLabelValues("error").Inc()
		s.Log.Error("notify failed", "channel", channel, "group", g.Key(), "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	m.Notifications.WithLabelValues(done).Inc()
	m.LastSuccess.SetToCurrentTime()
	s.Log.Info("notify", "channel", channel, "group", g.Key(), "result", done)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.WriteString(w, done) // #nosec G705 -- one of the engine's own result words, served as plain text
}

func (s *Server) interactions(w http.ResponseWriter, r *http.Request) {
	m := s.Engine.Metrics
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		http.Error(w, "unreadable body", http.StatusBadRequest)
		return
	}
	if why := slack.Rejection(s.SigningSecret(), r.Header, body, float64(time.Now().Unix())); why != "" {
		m.Interactions.WithLabelValues("rejected").Inc()
		s.Log.Warn("interaction rejected", "reason", why)
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		http.Error(w, "not a form", http.StatusBadRequest)
		return
	}
	var p engine.Interaction
	if err := json.Unmarshal([]byte(form.Get("payload")), &p); err != nil {
		http.Error(w, "payload is not JSON", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
	s.Background(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		defer func() {
			if rec := recover(); rec != nil {
				m.Interactions.WithLabelValues("error").Inc()
				s.Log.Error("interaction panicked", "panic", rec)
			}
		}()
		s.Log.Info("interaction", "result", s.Engine.Interact(ctx, &p))
	})
}
