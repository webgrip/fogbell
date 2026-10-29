package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fogbell-dev/fogbell/internal/format"
	"github.com/fogbell-dev/fogbell/internal/render"
	"github.com/fogbell-dev/fogbell/internal/slack"
	"github.com/fogbell-dev/fogbell/internal/state"
	"github.com/fogbell-dev/fogbell/internal/upstream"
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
	channel := p.Channel.ID
	ts := p.Container.MessageTS
	if ts == "" && p.Message != nil {
		ts = p.Message.TS
	}
	if actionID == "silence" && p.TriggerID != "" && e.askReason(ctx, p.TriggerID, channel, ts, value) {
		e.Metrics.Interactions.WithLabelValues("silence_asked").Inc()
		return "silence-asked"
	}
	return e.act(ctx, channel, ts, p.User, actionID, value, p.Message, "")
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
		"title":  slack.PlainText("Silence"),
		"submit": slack.PlainText("Silence " + label),
		"close":  slack.PlainText("Cancel"),
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
	if i, err := strconv.Atoi(target); err == nil {
		if objs := st.ObjectList(); i >= 0 && i < len(objs) {
			for k, v := range objs[i].Labels {
				labels[k] = v
			}
		}
	}
	return labels
}

func (e *Engine) silence(ctx context.Context, labels map[string]string, seconds float64, user, link, reason string) (string, float64, error) {
	now := e.now()
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
	s := upstream.Silence{CreatedBy: "slack:" + user, Comment: comment,
		StartsAt: rfc3339(now), EndsAt: rfc3339(now + seconds)}
	for _, k := range keys {
		s.Matchers = append(s.Matchers, upstream.Matcher{Name: k, Value: labels[k], IsEqual: true})
	}
	id, err := e.AM.CreateSilence(ctx, s)
	return id, now + seconds, err
}

func rfc3339(ts float64) string {
	return time.Unix(0, int64(ts*1e9)).UTC().Format(time.RFC3339Nano)
}

func (e *Engine) act(ctx context.Context, channel, ts string, user User, actionID, value string, fallback *slack.Message, reason string) string {
	msg := e.Slack.Message(ctx, channel, ts)
	if msg == nil {
		msg = fallback
	}
	if msg == nil {
		return "not-an-incident"
	}
	st0, ok := e.incident(msg)
	if !ok {
		return "not-an-incident"
	}
	key := st0.GroupKey
	l := e.lock(key)
	l.Lock()
	defer l.Unlock()
	if fresh := e.Slack.Message(ctx, channel, ts); fresh != nil {
		msg = fresh
	}
	st := *st0
	if cur, ok := e.incident(msg); ok {
		st = *cur
	}
	uid, uname := user.ID, user.display()
	link := format.Permalink(channel, ts)
	now := e.now()
	var note string
	switch actionID {
	case "ack":
		if st.AckedBy == "" && !st.Resolved() {
			e.Metrics.AckSeconds.WithLabelValues(timing(&st)...).Observe(now - fired(&st, now))
		}
		st.AckedBy = uid
		note = "👀 <@" + uid + "> is on it."
	case "silence":
		if st.SilenceID != "" {
			return "already-silenced"
		}
		if value == "" {
			value = "1h"
		}
		choice, target, _ := strings.Cut(value, "|")
		seconds, ok := Silences[choice]
		if !ok {
			seconds = 3600
		}
		labels := SilenceMatchers(&st, target)
		id, until, err := e.silence(ctx, labels, seconds, uname, link, reason)
		if err != nil {
			e.Metrics.Interactions.WithLabelValues("error").Inc()
			e.Log.Warn("silence failed", "error", err)
			e.reply(ctx, channel, ts, "🔕 Alertmanager did not take the silence <@"+uid+"> asked for. Try again, or silence it there.")
			return "silence-failed"
		}
		st.SilenceID, st.SilenceUntil, st.SilenceBy, st.SilenceReason, st.SilenceAt = id, until, uid, format.Cut(reason, 200), now
		note = "🔕 <@" + uid + "> silenced this for " + choice + ", until " + format.Date(until, format.Long, format.LongFallback) +
			". Matchers: " + render.MatcherText(labels)
		if reason != "" {
			note += "\nWhy: _" + format.Escape(reason) + "_"
		}
	case "ticket":
		done, text := e.ticket(ctx, &st, channel, ts, user)
		if text == "" {
			return done
		}
		note = text
	case "noise":
		var voters []string
		for _, v := range strings.Split(st.NoiseBy, ",") {
			if v != "" {
				voters = append(voters, v)
			}
		}
		for _, v := range voters {
			if v == uid {
				return "noise-already"
			}
		}
		voters = append(voters, uid)
		st.NoiseBy, st.NoiseVotes = strings.Join(voters, ","), state.Int(len(voters))
		e.Metrics.Noise.WithLabelValues(st.Alertname).Inc()
		note = fmt.Sprintf("🙈 <@%s> says this alert is noise (%d vote%s). It counts toward the weekly noise report.",
			uid, len(voters), format.Plural(len(voters)))
	case "postmortem":
		if !st.Resolved() {
			return "not-resolved"
		}
		draft := Postmortem(&st, e.thread(ctx, channel, ts), channel, ts, now)
		e.reply(ctx, channel, ts, "📝 Postmortem draft for <@"+uid+">, from this thread. Copy it into a document and finish it; "+
			"nothing in it was checked by a person.\n```\n"+draft+"\n```")
		e.Metrics.Interactions.WithLabelValues(actionID).Inc()
		return actionID
	case "unsilence":
		if st.SilenceID != "" {
			if err := e.AM.ExpireSilence(ctx, st.SilenceID); err != nil {
				e.Log.Warn("expire silence failed", "error", err)
			}
		}
		st.SilenceID, st.SilenceUntil, st.SilenceBy, st.SilenceReason = "", 0, "", ""
		note = "🔔 <@" + uid + "> lifted the silence."
	default:
		return "unknown-action"
	}
	e.restate(ctx, channel, ts, msg, &st)
	e.reply(ctx, channel, ts, note)
	e.put(key, entry{channel, ts, st})
	e.Metrics.Interactions.WithLabelValues(actionID).Inc()
	return actionID
}

func fired(st *state.Incident, now float64) float64 {
	switch {
	case st.FiredAt != 0:
		return st.FiredAt
	case st.PostedAt != 0:
		return st.PostedAt
	}
	return now
}

// ticket files a ticket for the incident, assigned to who pressed when the tracker knows their Slack
// address. It returns what was done, and the thread line when the message changes.
func (e *Engine) ticket(ctx context.Context, st *state.Incident, channel, ts string, user User) (string, string) {
	if st.TicketURL != "" {
		name := "ticket"
		if e.Tracker != nil {
			name = e.Tracker.Name()
		}
		return "ticket", "📋 There is a ticket already: <" + st.TicketURL + "|" + name + ">"
	}
	if e.Tracker == nil || !e.Tracker.Available() {
		return "ticket-unavailable", ""
	}
	name, markdown := TicketText(st, format.Permalink(channel, ts), user.display(), e.now())
	member := e.member(ctx, user.ID)
	t, err := e.Tracker.Create(ctx, name, markdown, member)
	if err != nil {
		e.Metrics.Interactions.WithLabelValues("error").Inc()
		e.Log.Warn("ticket create failed", "tracker", e.Tracker.Name(), "error", err)
		e.reply(ctx, channel, ts, "📋 "+e.Tracker.Name()+" did not create the ticket <@"+user.ID+"> asked for ("+
			format.Cut(format.Escape(err.Error()), 200)+"). Try again, or create it by hand.")
		return "ticket-failed", ""
	}
	st.TicketURL, st.TicketID = t.URL, t.ID
	note := "📋 <@" + user.ID + "> opened a ticket: <" + t.URL + "|" + format.Escape(name) + ">"
	if member == "" {
		note += " (not assigned: no " + e.Tracker.Name() + " member with their Slack address)"
	}
	return "ticket", note
}

// member is the tracker user behind a Slack user, matched on email address; "" when Slack withholds the
// address or the tracker has no such member. A ticket without an assignee beats no ticket.
func (e *Engine) member(ctx context.Context, uid string) string {
	email, err := e.Slack.UserEmail(ctx, uid)
	if err != nil || email == "" {
		if err != nil {
			e.Log.Warn("assignee lookup failed", "error", err)
		}
		return ""
	}
	m, err := e.Tracker.Member(ctx, email)
	if err != nil {
		e.Log.Warn("assignee lookup failed", "error", err)
		return ""
	}
	return m
}

// TicketText is the name and markdown description of an incident's ticket: the alert's own fields and
// the thread link, nothing invented.
func TicketText(st *state.Incident, link, who string, now float64) (string, string) {
	f := st.FieldMap()
	headline := f["summary"]
	if headline == "" {
		headline = st.Alertname
	}
	if headline == "" {
		headline = "Alert"
	}
	name := headline
	if f["subject"] != "" {
		name += ": " + f["subject"]
	}
	severity := st.Severity
	if severity == "" {
		severity = "no severity"
	}
	lines := []string{"Opened from a Slack alert by " + who + " on " + time.Unix(int64(now), 0).UTC().Format("2006-01-02 15:04 UTC") + ".", "",
		"- **Alert:** `" + st.Alertname + "` (" + severity + ")"}
	if f["subject"] != "" {
		lines = append(lines, "- **Subject:** "+f["subject"])
	}
	lines = append(lines, "- **Slack thread:** "+link)
	for _, k := range []struct{ key, label string }{{"runbook_url", "Runbook"}, {"dashboard_url", "Dashboard"}} {
		if f[k.key] != "" {
			lines = append(lines, "- **"+k.label+":** "+f[k.key])
		}
	}
	for _, k := range []struct{ key, label string }{{"description", "What is wrong"}, {"impact", "Impact"}, {"action", "First move"}} {
		if f[k.key] != "" {
			lines = append(lines, "", "## "+k.label, "", f[k.key])
		}
	}
	return format.Cut(name, 250), strings.Join(lines, "\n")
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

// restate rewrites the state line and the buttons of an existing message and keeps the rest.
func (e *Engine) restate(ctx context.Context, channel, ts string, msg *slack.Message, st *state.Incident) {
	if len(msg.Attachments) == 0 {
		return
	}
	att := msg.Attachments[0]
	var blocks []slack.Block
	for _, b := range att.Blocks {
		if b.BlockID != "state" {
			blocks = append(blocks, b)
		}
	}
	resolved := st.Resolved()
	at := len(blocks)
	for i, b := range blocks {
		if b.BlockID == "actions" {
			at = i
			break
		}
	}
	if at == len(blocks) && len(blocks) > 0 {
		at = len(blocks) - 1
		blocks = append(blocks[:at], append([]slack.Block{{Type: "actions", BlockID: "actions"}}, blocks[at:]...)...)
	}
	if line := render.StateLine(st, resolved); line != "" {
		blocks = append(blocks[:at], append([]slack.Block{{Type: "context", BlockID: "state",
			Elements: []slack.Element{slack.TextElement(line)}}}, blocks[at:]...)...)
		at++
	}
	if at < len(blocks) {
		var links []slack.Element
		for _, el := range render.RowElements(blocks[at].Elements) {
			if strings.HasPrefix(el.ActionID, "link_") && el.ActionID != "link_ticket" {
				links = append(links, el)
			}
		}
		var acting []slack.Element
		if e.interactive() {
			acting = e.Render.InteractiveElements(st, resolved, e.features())
		}
		row := render.ActionRow(append(links, acting...))
		if len(row) == 0 {
			blocks = append(blocks[:at], blocks[at+1:]...)
		} else {
			blocks[at] = slack.Block{Type: "actions", BlockID: "actions", Elements: row}
		}
	}
	att.Blocks = blocks
	if err := e.Slack.UpdateMessage(ctx, slack.Update{Channel: channel, TS: ts, Text: msg.Text,
		Attachments: []slack.Attachment{att}, Metadata: e.metadata(st)}); err != nil {
		e.Log.Warn("restate failed", "error", err)
	}
}

// Postmortem is a markdown draft from an incident's state and its thread: the timeline as it happened,
// impact and first move from the alert, and the parts only a person can write left as prompts.
func Postmortem(st *state.Incident, replies []slack.Message, channel, ts string, now float64) string {
	f := st.FieldMap()
	type event struct {
		at   float64
		text string
	}
	var timeline []event
	if st.StartedAt != 0 {
		timeline = append(timeline, event{st.StartedAt, "Alert started firing"})
	}
	if st.PostedAt != 0 {
		timeline = append(timeline, event{st.PostedAt, "Posted in Slack: " + format.Permalink(channel, ts)})
	}
	for _, r := range replies {
		lines := strings.Split(format.Plain(r.Text), "\n")
		if len(lines) == 0 || lines[0] == "" || strings.HasPrefix(lines[0], "📝") {
			continue
		}
		line := lines[0]
		if len([]rune(line)) > 200 {
			line = format.Cut(line, 200) + " …"
		}
		at, _ := strconv.ParseFloat(r.TS, 64)
		timeline = append(timeline, event{at, line})
	}
	if st.ResolvedAt != 0 {
		timeline = append(timeline, event{st.ResolvedAt, "Resolved"})
	}
	sort.SliceStable(timeline, func(i, j int) bool { return timeline[i].at < timeline[j].at })
	took := "unknown"
	if st.ResolvedAt != 0 && st.StartedAt != 0 {
		took = format.Duration(st.ResolvedAt - st.StartedAt)
	}
	summary := f["summary"]
	if summary == "" {
		summary = st.Alertname
	}
	alertLine := "- Alert: `" + st.Alertname + "` (" + st.Severity + ")"
	if f["subject"] != "" {
		alertLine += " on " + format.Plain(f["subject"])
	}
	durLine := "- Duration: " + took
	if flaps := st.Firings(); flaps > 1 {
		durLine += fmt.Sprintf(", re-fired %d times within an hour", flaps-1)
	}
	picked := "- Picked up by: nobody pressed I'm on it"
	if st.AckedBy != "" {
		picked = "- Picked up by: @" + st.AckedBy
	}
	lines := []string{"# Postmortem draft: " + format.Plain(summary), "",
		"Drafted " + time.Unix(int64(now), 0).UTC().Format("2006-01-02 15:04 UTC") + " from the Slack thread. Edit before filing.", "",
		"## Summary", "", alertLine, durLine, picked}
	if st.TicketURL != "" {
		lines = append(lines, "- Ticket: "+st.TicketURL)
	}
	impact := format.Plain(f["impact"])
	if impact == "" {
		impact = "_Who noticed, and what broke?_"
	}
	lines = append(lines, "", "## Impact", "", impact, "", "## Timeline (UTC)", "")
	for i, ev := range timeline {
		if i == 40 {
			lines = append(lines, fmt.Sprintf("- … %d more lines in the thread", len(timeline)-40))
			break
		}
		lines = append(lines, "- "+time.Unix(int64(ev.at), 0).UTC().Format("15:04")+" "+ev.text)
	}
	action := format.Plain(f["action"])
	if action == "" {
		action = "(none given)"
	}
	lines = append(lines, "", "## What was done", "", "- First move per the alert: "+action,
		"- _What else was done, and by whom?_", "", "## Root cause", "", "_Why did it happen? Why was it not caught earlier?_", "",
		"## Follow-ups", "", "- _What changes, so it does not happen again or is found sooner?_")
	if f["runbook_url"] != "" {
		lines = append(lines, "", "Runbook: "+f["runbook_url"])
	}
	return format.Cut(strings.Join(lines, "\n"), 3700)
}
