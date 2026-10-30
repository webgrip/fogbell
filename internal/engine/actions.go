package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/webgrip/fogbell/internal/events"
	"github.com/webgrip/fogbell/internal/format"
	"github.com/webgrip/fogbell/internal/investigate"
	"github.com/webgrip/fogbell/internal/render"
	"github.com/webgrip/fogbell/internal/slack"
	"github.com/webgrip/fogbell/internal/state"
	"github.com/webgrip/fogbell/internal/store"
	"github.com/webgrip/fogbell/internal/upstream"
)

// Silences maps a silence choice to its length in seconds.
var Silences = map[string]float64{"1h": 3600, "24h": 86400, "7d": 7 * 86400}

// User is who pressed.
type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
}

func (u User) display() string {
	switch {
	case u.Username != "":
		return u.Username
	case u.Name != "":
		return u.Name
	}
	return "someone"
}

// Interaction is the part of a Slack interaction payload fogbell reads.
type Interaction struct {
	Type      string `json:"type"`
	TriggerID string `json:"trigger_id"`
	User      User   `json:"user"`
	Channel   struct {
		ID string `json:"id"`
	} `json:"channel"`
	Container struct {
		MessageTS string `json:"message_ts"`
	} `json:"container"`
	Message *slack.Message `json:"message"`
	Actions []struct {
		ActionID       string        `json:"action_id"`
		SelectedOption *slack.Option `json:"selected_option"`
	} `json:"actions"`
	View *View `json:"view"`
}

// View is a submitted modal.
type View struct {
	CallbackID      string `json:"callback_id"`
	PrivateMetadata string `json:"private_metadata"`
	State           struct {
		Values map[string]map[string]struct {
			Value string `json:"value"`
		} `json:"values"`
	} `json:"state"`
}

// press is one button press on an incident, under its lock, with the message's current state.
type press struct {
	en     store.Entry
	msg    *slack.Message
	user   User
	value  string
	reason string
	now    float64
}

func (p *press) st() *state.Incident { return &p.en.State }

func (p *press) link() string { return format.Permalink(p.en.Channel, p.en.TS) }

// outcome is what an action did: its result word, the thread line, and the event it emits. An empty
// note leaves the message as it was.
type outcome struct {
	result, note, event string
}

type action func(ctx context.Context, p *press) outcome

func (e *Engine) registry() map[string]action {
	return map[string]action{
		"ack":         e.ack,
		"silence":     e.silence,
		"unsilence":   e.unsilence,
		"noise":       e.noise,
		"ticket":      e.ticket,
		"postmortem":  e.postmortem,
		"investigate": e.investigate,
	}
}

// Interact handles one Slack interaction: a button or select on an incident message, or the submitted
// "why?" modal of a silence.
func (e *Engine) Interact(ctx context.Context, p *Interaction) string {
	if p.Type == "view_submission" {
		return e.submitView(ctx, p)
	}
	actionID, value := "", ""
	if len(p.Actions) > 0 {
		actionID = p.Actions[0].ActionID
		if o := p.Actions[0].SelectedOption; o != nil {
			value = o.Value
		}
	}
	if actionID == "more" {
		actionID, value = value, ""
	}
	if strings.HasPrefix(actionID, "link_") {
		return "link"
	}
	ts := p.Container.MessageTS
	if ts == "" && p.Message != nil {
		ts = p.Message.TS
	}
	if actionID == "silence" && p.TriggerID != "" && e.askReason(ctx, p.TriggerID, p.Channel.ID, ts, value) {
		e.Metrics.Interactions.WithLabelValues("silence_asked").Inc()
		return "silence-asked"
	}
	return e.act(ctx, p.Channel.ID, ts, p.User, actionID, value, p.Message, "")
}

func (e *Engine) act(ctx context.Context, channel, ts string, user User, actionID, value string, fallback *slack.Message, reason string) string {
	run, known := e.actions[actionID]
	if !known {
		return "unknown-action"
	}
	msg := e.Slack.Message(ctx, channel, ts)
	if msg == nil {
		msg = fallback
	}
	if msg == nil {
		return "not-an-incident"
	}
	st, ok := e.Store.Decode(msg)
	if !ok {
		return "not-an-incident"
	}
	defer e.Store.Lock(st.GroupKey)()
	if fresh := e.Slack.Message(ctx, channel, ts); fresh != nil {
		if cur, ok := e.Store.Decode(fresh); ok {
			msg, st = fresh, cur
		}
	}
	p := &press{en: store.Entry{Channel: channel, TS: ts, State: *st}, msg: msg, user: user, value: value, reason: reason, now: e.now()}
	out := run(ctx, p)
	if out.note == "" {
		return out.result
	}
	e.restate(ctx, p.en, msg)
	e.reply(ctx, channel, ts, out.note)
	e.Store.Put(p.en.State.GroupKey, p.en)
	e.Metrics.Interactions.WithLabelValues(actionID).Inc()
	if out.event != "" {
		e.emit(ctx, out.event, p.en, user.ID)
	}
	return out.result
}

func (e *Engine) ack(_ context.Context, p *press) outcome {
	st := p.st()
	if st.AckedBy == "" && !st.Resolved() {
		e.Metrics.AckSeconds.WithLabelValues(timing(st)...).Observe(p.now - fired(st, p.now))
	}
	st.AckedBy = p.user.ID
	return outcome{"ack", "👀 <@" + p.user.ID + "> is on it.", events.Acknowledged}
}

func (e *Engine) silence(ctx context.Context, p *press) outcome {
	st := p.st()
	if st.SilenceID != "" {
		return outcome{result: "already-silenced"}
	}
	value := p.value
	if value == "" {
		value = "1h"
	}
	choice, target, _ := strings.Cut(value, "|")
	seconds, ok := Silences[choice]
	if !ok {
		seconds = 3600
	}
	labels := SilenceMatchers(st, target)
	id, err := e.AM.CreateSilence(ctx, newSilence(labels, p.now, seconds, p.user.display(), p.link(), p.reason))
	if err != nil {
		e.Metrics.Interactions.WithLabelValues("error").Inc()
		e.Log.Warn("silence failed", "error", err)
		e.reply(ctx, p.en.Channel, p.en.TS, "🔕 Alertmanager did not take the silence <@"+p.user.ID+"> asked for. Try again, or silence it there.")
		return outcome{result: "silence-failed"}
	}
	until := p.now + seconds
	st.SilenceID, st.SilenceUntil, st.SilenceBy, st.SilenceReason, st.SilenceAt = id, until, p.user.ID, format.Cut(p.reason, 200), p.now
	note := "🔕 <@" + p.user.ID + "> silenced this for " + choice + ", until " + format.Date(until, format.Long, format.LongFallback) +
		". Matchers: " + render.MatcherText(labels)
	if p.reason != "" {
		note += "\nWhy: _" + format.Escape(p.reason) + "_"
	}
	return outcome{"silence", note, events.Silenced}
}

func (e *Engine) unsilence(ctx context.Context, p *press) outcome {
	st := p.st()
	if st.SilenceID != "" {
		if err := e.AM.ExpireSilence(ctx, st.SilenceID); err != nil {
			e.Log.Warn("expire silence failed", "error", err)
		}
	}
	st.SilenceID, st.SilenceUntil, st.SilenceBy, st.SilenceReason = "", 0, "", ""
	return outcome{"unsilence", "🔔 <@" + p.user.ID + "> lifted the silence.", events.Unsilenced}
}

func (e *Engine) noise(_ context.Context, p *press) outcome {
	st := p.st()
	var voters []string
	for _, v := range strings.Split(st.NoiseBy, ",") {
		if v == p.user.ID {
			return outcome{result: "noise-already"}
		}
		if v != "" {
			voters = append(voters, v)
		}
	}
	voters = append(voters, p.user.ID)
	st.NoiseBy, st.NoiseVotes = strings.Join(voters, ","), state.Int(len(voters))
	e.Metrics.Noise.WithLabelValues(st.Alertname).Inc()
	return outcome{"noise", fmt.Sprintf("🙈 <@%s> says this alert is noise (%d vote%s). It counts toward the weekly noise report.",
		p.user.ID, len(voters), format.Plural(len(voters))), ""}
}

func (e *Engine) postmortem(ctx context.Context, p *press) outcome {
	if !p.st().Resolved() {
		return outcome{result: "not-resolved"}
	}
	draft := Postmortem(p.st(), e.thread(ctx, p.en.Channel, p.en.TS), p.en.Channel, p.en.TS, p.now)
	e.reply(ctx, p.en.Channel, p.en.TS, "📝 Postmortem draft for <@"+p.user.ID+">, from this thread. Copy it into a document and finish it; "+
		"nothing in it was checked by a person.\n```\n"+draft+"\n```")
	e.Metrics.Interactions.WithLabelValues("postmortem").Inc()
	return outcome{result: "postmortem"}
}

// ticket files a ticket, assigned to who pressed when the tracker knows their Slack address.
func (e *Engine) ticket(ctx context.Context, p *press) outcome {
	st := p.st()
	if st.TicketURL != "" {
		return outcome{"ticket", "📋 There is a ticket already: <" + st.TicketURL + "|" + e.trackerName() + ">", ""}
	}
	if !e.features().Ticket {
		return outcome{result: "ticket-unavailable"}
	}
	name, markdown := TicketText(st, p.link(), p.user.display(), p.now)
	member := e.member(ctx, p.user.ID)
	t, err := e.Tracker.Create(ctx, name, markdown, member)
	if err != nil {
		e.Metrics.Interactions.WithLabelValues("error").Inc()
		e.Log.Warn("ticket create failed", "tracker", e.Tracker.Name(), "error", err)
		e.reply(ctx, p.en.Channel, p.en.TS, "📋 "+e.Tracker.Name()+" did not create the ticket <@"+p.user.ID+"> asked for ("+
			format.Cut(format.Escape(err.Error()), 200)+"). Try again, or create it by hand.")
		return outcome{result: "ticket-failed"}
	}
	st.TicketURL, st.TicketID = t.URL, t.ID
	note := "📋 <@" + p.user.ID + "> opened a ticket: <" + t.URL + "|" + format.Escape(name) + ">"
	if member == "" {
		note += " (not assigned: no " + e.Tracker.Name() + " member with their Slack address)"
	}
	return outcome{"ticket", note, events.Ticketed}
}

// investigate hands the incident to the investigator, once per firing.
func (e *Engine) investigate(ctx context.Context, p *press) outcome {
	st := p.st()
	if st.Investigation != "" {
		return outcome{result: "investigation-running"}
	}
	if !e.features().Investigate {
		return outcome{result: "investigate-unavailable"}
	}
	title, brief := InvestigationBrief(st, p.link(), e.Store.Group(st.GroupKey))
	id, err := e.Investigator.Start(ctx, investigate.Request{ID: st.GroupKey + "@" + fmt.Sprint(int64(st.Shown(p.now))), Title: title, Description: brief})
	if err != nil {
		e.Metrics.Interactions.WithLabelValues("error").Inc()
		e.Log.Warn("investigation not started", "investigator", e.Investigator.Name(), "error", err)
		why := "it answered with an error"
		if errors.Is(err, investigate.ErrRefused) {
			why = "it refused, most likely a spent allowance"
		}
		e.reply(ctx, p.en.Channel, p.en.TS, "🔎 "+e.Investigator.Name()+" did not start the investigation <@"+p.user.ID+"> asked for: "+why+".")
		return outcome{result: "investigate-failed"}
	}
	st.Investigation, st.InvestigationBy, st.InvestigationDone = id, p.user.ID, false
	return outcome{"investigate", "🔎 <@" + p.user.ID + "> asked " + e.Investigator.Name() + " to investigate. Its findings land in this thread.", events.Investigated}
}

func (e *Engine) trackerName() string {
	if e.Tracker != nil {
		return e.Tracker.Name()
	}
	return "ticket"
}

// member is the tracker user behind a Slack user, matched on email address; "" when Slack withholds the
// address or the tracker has no such member. A ticket without an assignee beats no ticket.
func (e *Engine) member(ctx context.Context, uid string) string {
	email, err := e.Slack.UserEmail(ctx, uid)
	if err == nil && email != "" {
		var m string
		if m, err = e.Tracker.Member(ctx, email); err == nil {
			return m
		}
	}
	if err != nil {
		e.Log.Warn("assignee lookup failed", "error", err)
	}
	return ""
}

func (e *Engine) thread(ctx context.Context, channel, ts string) []slack.Message {
	msgs, err := e.Slack.Replies(ctx, channel, ts)
	if err != nil {
		e.Metrics.Degraded.WithLabelValues("thread_read").Inc()
		e.Log.Warn("conversations.replies unavailable", "error", err)
		return nil
	}
	return msgs
}

type viewMeta struct {
	C string `json:"c"`
	T string `json:"t"`
	V string `json:"v"`
}

// askReason opens the "why?" modal of a silence. False when Slack refuses it (an expired trigger, an
// outage); the caller then silences without a reason. It runs first: a trigger lives three seconds.
func (e *Engine) askReason(ctx context.Context, trigger, channel, ts, value string) bool {
	choice, _, _ := strings.Cut(value, "|")
	if choice == "" {
		choice = "1h"
	}
	label := choice
	for _, c := range render.SilenceChoices {
		if c.Value == choice {
			label = c.Label
		}
	}
	meta, _ := json.Marshal(viewMeta{C: channel, T: ts, V: value})
	view := map[string]any{
		"type": "modal", "callback_id": "silence_reason", "private_metadata": string(meta),
		"title": slack.PlainText("Silence"), "submit": slack.PlainText("Silence " + label), "close": slack.PlainText("Cancel"),
		"blocks": []map[string]any{{
			"type": "input", "block_id": "reason", "label": slack.PlainText("Why?"),
			"hint": slack.PlainText("Becomes the silence's comment in Alertmanager and a line in the thread."),
			"element": map[string]any{"type": "plain_text_input", "action_id": "reason", "multiline": true, "max_length": 300,
				"placeholder": slack.PlainText("Known, fix merged, waiting for the rollout")},
		}},
	}
	if err := e.Slack.OpenView(ctx, trigger, view); err != nil {
		e.Metrics.Degraded.WithLabelValues("reason_modal").Inc()
		e.Log.Warn("views.open failed; silencing without a reason", "error", err)
		return false
	}
	return true
}

func (e *Engine) submitView(ctx context.Context, p *Interaction) string {
	if p.View == nil || p.View.CallbackID != "silence_reason" {
		return "unknown-view"
	}
	var m viewMeta
	_ = json.Unmarshal([]byte(p.View.PrivateMetadata), &m)
	reason := strings.Join(strings.Fields(p.View.State.Values["reason"]["reason"].Value), " ")
	return e.act(ctx, m.C, m.T, p.User, "silence", m.V, nil, reason)
}

// SilenceMatchers is the group's labels, plus one object's when target is its index.
func SilenceMatchers(st *state.Incident, target string) map[string]string {
	labels := st.LabelSet()
	var i int
	if _, err := fmt.Sscan(target, &i); err == nil {
		if objs := st.ObjectList(); i >= 0 && i < len(objs) {
			for k, v := range objs[i].Labels {
				labels[k] = v
			}
		}
	}
	return labels
}

func newSilence(labels map[string]string, now, seconds float64, user, link, reason string) upstream.Silence {
	origin := "from Slack by " + user + ": " + link
	comment := strings.ToUpper(origin[:1]) + origin[1:]
	if reason != "" {
		comment = reason + " (" + origin + ")"
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	s := upstream.Silence{CreatedBy: "slack:" + user, Comment: comment, StartsAt: rfc3339(now), EndsAt: rfc3339(now + seconds)}
	for _, k := range keys {
		s.Matchers = append(s.Matchers, upstream.Matcher{Name: k, Value: labels[k], IsEqual: true})
	}
	return s
}

func rfc3339(ts float64) string { return time.Unix(0, int64(ts*1e9)).UTC().Format(time.RFC3339Nano) }

func fired(st *state.Incident, now float64) float64 {
	switch {
	case st.FiredAt != 0:
		return st.FiredAt
	case st.PostedAt != 0:
		return st.PostedAt
	}
	return now
}
