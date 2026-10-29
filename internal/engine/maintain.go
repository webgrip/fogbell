package engine

import (
	"context"
	"math"
	"os"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/webgrip/fogbell/internal/format"
	"github.com/webgrip/fogbell/internal/state"
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

// Maintain runs the time-driven work on open incidents: a silence about to end while the alert still
// fires, a page nobody picked up, and the redraw of a firing incident. It says what it did.
func (e *Engine) Maintain(ctx context.Context) []string {
	for _, name := range e.Config.AlertChannels() {
		e.scan(ctx, e.Slack.ChannelID(ctx, name))
	}
	now := e.now()
	e.mu.Lock()
	e.init()
	snapshot := make(map[string]entry, len(e.index))
	for k, v := range e.index {
		snapshot[k] = v
	}
	e.mu.Unlock()
	var done []string
	for key, en := range snapshot {
		st := en.St
		if st.Resolved() {
			continue
		}
		lead := 600.0
		if st.SilenceUntil-st.SilenceAt > 2*expiryWarning {
			lead = expiryWarning
		}
		if st.SilenceID != "" && st.ExpiryWarned != st.SilenceID && st.SilenceUntil-now > 0 && st.SilenceUntil-now <= lead {
			done = append(done, e.warnExpiry(ctx, key, en.Channel, en.TS, st.SilenceID))
		}
		if e.needsEscalation(&en.St, now) {
			done = append(done, e.escalate(ctx, key, en.Channel, en.TS))
		}
		refreshed := st.RefreshedAt
		if refreshed == 0 {
			refreshed = st.PostedAt
		}
		if (e.Prom != nil || e.Config.Enrich.Related) && st.State == "firing" && now-refreshed >= e.Config.Enrich.Refresh.Seconds() {
			done = append(done, e.refresh(ctx, key, en.Channel, en.TS))
		}
	}
	return done
}

// refresh redraws the sparkline and the also-firing line of a firing incident in place, and edits the
// message only when one changed. It needs the group in memory: after a restart that is the next
// notification, which Alertmanager repeats.
func (e *Engine) refresh(ctx context.Context, key, channel, ts string) string {
	g := e.group(key)
	if g == nil {
		return "unknown"
	}
	lines := map[string]string{}
	if line, err := e.spark(ctx, g); err != nil {
		e.Log.Warn("refresh spark failed", "error", err)
	} else if line != "" {
		lines["spark"] = line
	}
	if line, err := e.related(ctx, g); err != nil {
		e.Log.Warn("refresh related failed", "error", err)
	} else {
		lines["related"] = line
	}
	l := e.lock(key)
	l.Lock()
	defer l.Unlock()
	en, ok := e.get(key)
	if !ok || en.TS != ts || en.St.State != "firing" {
		return "changed"
	}
	st := en.St
	st.RefreshedAt = e.now()
	same := true
	for k, v := range lines {
		if (k == "spark" && st.Spark != v) || (k == "related" && st.Related != v) {
			same = false
		}
	}
	if same {
		e.put(key, entry{channel, ts, st})
		e.Metrics.Refreshes.WithLabelValues("same").Inc()
		return "same"
	}
	setLines(&st, lines)
	if err := e.update(ctx, channel, ts, g, &st); err != nil {
		e.Log.Warn("refresh update failed", "error", err)
		return "error"
	}
	e.put(key, entry{channel, ts, st})
	e.Metrics.Refreshes.WithLabelValues("edited").Inc()
	return "refreshed"
}

func (e *Engine) needsEscalation(st *state.Incident, now float64) bool {
	if !e.Config.IsPage(st.Receiver) || st.AckedBy != "" || st.SilenceID != "" {
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

func (e *Engine) escalate(ctx context.Context, key, channel, ts string) string {
	l := e.lock(key)
	l.Lock()
	defer l.Unlock()
	msg := e.Slack.Message(ctx, channel, ts)
	en, _ := e.get(key)
	st := en.St
	if msg != nil {
		if cur, ok := e.incident(msg); ok {
			st = *cur
		}
	}
	now := e.now()
	if !e.needsEscalation(&st, now) {
		return "changed"
	}
	h, m, _ := e.Config.HandoverClock()
	person := OnCall(e.Roster(), e.Now(), e.Config.Location(), h, m)
	shown := fired(&st, 0)
	st.Escalated = shown
	e.reply(ctx, channel, ts, "📟 <@"+person+">, you are on call this week: nobody has picked this up in "+
		format.Duration(now-shown)+". Press 👀 I'm on it, or hand it on in this thread.")
	e.Metrics.Escalations.WithLabelValues(st.Alertname).Inc()
	if msg != nil {
		e.restate(ctx, channel, ts, msg, &st)
	}
	e.put(key, entry{channel, ts, st})
	return "escalated"
}

func (e *Engine) warnExpiry(ctx context.Context, key, channel, ts, sid string) string {
	l := e.lock(key)
	l.Lock()
	defer l.Unlock()
	msg := e.Slack.Message(ctx, channel, ts)
	en, _ := e.get(key)
	st := en.St
	if msg != nil {
		if cur, ok := e.incident(msg); ok {
			st = *cur
		}
	}
	if st.SilenceID != sid || st.Resolved() {
		return "changed"
	}
	active, err := e.AM.SilenceActive(ctx, sid)
	if err != nil {
		e.Log.Warn("silence unreadable; asking again next round", "silence", sid, "error", err)
		return "unknown"
	}
	st.ExpiryWarned = sid
	if active {
		e.reply(ctx, channel, ts, "⏳ The silence <@"+st.SilenceBy+"> set ends at "+format.Date(st.SilenceUntil, format.Time, format.TimeFallback)+
			", in "+format.Duration(st.SilenceUntil-e.now())+", and this still fires. Silence it again, or fix the cause.")
	}
	if msg != nil {
		e.restate(ctx, channel, ts, msg, &st)
	}
	e.put(key, entry{channel, ts, st})
	if active {
		return "warned"
	}
	return "gone"
}
