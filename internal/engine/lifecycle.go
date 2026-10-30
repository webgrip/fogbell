package engine

import (
	"context"
	"encoding/json"

	"github.com/webgrip/fogbell/internal/alert"
	"github.com/webgrip/fogbell/internal/events"
	"github.com/webgrip/fogbell/internal/format"
	"github.com/webgrip/fogbell/internal/render"
	"github.com/webgrip/fogbell/internal/slack"
	"github.com/webgrip/fogbell/internal/state"
	"github.com/webgrip/fogbell/internal/store"
)

// Step is what a notification does to its incident.
type Step int

// The steps of the incident lifecycle.
const (
	Post Step = iota
	Update
	Resolve
	Reopen
	Ignore
)

func (s Step) String() string {
	return [...]string{"posted", "updated", "resolved", "reopened", "ignored-resolve"}[s]
}

// Decide is the lifecycle as a table: the key's latest message (found, and whether it is open and
// when it resolved), the notification, and the reopen window give the step.
func Decide(found bool, latest *state.Incident, resolved bool, now, reopenWindow float64) Step {
	open := found && !latest.Resolved()
	switch {
	case resolved && open:
		return Resolve
	case resolved:
		return Ignore
	case open:
		return Update
	case found && now-latest.ResolvedAt < reopenWindow:
		return Reopen
	}
	return Post
}

// Notify handles one Alertmanager notification for a channel and says what it did. With byTeam, a
// group whose team has a channel of its own goes there; a team channel the app is not in falls back
// to channel, counted, so a team that has not invited the app yet still gets its alerts.
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
	unlock := e.Store.Lock(key)
	e.Store.SetGroup(key, g)
	hit, found := e.Store.Find(ctx, channel, key)
	step := Decide(found, &hit.State, g.Resolved(), now, e.Config.Incidents.ReopenWindow.Seconds())
	if step == Post || step == Ignore {
		if done := e.fold(ctx, key, g, now); done != "" {
			unlock()
			return done, nil
		}
	}
	if step != Post {
		defer unlock()
		return step.String(), e.apply(ctx, step, key, hit, g, now)
	}
	en, err := e.open(ctx, channel, key, g, now)
	unlock()
	if err != nil {
		return "", err
	}
	e.Spawn(func() { e.enrich(context.WithoutCancel(ctx), en, key, g) })
	return step.String(), nil
}

// apply runs a step on an existing incident.
func (e *Engine) apply(ctx context.Context, step Step, key string, en store.Entry, g *alert.Group, now float64) error {
	st := &en.State
	active := len(g.Active())
	var reply, kind string
	switch step {
	case Ignore:
		return nil
	case Resolve:
		e.Store.SetGroup(key, nil)
		st.State, st.ResolvedAt, st.Count = "resolved", now, 0
		e.Metrics.ResolveSecs.WithLabelValues(timing(st)...).Observe(now - st.Shown(now))
		reply, kind = "✅ Resolved after "+format.Duration(now-startOr(st.StartedAt, now))+".", events.Resolved
	case Update:
		reply, kind = render.DescribeChange(int(st.Count), g), events.Updated
		st.Count = state.Int(active)
		st.SetObjects(e.Config.ObjectReader().Distinct(g, 5))
	case Reopen:
		flaps := st.Firings() + 1
		st.State, st.Count, st.Flaps, st.ResolvedAt, st.FiredAt = "firing", state.Int(active), state.Int(flaps), 0, now
		st.AckedBy, st.SilenceID, st.InvestigationDone, st.Investigation = "", "", false, ""
		st.SetObjects(e.Config.ObjectReader().Distinct(g, 5))
		e.Metrics.Incidents.WithLabelValues(timing(st)...).Inc()
		reply, kind = "🔁 Fired again, "+format.Ordinal(flaps)+" time in the last hour.", events.Reopened
		if e.Config.IsPage(g.Receiver) {
			reply += " <!channel>"
		}
	}
	if err := e.update(ctx, en, g); err != nil {
		return err
	}
	e.reply(ctx, en.Channel, en.TS, reply)
	e.Store.Put(key, en)
	e.emit(ctx, kind, en, "")
	return nil
}

// open posts a new incident.
func (e *Engine) open(ctx context.Context, channel, key string, g *alert.Group, now float64) (store.Entry, error) {
	objects := e.Config.ObjectReader()
	start := 0.0
	for _, a := range g.Active() {
		if s := a.Start(); s != 0 && (start == 0 || s < start) {
			start = s
		}
	}
	fields, _ := json.Marshal(objects.ContractFields(g))
	st := state.Incident{
		GroupKey: key, State: "firing", Count: state.Int(len(g.Active())), StartedAt: startOr(start, now),
		Alertname: g.Label("alertname"), GroupLabels: alert.SortedJSON(g.GroupLabels), Flaps: 1,
		Severity: g.Label("severity"), Receiver: g.Receiver, Team: g.Label(e.Config.Channels.TeamLabel),
		PostedAt: now, FiredAt: now, Fields: string(fields), WeekCount: state.Int(e.weekCount(ctx, channel, key) + 1),
	}
	st.SetObjects(objects.Distinct(g, 5))
	ch, ts, err := e.post(ctx, channel, g, &st, e.Config.IsPage(g.Receiver))
	if err != nil {
		return store.Entry{}, err
	}
	en := store.Entry{Channel: ch, TS: ts, State: st}
	e.Metrics.Incidents.WithLabelValues(timing(&st)...).Inc()
	e.Store.Put(key, en)
	e.emit(ctx, events.Opened, en, "")
	return en, nil
}

func (e *Engine) weekCount(ctx context.Context, channel, key string) int {
	n := 0
	for _, m := range e.Store.Incidents(ctx, channel, e.now()-7*86400) {
		if m.State.GroupKey == key {
			n++
		}
	}
	return n
}
