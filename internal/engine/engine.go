// Package engine turns Alertmanager notifications into one Slack message per incident and keeps that
// message true: it posts once, edits in place, threads every change under it, and closes it when the
// group resolves. Its state lives on the message (package store); what it looks up is package enrich;
// what people press is in actions.go.
package engine

import (
	"context"
	"log/slog"
	"time"

	"github.com/webgrip/fogbell/internal/alert"
	"github.com/webgrip/fogbell/internal/config"
	"github.com/webgrip/fogbell/internal/enrich"
	"github.com/webgrip/fogbell/internal/events"
	"github.com/webgrip/fogbell/internal/format"
	"github.com/webgrip/fogbell/internal/investigate"
	"github.com/webgrip/fogbell/internal/metrics"
	"github.com/webgrip/fogbell/internal/render"
	"github.com/webgrip/fogbell/internal/slack"
	"github.com/webgrip/fogbell/internal/state"
	"github.com/webgrip/fogbell/internal/store"
	"github.com/webgrip/fogbell/internal/upstream"
)

// Tracker files tickets for the ticket button.
type Tracker interface {
	Name() string
	Available() bool
	Member(ctx context.Context, email string) (string, error)
	Create(ctx context.Context, name, markdown, member string) (upstream.Ticket, error)
}

// Upstreams are the optional, read-only services the enrichers read.
type Upstreams struct {
	Prom  *upstream.Prometheus
	Ruler *upstream.Ruler
	Logs  *upstream.Logs
	Kube  enrich.KubeReader
}

// Engine is the incident engine. Build it with New, then Wire its upstreams.
type Engine struct {
	Config       *config.Config
	Slack        *slack.Client
	Store        *store.Store
	Render       *render.Renderer
	AM           *upstream.Alertmanager
	Tracker      Tracker
	Investigator investigate.Investigator
	Events       events.Sink
	Metrics      *metrics.Metrics
	Log          *slog.Logger
	// Now is the clock; tests replace it.
	Now func() time.Time
	// Interactive is read on every render: the signing secret can arrive after start-up, and the
	// buttons that act follow it.
	Interactive func() bool
	// Spawn runs work after the first post beside the webhook Alertmanager waits on; tests run it inline.
	Spawn func(func())
	// Roster reads the on-call roster; tests replace it.
	Roster func() Roster

	up        Upstreams
	enrichers []enrich.Enricher
	actions   map[string]action
}

// New wires an engine with its defaults and no upstreams.
func New(cfg *config.Config, sl *slack.Client, am *upstream.Alertmanager, m *metrics.Metrics) *Engine {
	e := &Engine{
		Config:      cfg,
		Slack:       sl,
		Render:      &render.Renderer{Config: cfg},
		AM:          am,
		Events:      events.Discard{},
		Metrics:     m,
		Log:         slog.Default(),
		Now:         time.Now,
		Interactive: func() bool { return false },
		Spawn:       func(f func()) { go f() },
	}
	e.Store = store.New(store.Options{
		History: sl, Window: cfg.Incidents.HistoryWindow.Seconds(), Now: e.now,
		Events:        store.Events{Incident: cfg.Slack.Events.Incident, Adopt: cfg.Slack.AdoptEvents},
		HistoryFailed: func() { m.HistoryFailed.Inc() },
		Log:           e.Log,
	})
	e.Roster = e.readRoster
	e.actions = e.registry()
	e.Wire(Upstreams{})
	return e
}

// Wire sets the upstreams and builds the enrichers they and the configuration turn on, in the order
// their lines show.
func (e *Engine) Wire(up Upstreams) {
	e.up = up
	cfg := e.Config.Enrich
	objects := e.Config.ObjectReader()
	var list []enrich.Enricher
	if up.Logs != nil {
		list = append(list, &enrich.Logs{Logs: up.Logs, Objects: objects,
			Fields: enrich.LogFields{Namespace: cfg.Logs.Namespace, Pod: cfg.Logs.Pod, Container: cfg.Logs.Container}})
	}
	series := &enrich.Series{Prom: up.Prom, Objects: objects, Now: e.clock}
	if up.Prom != nil {
		list = append(list, &enrich.Spark{Series: series})
	}
	list = append(list, &enrich.History{Store: e.Store, Now: e.clock})
	if cfg.Related {
		list = append(list, &enrich.Related{AM: e.AM, Quiet: e.Config.Quiet})
	}
	if up.Kube != nil && cfg.Flux.Enabled {
		list = append(list, &enrich.Flux{Kube: up.Kube, Window: time.Duration(cfg.Flux.Window), OwnSource: cfg.Flux.OwnSource,
			Links: enrich.Links{Commit: cfg.Flux.CommitURL, Path: cfg.Flux.PathURL}, Now: e.clock})
	}
	if up.Prom != nil && cfg.Charts {
		list = append(list, &enrich.Chart{Series: series})
	}
	e.enrichers = list
}

func (e *Engine) clock() time.Time { return e.Now() }

func (e *Engine) now() float64 { return float64(e.Now().UnixNano()) / 1e9 }

func (e *Engine) features() render.Features {
	return render.Features{
		Ticket:      e.Tracker != nil && e.Tracker.Available(),
		Investigate: e.Investigator != nil && e.Investigator.Available(),
	}
}

func (e *Engine) interactive() bool { return e.Interactive != nil && e.Interactive() }

func (e *Engine) attachments(g *alert.Group, st *state.Incident) []slack.Attachment {
	return e.Render.Render(g, st, e.interactive(), e.features())
}

func (e *Engine) post(ctx context.Context, channel string, g *alert.Group, st *state.Incident, mention bool) (string, string, error) {
	text := "*" + e.Render.Title(g, false) + "*"
	if mention {
		text = "<!channel> " + text
	}
	return e.Slack.PostMessage(ctx, slack.Post{Channel: channel, Text: text, Attachments: e.attachments(g, st), Metadata: e.Store.Metadata(st)})
}

func (e *Engine) update(ctx context.Context, en store.Entry, g *alert.Group) error {
	return e.Slack.UpdateMessage(ctx, slack.Update{Channel: en.Channel, TS: en.TS, Text: "*" + e.Render.Title(g, g.Resolved()) + "*",
		Attachments: e.attachments(g, &en.State), Metadata: e.Store.Metadata(&en.State)})
}

func (e *Engine) reply(ctx context.Context, channel, ts, text string) {
	if err := e.Slack.Reply(ctx, channel, ts, text); err != nil {
		e.Log.Warn("thread reply failed", "channel", channel, "ts", ts, "error", err)
	}
}

func (e *Engine) emit(ctx context.Context, kind string, en store.Entry, actor string) {
	e.Events.Send(ctx, kind, events.Incident{Channel: en.Channel, TS: en.TS, Link: format.Permalink(en.Channel, en.TS), Actor: actor, State: en.State})
}

// restate rewrites the state line and the buttons of an existing message and keeps the rest, for a
// change made without the notification group at hand.
func (e *Engine) restate(ctx context.Context, en store.Entry, msg *slack.Message) {
	if len(msg.Attachments) == 0 {
		return
	}
	att := msg.Attachments[0]
	var acting []slack.Element
	if e.interactive() {
		acting = e.Render.InteractiveElements(&en.State, en.State.Resolved(), e.features())
	}
	att.Blocks = render.Restate(att.Blocks, render.StateLine(&en.State, en.State.Resolved()), acting)
	if err := e.Slack.UpdateMessage(ctx, slack.Update{Channel: en.Channel, TS: en.TS, Text: msg.Text,
		Attachments: []slack.Attachment{att}, Metadata: e.Store.Metadata(&en.State)}); err != nil {
		e.Log.Warn("restate failed", "error", err)
	}
}

func timing(st *state.Incident) []string { return []string{st.Alertname, st.Severity, st.Team} }

func startOr(start, now float64) float64 {
	if start != 0 {
		return start
	}
	return now
}
