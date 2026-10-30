package engine

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/webgrip/fogbell/internal/alert"
	"github.com/webgrip/fogbell/internal/format"
	"github.com/webgrip/fogbell/internal/slack"
	"github.com/webgrip/fogbell/internal/state"
)

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

// InvestigationBrief is the title and brief an investigator gets: what fires, on what, since when,
// the alert's own contract fields and what fogbell already looked up, and what to report. The agent
// is asked to look and report, never to change anything.
func InvestigationBrief(st *state.Incident, link string, g *alert.Group) (string, string) {
	f := st.FieldMap()
	headline := f["summary"]
	if headline == "" {
		headline = st.Alertname
	}
	title := "Investigate: " + headline
	if f["subject"] != "" {
		title += " on " + f["subject"]
	}
	lines := []string{
		"An alert is firing. Find out why, without changing anything: read metrics, logs, events and recent changes, and report.",
		"",
		"- **Alert:** `" + st.Alertname + "` (" + st.Severity + ")",
		"- **Firing since:** " + time.Unix(int64(st.StartedAt), 0).UTC().Format("2006-01-02 15:04 UTC"),
		"- **Group labels:** " + st.GroupLabels,
		"- **Slack thread:** " + link,
	}
	if f["subject"] != "" {
		lines = append(lines, "- **Subject:** "+f["subject"])
	}
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
	if g != nil {
		lines = append(lines, "", "## Firing alerts", "")
		for _, a := range g.Active()[:min(20, len(g.Active()))] {
			lines = append(lines, "- "+labelText(a.Labels))
		}
	}
	var seen []string
	for _, s := range []string{st.Spark, st.Related, st.Change, st.History} {
		if s != "" {
			seen = append(seen, "- "+format.Plain(s))
		}
	}
	if len(seen) > 0 {
		lines = append(lines, "", "## Already looked up", "")
		lines = append(lines, seen...)
	}
	lines = append(lines, "", "## Report", "",
		"Summarise in a few sentences: the most likely cause with the evidence for it, what is affected, and the fix you would make.",
		"Say what you could not check. Do not change anything.")
	return format.Cut(title, 200), strings.Join(lines, "\n")
}

func labelText(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + strconv.Quote(labels[k])
	}
	return "{" + strings.Join(parts, ", ") + "}"
}
