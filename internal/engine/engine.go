// Package engine turns Alertmanager notifications into one Slack message per incident and keeps that
// message true: it posts once, edits in place, threads every change under it, and closes it when the
// group resolves. Its state lives on the message as metadata; there is no database.
package engine

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/fogbell-dev/fogbell/internal/alert"
	"github.com/fogbell-dev/fogbell/internal/config"
	"github.com/fogbell-dev/fogbell/internal/format"
	"github.com/fogbell-dev/fogbell/internal/metrics"
	"github.com/fogbell-dev/fogbell/internal/render"
	"github.com/fogbell-dev/fogbell/internal/slack"
	"github.com/fogbell-dev/fogbell/internal/state"
	"github.com/fogbell-dev/fogbell/internal/upstream"
)

// Tracker files tickets for the ticket button.
type Tracker interface {
	Name() string
	Available() bool
	Member(ctx context.Context, email string) (string, error)
	Create(ctx context.Context, name, markdown, member string) (upstream.Ticket, error)
}

// KubeReader reads the Kubernetes API.
type KubeReader interface {
	Get(ctx context.Context, path string, out any) error
}

// Engine is the incident engine. Build it with New.
type Engine struct {
	Config  *config.Config
	Slack   *slack.Client
	Render  *render.Renderer
	AM      *upstream.Alertmanager
	Prom    *upstream.Prometheus
	Ruler   *upstream.Ruler
	Logs    *upstream.Logs
	Kube    KubeReader
	Tracker Tracker
	Metrics *metrics.Metrics
	Log     *slog.Logger
	// Now is the clock; tests replace it.
	Now func() time.Time
	// Interactive is read on every render: the signing secret can arrive after start-up, and the
	// buttons that act follow it.
	Interactive func() bool
	// Spawn runs work after the first post beside the webhook Alertmanager waits on; tests run it inline.
	Spawn func(func())
	// Roster reads the on-call roster; tests replace it.
	Roster func() Roster

	mu        sync.Mutex
	locks     map[string]*sync.Mutex
	scanLocks map[string]*sync.Mutex
	index     map[string]entry
	scanned   map[string]bool
	groups    map[string]*alert.Group
	children  map[string]child
	flux      fluxCache
}

type entry struct {
	Channel, TS string
	St          state.Incident
}

type child struct {
	Channel, TS, Parent string
	Count               int
}

// New wires an engine with its defaults.
func New(cfg *config.Config, sl *slack.Client, am *upstream.Alertmanager, m *metrics.Metrics) *Engine {
	e := &Engine{
		Config:      cfg,
		Slack:       sl,
		Render:      &render.Renderer{Config: cfg},
		AM:          am,
		Metrics:     m,
		Log:         slog.Default(),
		Now:         time.Now,
		Interactive: func() bool { return false },
		Spawn:       func(f func()) { go f() },
	}
	e.Roster = e.readRoster
	return e
}

func (e *Engine) now() float64 { return float64(e.Now().UnixNano()) / 1e9 }

func (e *Engine) init() {
	if e.locks == nil {
		e.locks = map[string]*sync.Mutex{}
		e.scanLocks = map[string]*sync.Mutex{}
		e.index = map[string]entry{}
		e.scanned = map[string]bool{}
		e.groups = map[string]*alert.Group{}
		e.children = map[string]child{}
	}
}

func (e *Engine) lock(key string) *sync.Mutex {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.init()
	l, ok := e.locks[key]
	if !ok {
		l = &sync.Mutex{}
		e.locks[key] = l
	}
	return l
}

func (e *Engine) get(key string) (entry, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.init()
	en, ok := e.index[key]
	return en, ok
}

func (e *Engine) put(key string, en entry) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.init()
	e.index[key] = en
}

func (e *Engine) group(key string) *alert.Group {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.init()
	return e.groups[key]
}

func (e *Engine) setGroup(key string, g *alert.Group) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.init()
	if g == nil {
		delete(e.groups, key)
		return
	}
	e.groups[key] = g
}

// Forget drops what the engine remembers, as a restart would; for tests.
func (e *Engine) Forget() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.locks, e.index = nil, nil
	e.init()
}

func (e *Engine) features() render.Features {
	return render.Features{Ticket: e.Tracker != nil && e.Tracker.Available()}
}

func (e *Engine) interactive() bool { return e.Interactive != nil && e.Interactive() }

// incident reads fogbell's state from a message: its own event type, or one it adopts.
func (e *Engine) incident(m *slack.Message) (*state.Incident, bool) {
	var st state.Incident
	if m.Payload(e.Config.Slack.Events.Incident, &st) {
		return &st, true
	}
	for _, ev := range e.Config.Slack.AdoptEvents {
		if m.Payload(ev, &st) {
			return &st, true
		}
	}
	return nil, false
}

func (e *Engine) metadata(st *state.Incident) *slack.Metadata {
	raw, _ := json.Marshal(st)
	return &slack.Metadata{EventType: e.Config.Slack.Events.Incident, EventPayload: raw}
}

func (e *Engine) history(ctx context.Context, channel string, oldest float64) []slack.Message {
	msgs, err := e.Slack.History(ctx, channel, oldest)
	if err != nil {
		e.Metrics.HistoryFailed.Inc()
		e.Log.Warn("conversations.history unavailable", "channel", channel, "error", err)
		return nil
	}
	return msgs
}

func tsLess(a, b string) bool {
	x, _ := strconv.ParseFloat(a, 64)
	y, _ := strconv.ParseFloat(b, 64)
	return x < y
}

// scan reads every incident message of the history window, oldest first, into the index.
func (e *Engine) scan(ctx context.Context, channel string) {
	e.mu.Lock()
	e.init()
	sl, ok := e.scanLocks[channel]
	if !ok {
		sl = &sync.Mutex{}
		e.scanLocks[channel] = sl
	}
	e.mu.Unlock()
	sl.Lock()
	defer sl.Unlock()
	e.mu.Lock()
	done := e.scanned[channel]
	e.mu.Unlock()
	if done {
		return
	}
	msgs := e.history(ctx, channel, e.now()-e.Config.Incidents.HistoryWindow.Seconds())
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := len(msgs) - 1; i >= 0; i-- {
		st, ok := e.incident(&msgs[i])
		if !ok || st.GroupKey == "" {
			continue
		}
		if cur, has := e.index[st.GroupKey]; has && tsLess(msgs[i].TS, cur.TS) {
			continue
		}
		e.index[st.GroupKey] = entry{Channel: channel, TS: msgs[i].TS, St: *st}
		for k, n := range st.ChildMap() {
			e.children[k] = child{Channel: channel, TS: msgs[i].TS, Parent: st.GroupKey, Count: n}
		}
	}
	e.scanned[channel] = true
}

func (e *Engine) find(ctx context.Context, channel, key string) (entry, bool) {
	e.scan(ctx, channel)
	en, ok := e.get(key)
	if !ok || en.Channel != channel {
		return entry{}, false
	}
	return en, true
}

func (e *Engine) weekCount(ctx context.Context, channel, key string) int {
	n := 0
	for _, m := range e.history(ctx, channel, e.now()-7*86400) {
		if st, ok := e.incident(&m); ok && st.GroupKey == key {
			n++
		}
	}
	return n
}

func (e *Engine) post(ctx context.Context, channel string, g *alert.Group, st *state.Incident, mention bool) (string, string, error) {
	text := "*" + e.Render.Title(g, false) + "*"
	if mention {
		text = "<!channel> " + text
	}
	return e.Slack.PostMessage(ctx, slack.Post{Channel: channel, Text: text,
		Attachments: e.Render.Render(g, st, e.interactive(), e.features()), Metadata: e.metadata(st)})
}

func (e *Engine) update(ctx context.Context, channel, ts string, g *alert.Group, st *state.Incident) error {
	return e.Slack.UpdateMessage(ctx, slack.Update{Channel: channel, TS: ts, Text: "*" + e.Render.Title(g, g.Resolved()) + "*",
		Attachments: e.Render.Render(g, st, e.interactive(), e.features()), Metadata: e.metadata(st)})
}

func (e *Engine) reply(ctx context.Context, channel, ts, text string) {
	if err := e.Slack.Reply(ctx, channel, ts, text); err != nil {
		e.Log.Warn("thread reply failed", "channel", channel, "ts", ts, "error", err)
	}
}

func timing(st *state.Incident) []string { return []string{st.Alertname, st.Severity, st.Team} }

// Notify handles one Alertmanager notification for a channel and says what it did. With byTeam (a
// receiver whose groups belong to teams), a group whose team has a channel of its own goes there; a
// team channel the app is not in falls back to channel, counted, so a team that has not invited the
// app yet still gets its alerts.
func (e *Engine) Notify(ctx context.Context, channel string, g *alert.Group, byTeam bool) (string, error) {
	if byTeam {
		if tc := e.Config.TeamChannel(g.Label(e.Config.Channels.TeamLabel)); tc != "" && tc != channel {
			done, err := e.notify(ctx, tc, g)
			if err == nil || !slack.Is(err, "not_in_channel", "channel_not_found", "is_archived") {
				return done, err
			}
			e.Metrics.Degraded.WithLabelValues("team_channel").Inc()
			e.Log.Warn("team channel unusable; posting to the receiver's channel", "team_channel", tc, "channel", channel, "error", err)
		}
	}
	return e.notify(ctx, channel, g)
}

func (e *Engine) notify(ctx context.Context, channelName string, g *alert.Group) (string, error) {
	key := g.Key()
	channel := e.Slack.ChannelID(ctx, channelName)
	now := e.now()
	page := e.Config.IsPage(g.Receiver)
	objects := e.Config.ObjectReader()
	l := e.lock(key)
	l.Lock()
	e.setGroup(key, g)
	hit, found := e.find(ctx, channel, key)
	active := g.Active()
	open := found && !hit.St.Resolved()
	if !open {
		if done := e.fold(ctx, key, g, active, now); done != "" {
			l.Unlock()
			return done, nil
		}
	}
	if g.Resolved() {
		defer l.Unlock()
		if !open {
			return "ignored-resolve", nil
		}
		e.setGroup(key, nil)
		st := hit.St
		st.State, st.ResolvedAt, st.Count = "resolved", now, 0
		e.Metrics.ResolveSecs.WithLabelValues(timing(&st)...).Observe(now - st.Shown(now))
		if err := e.update(ctx, hit.Channel, hit.TS, g, &st); err != nil {
			return "", err
		}
		e.reply(ctx, hit.Channel, hit.TS, "✅ Resolved after "+format.Duration(now-startOr(st.StartedAt, now))+".")
		e.put(key, entry{hit.Channel, hit.TS, st})
		return "resolved", nil
	}
	if open {
		defer l.Unlock()
		st := hit.St
		change := render.DescribeChange(int(st.Count), g)
		st.Count = state.Int(len(active))
		st.SetObjects(objects.Distinct(g, 5))
		if err := e.update(ctx, hit.Channel, hit.TS, g, &st); err != nil {
			return "", err
		}
		e.reply(ctx, hit.Channel, hit.TS, change)
		e.put(key, entry{hit.Channel, hit.TS, st})
		return "updated", nil
	}
	if found && now-hit.St.ResolvedAt < e.Config.Incidents.ReopenWindow.Seconds() {
		defer l.Unlock()
		st := hit.St
		flaps := st.Firings() + 1
		st.State, st.Count, st.Flaps, st.ResolvedAt, st.FiredAt = "firing", state.Int(len(active)), state.Int(flaps), 0, now
		st.AckedBy, st.SilenceID = "", ""
		st.SetObjects(objects.Distinct(g, 5))
		e.Metrics.Incidents.WithLabelValues(timing(&st)...).Inc()
		if err := e.update(ctx, hit.Channel, hit.TS, g, &st); err != nil {
			return "", err
		}
		text := "🔁 Fired again, " + format.Ordinal(flaps) + " time in the last hour."
		if page {
			text += " <!channel>"
		}
		e.reply(ctx, hit.Channel, hit.TS, text)
		e.put(key, entry{hit.Channel, hit.TS, st})
		return "reopened", nil
	}

	start := 0.0
	for _, a := range active {
		if s := a.Start(); s != 0 && (start == 0 || s < start) {
			start = s
		}
	}
	fields, _ := json.Marshal(objects.ContractFields(g))
	st := state.Incident{
		GroupKey: key, State: "firing", Count: state.Int(len(active)), StartedAt: startOr(start, now),
		Alertname: g.Label("alertname"), GroupLabels: alert.SortedJSON(g.GroupLabels), Flaps: 1,
		Severity: g.Label("severity"), Receiver: g.Receiver, Team: g.Label(e.Config.Channels.TeamLabel),
		PostedAt: now, FiredAt: now, Fields: string(fields), WeekCount: state.Int(e.weekCount(ctx, channel, key) + 1),
	}
	st.SetObjects(objects.Distinct(g, 5))
	ch, ts, err := e.post(ctx, channel, g, &st, page)
	if err != nil {
		l.Unlock()
		return "", err
	}
	e.Metrics.Incidents.WithLabelValues(timing(&st)...).Inc()
	e.put(key, entry{ch, ts, st})
	l.Unlock()
	e.Spawn(func() { e.enrich(context.WithoutCancel(ctx), ch, ts, key, g) })
	return "posted", nil
}

func startOr(start, now float64) float64 {
	if start != 0 {
		return start
	}
	return now
}
