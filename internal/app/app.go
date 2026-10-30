// Package app builds fogbell from its configuration: every client, the engine, the HTTP server and
// the background loops. cmd/fogbell only reads flags and calls it.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/webgrip/fogbell/internal/config"
	"github.com/webgrip/fogbell/internal/engine"
	"github.com/webgrip/fogbell/internal/events"
	"github.com/webgrip/fogbell/internal/investigate"
	"github.com/webgrip/fogbell/internal/metrics"
	"github.com/webgrip/fogbell/internal/server"
	"github.com/webgrip/fogbell/internal/slack"
	"github.com/webgrip/fogbell/internal/upstream"
)

// App is fogbell, built.
type App struct {
	Config  *config.Config
	Engine  *engine.Engine
	Handler http.Handler
	Log     *slog.Logger
}

// ReadFile is a secret or token file's content, trimmed; "" when it is missing.
func ReadFile(path string) string {
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// Build wires every part the configuration turns on.
func Build(cfg *config.Config, log *slog.Logger, version string) (*App, error) {
	token := ReadFile(cfg.Slack.TokenFile)
	if token == "" {
		return nil, errors.New("slack.tokenFile is empty or unreadable: fogbell posts with a bot token")
	}
	httpClient := &http.Client{Timeout: 10 * time.Second}
	m := metrics.New()
	sl := slack.New(token, httpClient)
	sl.BaseURL, sl.Log = cfg.Slack.APIURL, log
	sl.Record = func(method, result string) { m.SlackRequests.WithLabelValues(method, result).Inc() }

	e := engine.New(cfg, sl, &upstream.Alertmanager{URL: cfg.Alertmanager.URL, HTTP: httpClient}, m)
	e.Log = log
	signing := func() string { return ReadFile(cfg.Slack.SigningSecretFile) }
	e.Interactive = func() bool { return signing() != "" }
	up, err := upstreams(cfg, httpClient, log)
	if err != nil {
		return nil, err
	}
	e.Wire(up)
	if cu := cfg.Tickets.ClickUp; cu.List != "" {
		e.Tracker = &upstream.ClickUp{TokenFile: cu.TokenFile, List: cu.List, HTTP: httpClient}
	}
	if p := cfg.Investigate.Ploeg; p.URL != "" {
		e.Investigator = &investigate.Ploeg{URL: p.URL, TokenFile: p.TokenFile, Team: p.Team, Target: p.Target,
			BudgetUSD: p.BudgetUSD, Model: p.Model, Console: p.Console, HTTP: httpClient}
	}
	e.Events = sinks(cfg, httpClient, log, version)

	srv := &server.Server{Engine: e, SigningSecret: signing, Log: log, Background: func(f func()) { go f() }}
	return &App{Config: cfg, Engine: e, Handler: srv.Handler(), Log: log}, nil
}

func upstreams(cfg *config.Config, httpClient *http.Client, log *slog.Logger) (engine.Upstreams, error) {
	var up engine.Upstreams
	en := cfg.Enrich
	if en.Metrics.URL != "" {
		up.Prom = &upstream.Prometheus{URL: en.Metrics.URL, HTTP: httpClient}
	}
	if en.Ruler.URL != "" || up.Prom != nil {
		up.Ruler = &upstream.Ruler{URL: en.Ruler.URL, HTTP: httpClient}
		if en.Ruler.GeneratorHost != "" {
			up.Ruler.GeneratorHost = regexp.MustCompile(en.Ruler.GeneratorHost)
		}
	}
	if en.Logs.URL != "" {
		up.Logs = &upstream.Logs{URL: en.Logs.URL, HTTP: httpClient}
	}
	if en.Flux.Enabled {
		kube, err := upstream.InCluster()
		switch {
		case err != nil:
			return up, fmt.Errorf("enrich.flux: %w", err)
		case kube == nil:
			log.Warn("not in a cluster; no change line")
		default:
			up.Kube = kube
		}
	}
	return up, nil
}

func sinks(cfg *config.Config, httpClient *http.Client, log *slog.Logger, version string) events.Sink {
	var out events.Fanout
	for _, w := range cfg.Events.Webhooks {
		types := map[string]bool{}
		for _, t := range w.Types {
			types[t] = true
		}
		out = append(out, &events.Webhook{URL: w.URL, SecretFile: w.SecretFile, Types: types,
			Source: "fogbell/" + version, HTTP: httpClient, Log: log, Now: time.Now})
	}
	if len(out) == 0 {
		return events.Discard{}
	}
	return out
}

// Run serves HTTP and runs the background loops until ctx ends.
func (a *App) Run(ctx context.Context) error {
	go every(ctx, 2*time.Minute, func(now time.Time) {
		for _, d := range a.Engine.Maintain(ctx) {
			a.Log.Info("maintain", "result", d)
		}
		for _, d := range a.Engine.Tick(ctx, now) {
			a.Log.Info("schedule", "result", d)
		}
	})
	hs := &http.Server{Addr: a.Config.Listen, Handler: a.Handler, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = hs.Shutdown(shutdown)
	}()
	a.Log.Info("fogbell started", "listen", a.Config.Listen, "interactive", a.Engine.Interactive(),
		"alert_channels", a.Config.AlertChannels(), "digest_channels", a.Config.DigestChannels())
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func every(ctx context.Context, d time.Duration, f func(time.Time)) {
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			f(now)
		}
	}
}
