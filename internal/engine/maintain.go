package engine

import (
	"context"
	"math"
	"os"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/webgrip/fogbell/internal/format"
	"github.com/webgrip/fogbell/internal/state"
	"github.com/webgrip/fogbell/internal/store"
)

const expiryWarning = 3600

// Roster is the on-call rotation: one person a week, in order, handing over at onCall.handoverAt on
// the weekday of RotationStart.
type Roster struct {
	People               []string `yaml:"people" json:"people"`
	RotationStart        string   `yaml:"rotation_start" json:"rotation_start"`
	EscalateAfterMinutes int      `yaml:"escalate_after_minutes" json:"escalate_after_minutes"`
}

func (e *Engine) readRoster() Roster {
	path := e.Config.OnCall.RosterFile
	if path == "" {
		return Roster{}
	}
	raw, err := os.ReadFile(path)
	var r Roster
	if err == nil {
		err = yaml.Unmarshal(raw, &r)
	}
	if err != nil {
		e.Metrics.Degraded.WithLabelValues("roster").Inc()
		e.Log.Warn("roster unreadable; no escalation", "error", err)
		return Roster{}
	}
	return r
}

// OnCall is the Slack user on call at now: the roster's people in turn, a week each, handing over at
// hour:minute in loc on the day of rotation_start. "" without people or a valid start.
func OnCall(r Roster, now time.Time, loc *time.Location, hour, minute int) string {
	if len(r.People) == 0 {
		return ""
	}
	day, err := time.ParseInLocation("2006-01-02", r.RotationStart, loc)
	if err != nil {
		return ""
	}
	start := time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 0, 0, loc)
	weeks := int(math.Floor(now.Sub(start).Hours() / (7 * 24)))
	n := len(r.People)
	return r.People[((weeks%n)+n)%n]
}

// task is time-driven work on one open incident. due says whether it applies now; run does it under
// the incident's lock with the message's current state and says what it did.
type task struct {
	due func(st *state.Incident, now float64) bool
	run func(ctx context.Context, en *store.Entry, now float64) string
}

func (e *Engine) tasks() []task {
	return []task{
		{e.expiryDue, e.warnExpiry},
		{e.escalationDue, e.escalate},
		{e.investigationDue, e.collectFindings},
	}
}

// Maintain runs the time-driven work on open incidents: a silence about to end while the alert still
// fires, a page nobody picked up, an investigation's findings, and the redraw of a firing incident.
// It says what it did.
func (e *Engine) Maintain(ctx context.Context) []string {
	for _, name := range e.Config.AlertChannels() {
		e.Store.Scan(ctx, e.Slack.ChannelID(ctx, name))
	}
	now := e.now()
	var done []string
	e.Store.Each(func(key string, en store.Entry) {
		if en.State.Resolved() && !e.investigationDue(&en.State, now) {
			return
		}
		for _, t := range e.tasks() {
			if t.due(&en.State, now) {
				done = append(done, e.locked(ctx, key, en, t))
			}
		}
		if e.refreshDue(&en.State, now) {
			done = append(done, e.refresh(ctx, key, en))
		}
	})
	return done
}

// locked re-reads the message under the incident's lock and runs a task on its current state, if it
// still applies; the result is restated on the message and kept.
func (e *Engine) locked(ctx context.Context, key string, en store.Entry, t task) string {
	defer e.Store.Lock(key)()
	msg := e.Slack.Message(ctx, en.Channel, en.TS)
	if cur, ok := e.Store.Get(key); ok && cur.TS == en.TS {
		en = cur
	}
	if msg != nil {
		if st, ok := e.Store.Decode(msg); ok {
			en.State = *st
		}
	}
	now := e.now()
	if !t.due(&en.State, now) {
		return "changed"
	}
	done := t.run(ctx, &en, now)
	if msg != nil {
		e.restate(ctx, en, msg)
	}
	e.Store.Put(key, en)
	return done
}

func (e *Engine) refreshDue(st *state.Incident, now float64) bool {
	since := st.RefreshedAt
	if since == 0 {
		since = st.PostedAt
	}
	return st.State == "firing" && (e.up.Prom != nil || e.Config.Enrich.Related) && now-since >= e.Config.Enrich.Refresh.Seconds()
}

// expiryDue: a silence made from the message ends within the hour (ten minutes for a silence of two
// hours or less, or the warning would follow the press), not warned about yet.
func (e *Engine) expiryDue(st *state.Incident, now float64) bool {
	lead := 600.0
	if st.SilenceUntil-st.SilenceAt > 2*expiryWarning {
		lead = expiryWarning
	}
	left := st.SilenceUntil - now
	return !st.Resolved() && st.SilenceID != "" && st.ExpiryWarned != st.SilenceID && left > 0 && left <= lead
}

func (e *Engine) warnExpiry(ctx context.Context, en *store.Entry, now float64) string {
	st := &en.State
	active, err := e.AM.SilenceActive(ctx, st.SilenceID)
	if err != nil {
		e.Log.Warn("silence unreadable; asking again next round", "silence", st.SilenceID, "error", err)
		return "unknown"
	}
	st.ExpiryWarned = st.SilenceID
	if !active {
		return "gone"
	}
	e.reply(ctx, en.Channel, en.TS, "⏳ The silence <@"+st.SilenceBy+"> set ends at "+format.Date(st.SilenceUntil, format.Time, format.TimeFallback)+
		", in "+format.Duration(st.SilenceUntil-now)+", and this still fires. Silence it again, or fix the cause.")
	return "warned"
}

// escalationDue: a page nobody picked up or silenced within the roster's minutes, not escalated yet
// for this firing.
func (e *Engine) escalationDue(st *state.Incident, now float64) bool {
	if st.Resolved() || !e.Config.IsPage(st.Receiver) || st.AckedBy != "" || st.SilenceID != "" {
		return false
	}
	shown := fired(st, 0)
	if shown == 0 || st.Escalated == shown {
		return false
	}
	r := e.Roster()
	after := r.EscalateAfterMinutes
	if after == 0 {
		after = 15
	}
	return len(r.People) > 0 && now-shown >= float64(60*after)
}

func (e *Engine) escalate(ctx context.Context, en *store.Entry, now float64) string {
	st := &en.State
	h, m, _ := e.Config.HandoverClock()
	person := OnCall(e.Roster(), e.Now(), e.Config.Location(), h, m)
	shown := fired(st, 0)
	st.Escalated = shown
	e.reply(ctx, en.Channel, en.TS, "📟 <@"+person+">, you are on call this week: nobody has picked this up in "+
		format.Duration(now-shown)+". Press 👀 I'm on it, or hand it on in this thread.")
	e.Metrics.Escalations.WithLabelValues(st.Alertname).Inc()
	return "escalated"
}

// investigationDue: an investigation is running and its findings are not posted yet.
func (e *Engine) investigationDue(st *state.Incident, _ float64) bool {
	return st.Investigation != "" && !st.InvestigationDone && e.Investigator != nil
}

func (e *Engine) collectFindings(ctx context.Context, en *store.Entry, _ float64) string {
	st := &en.State
	s, err := e.Investigator.Status(ctx, st.Investigation)
	if err != nil {
		e.Log.Warn("investigation unreadable; asking again next round", "id", st.Investigation, "error", err)
		return "unknown"
	}
	if s.URL != "" {
		st.InvestigationURL = s.URL
	}
	if !s.Done {
		return "investigating"
	}
	st.InvestigationDone = true
	text := "🔎 " + e.Investigator.Name() + " finished the investigation <@" + st.InvestigationBy + "> asked for (" + s.State + ")"
	if s.URL != "" {
		text += ": <" + s.URL + "|the work item>"
	}
	if s.Findings == "" {
		text += ". It reported no findings."
	} else {
		text += ".\n" + format.Cut(s.Findings, 2800) + "\n_An agent wrote this. Check it before you act on it._"
	}
	e.reply(ctx, en.Channel, en.TS, text)
	return "investigated"
}
