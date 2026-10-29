// Package render turns a notification group and its incident state into the Slack message that tracks
// it: the headline, the alert lines, the enrichment and state lines, and the row of buttons.
package render

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/fogbell-dev/fogbell/internal/alert"
	"github.com/fogbell-dev/fogbell/internal/config"
	"github.com/fogbell-dev/fogbell/internal/format"
	"github.com/fogbell-dev/fogbell/internal/slack"
	"github.com/fogbell-dev/fogbell/internal/state"
)

// RowMax is how many elements Slack shows in an attachment's action row before it folds the rest into
// "+N more", where it hides the buttons that act. The row is built to this size, the rest in one menu.
const RowMax = 5

// RowOrder ranks the row's elements: the first move first.
var RowOrder = []string{"link_object", "link_runbook", "ack", "unsilence", "silence", "postmortem",
	"link_dashboard", "link_all", "link_chat", "noise", "link_ticket", "ticket"}

// SilenceChoices are the silence lengths a person can pick.
var SilenceChoices = []struct{ Value, Label string }{{"1h", "1 hour"}, {"24h", "24 hours"}, {"7d", "7 days"}}

var colors = map[string]string{"critical": "#d1242f", "warning": "#d4a72c", "info": "#8c959f"}

// Renderer renders messages with a configuration.
type Renderer struct {
	Config *config.Config
}

// Features are the optional buttons whose credential is present right now.
type Features struct {
	Ticket bool
}

// Icon is the group's icon: a check mark once resolved, a configured icon, or the severity's.
func (r *Renderer) Icon(g *alert.Group, resolved bool) string {
	if resolved {
		return "✅"
	}
	if ic := r.Config.Icon(g.CommonLabels); ic != "" {
		return ic
	}
	switch g.Label("severity") {
	case "critical":
		return "🔴"
	case "warning":
		return "🟡"
	}
	return "⚪"
}

// Title is the message's headline.
func (r *Renderer) Title(g *alert.Group, resolved bool) string {
	text := r.Icon(g, resolved) + " "
	if resolved {
		text += "Resolved: "
	}
	text += format.Escape(g.Headline())
	if team := g.Label(r.Config.Channels.TeamLabel); team != "" {
		text += " · " + format.Escape(team)
	}
	if n := len(g.Active()); n > 1 {
		text += fmt.Sprintf(" (%d)", n)
	}
	return text
}

// LinkData is what an object link template can read.
type LinkData struct {
	Labels      map[string]string
	Annotations map[string]string
	Kind        string
	Value       string
	Start       int64
	StartMs     int64
	FromMs      int64
}

// ObjectLink is the URL of the broken object and the label of its button, or "".
func (r *Renderer) ObjectLink(a alert.Alert) (link, button string) {
	if u := a.Annotations["source_url"]; u != "" {
		return u, "Open"
	}
	objects := r.Config.ObjectReader()
	kind, labels := objects.Match(a.Labels)
	if kind == nil || !r.Config.HasTemplate("object:"+kind.Name) {
		return "", ""
	}
	start := a.Start()
	values := make([]string, len(kind.Labels))
	for i, l := range kind.Labels {
		values[i] = labels[l]
	}
	data := LinkData{Labels: a.Labels, Annotations: a.Annotations, Kind: kind.Name, Value: strings.Join(values, "/"),
		Start: int64(start), StartMs: int64(start * 1000), FromMs: int64((start - 3600) * 1000)}
	out, err := r.Config.Render("object:"+kind.Name, data)
	if err != nil || out == "" {
		return "", ""
	}
	return out, r.Config.ObjectButton(kind.Name)
}

// AlertLine is one alert as a line: its object, what is wrong, and since when.
func (r *Renderer) AlertLine(a alert.Alert, bullet bool) string {
	link, _ := r.ObjectLink(a)
	name := r.Config.ObjectReader().Subject(a)
	what := a.Annotations["description"]
	if what == "" {
		what = a.Annotations["summary"]
	}
	what = format.Escape(what)
	start, end := a.Start(), a.End()
	var when string
	if a.Status == "resolved" && end != 0 {
		when = "_" + format.Date(start, format.Long, format.LongFallback) + " to " + format.Date(end, format.Time, format.TimeFallback) + "_"
	} else {
		when = "_since " + format.Date(start, format.Long, format.LongFallback) + "_"
	}
	head := ""
	if name != "" {
		if link != "" {
			head = "*<" + link + "|" + format.Escape(name) + ">*"
		} else {
			head = "*" + format.Escape(name) + "*"
		}
	}
	if bullet {
		if head != "" {
			head += " "
		}
		return "• " + head + what + " " + when
	}
	if head != "" {
		head += "\n"
	}
	return head + what + " " + when
}

// InteractiveElements are the buttons that act. A firing incident: pick-up and silence. Any incident:
// the noise vote, and the ticket button when tickets are configured.
func (r *Renderer) InteractiveElements(st *state.Incident, resolved bool, f Features) []slack.Element {
	var out []slack.Element
	if !resolved {
		if st.AckedBy == "" {
			out = append(out, slack.Button("ack", "👀 I'm on it", ""))
		}
		if st.SilenceID != "" {
			out = append(out, slack.Button("unsilence", "🔔 Unsilence", ""))
		} else {
			out = append(out, SilenceSelect(st))
		}
	}
	out = append(out, slack.Button("noise", "🙈 Noise", ""))
	if resolved && st.Severity == "critical" {
		out = append(out, slack.Button("postmortem", "📝 Postmortem", ""))
	}
	switch {
	case st.TicketURL != "":
		out = append(out, slack.Button("link_ticket", "📋 Ticket", st.TicketURL))
	case f.Ticket:
		out = append(out, slack.Button("ticket", "📋 Ticket", ""))
	}
	return out
}

// ActionRow is at most RowMax elements: the first four by RowOrder and, when more are left, an
// overflow menu holding them. The silence select never goes in the menu: RowOrder keeps it in front.
func ActionRow(elements []slack.Element) []slack.Element {
	rank := map[string]int{}
	for i, a := range RowOrder {
		rank[a] = i
	}
	pos := func(e slack.Element) int {
		if p, ok := rank[e.ActionID]; ok {
			return p
		}
		return len(RowOrder)
	}
	ordered := append([]slack.Element(nil), elements...)
	sort.SliceStable(ordered, func(i, j int) bool { return pos(ordered[i]) < pos(ordered[j]) })
	if len(ordered) <= RowMax {
		return ordered
	}
	shown, rest := ordered[:RowMax-1], ordered[RowMax-1:]
	menu := slack.Element{Type: "overflow", ActionID: "more"}
	for _, e := range rest {
		label := ""
		if e.Label != nil {
			label = e.Label.Text
		}
		menu.Options = append(menu.Options, slack.Option{Text: *slack.PlainText(format.Cut(label, 75)), Value: e.ActionID, URL: e.URL})
	}
	return append(append([]slack.Element(nil), shown...), menu)
}

// RowElements is the row back as plain elements, the menu's options as the buttons they stand for.
func RowElements(elements []slack.Element) []slack.Element {
	var out []slack.Element
	for _, e := range elements {
		if e.Type != "overflow" {
			out = append(out, e)
			continue
		}
		for _, o := range e.Options {
			out = append(out, slack.Button(o.Value, o.Text.Text, o.URL))
		}
	}
	return out
}

// SilenceSelect offers the silence lengths. One object: the silence is on that object (value
// "<length>|0"). Several: an option group per object, and one for the whole group (value "<length>").
func SilenceSelect(st *state.Incident) slack.Element {
	objs := st.ObjectList()
	opts := func(target string) []slack.Option {
		var out []slack.Option
		for _, c := range SilenceChoices {
			v := c.Value
			if target != "" {
				v += "|" + target
			}
			out = append(out, slack.Option{Text: *slack.PlainText(c.Label), Value: v})
		}
		return out
	}
	sel := slack.Element{Type: "static_select", ActionID: "silence", Placeholder: slack.PlainText("🔕 Silence…")}
	switch {
	case len(objs) == 1:
		sel.Options = opts("0")
	case len(objs) > 1:
		for i, o := range objs {
			sel.OptionGroups = append(sel.OptionGroups, slack.OptionGroup{
				Label: *slack.PlainText(format.Cut("Only "+o.Name, 75)), Options: opts(fmt.Sprint(i))})
		}
		sel.OptionGroups = append(sel.OptionGroups, slack.OptionGroup{
			Label: *slack.PlainText(format.Cut(fmt.Sprintf("All %d in this group", int(st.Count)), 75)), Options: opts("")})
	default:
		sel.Options = opts("")
	}
	return sel
}

// AskPrompt is the question the ask link prefills, about what this alert is.
func AskPrompt(g *alert.Group, summary string) string {
	var where []string
	for _, k := range []string{"namespace", "pod", "node", "job"} {
		if v := g.Label(k); v != "" {
			where = append(where, k+"="+v)
		}
	}
	p := "About the alert " + g.Label("alertname") + " (" + g.Label("severity") + ")"
	if len(where) > 0 {
		p += " on " + strings.Join(where, ", ")
	}
	p += ": " + summary + ". What does it mean, what is affected, and what do I check first? "
	return format.Cut(p, 1500)
}

// AskData is what the ask link template can read.
type AskData struct {
	Prompt string
	Labels map[string]string
}

// AskLink is the configured ask link for a group, or "".
func (r *Renderer) AskLink(g *alert.Group, summary string) string {
	out, err := r.Config.Render("ask", AskData{Prompt: AskPrompt(g, summary), Labels: g.CommonLabels})
	if err != nil {
		return ""
	}
	if _, err := url.Parse(out); err != nil {
		return ""
	}
	return out
}

// StateLine is who is on it, the silence, re-fires and noise votes, as one line.
func StateLine(st *state.Incident, resolved bool) string {
	var parts []string
	if resolved && st.ResolvedAt != 0 && st.StartedAt != 0 {
		parts = append(parts, "✅ Resolved after "+format.Duration(st.ResolvedAt-st.StartedAt))
	}
	if st.AckedBy != "" && !resolved {
		parts = append(parts, "👀 <@"+st.AckedBy+"> is on it")
	}
	if st.SilenceID != "" && !resolved {
		parts = append(parts, "🔕 Silenced by <@"+st.SilenceBy+"> until "+format.Date(st.SilenceUntil, format.Long, format.LongFallback))
	}
	if st.Flaps > 1 {
		parts = append(parts, fmt.Sprintf("🔁 Fired %d times in an hour", int(st.Flaps)))
	}
	if st.WeekCount > 1 && st.History == "" {
		parts = append(parts, "📈 "+format.Ordinal(int(st.WeekCount))+" time this week")
	}
	if n := int(st.NoiseVotes); n > 0 {
		parts = append(parts, fmt.Sprintf("🙈 %d noise vote%s", n, format.Plural(n)))
	}
	return strings.Join(parts, " · ")
}

// Enrichment is the state's looked-up lines, in the order they are shown.
func Enrichment(st *state.Incident) []string {
	var out []string
	for _, s := range []string{st.Spark, st.History, st.Related, st.Change} {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Render is the attachment, with its blocks, for one notification group.
func (r *Renderer) Render(g *alert.Group, st *state.Incident, interactive bool, f Features) []slack.Attachment {
	resolved := g.Resolved()
	active := g.Active()
	first := g.First()
	ann := first.Annotations
	severity := g.Label("severity")

	var blocks []slack.Block
	var body string
	if len(active) == 1 {
		body = r.AlertLine(active[0], false)
	} else {
		var lines []string
		for i, a := range active {
			if i == 10 {
				break
			}
			lines = append(lines, r.AlertLine(a, true))
		}
		if len(active) > 10 {
			lines = append(lines, fmt.Sprintf("_10 of %d shown; the dashboard lists all of them._", len(active)))
		}
		body = strings.Join(lines, "\n")
	}
	blocks = append(blocks, slack.Block{Type: "section", Text: slack.Mrkdwn(format.Cut(body, 2900))})

	if !resolved && ann["action"] != "" {
		blocks = append(blocks, slack.Block{Type: "section", Text: slack.Mrkdwn("→ " + format.Escape(ann["action"]))})
	}
	if extra := Enrichment(st); len(extra) > 0 {
		b := slack.Block{Type: "context", BlockID: "enrich"}
		for _, t := range extra {
			b.Elements = append(b.Elements, slack.TextElement(format.Cut(t, 3000)))
		}
		blocks = append(blocks, b)
	}
	if line := StateLine(st, resolved); line != "" {
		blocks = append(blocks, slack.Block{Type: "context", BlockID: "state", Elements: []slack.Element{slack.TextElement(line)}})
	}

	var buttons []slack.Element
	link, button := "", ""
	if len(active) == 1 {
		link, button = r.ObjectLink(first)
	}
	if link != "" {
		b := slack.Button("link_object", button, link)
		b.Style = "primary"
		buttons = append(buttons, b)
	}
	if u := ann["runbook_url"]; u != "" {
		buttons = append(buttons, slack.Button("link_runbook", "Runbook", u))
	}
	if u := ann["dashboard_url"]; u != "" {
		buttons = append(buttons, slack.Button("link_dashboard", "Dashboard", u))
	}
	if link == "" && r.Config.Links.AllAlerts != "" {
		buttons = append(buttons, slack.Button("link_all", "All alerts", r.Config.Links.AllAlerts))
	}
	if !resolved {
		if ask := r.AskLink(g, ann["summary"]); ask != "" {
			buttons = append(buttons, slack.Button("link_chat", r.Config.Links.AskLabel, ask))
		}
	}
	if interactive {
		buttons = append(buttons, r.InteractiveElements(st, resolved, f)...)
	}
	if len(buttons) > 0 {
		blocks = append(blocks, slack.Block{Type: "actions", BlockID: "actions", Elements: ActionRow(buttons)})
	}
	blocks = append(blocks, slack.Block{Type: "context", Elements: []slack.Element{
		slack.TextElement(format.Escape(g.Label("alertname")) + " · " + severity)}})

	color := "#8c959f"
	if resolved {
		color = "#2da44e"
	} else if c, ok := colors[severity]; ok {
		color = c
	}
	return []slack.Attachment{{Color: color, Fallback: r.Title(g, resolved), Blocks: blocks}}
}

// DescribeChange is the thread reply for a notification on an open incident.
func DescribeChange(before int, g *alert.Group) string {
	now := len(g.Active())
	switch {
	case now > before:
		return fmt.Sprintf("➕ %d more firing, %d in total.", now-before, now)
	case now < before:
		return fmt.Sprintf("➖ %d cleared, %d still firing.", before-now, now)
	}
	return fmt.Sprintf("⏰ Still firing (%d), unchanged.", now)
}

// MatcherText renders silence matchers as code spans, sorted.
func MatcherText(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = "`" + k + "=" + labels[k] + "`"
	}
	return strings.Join(parts, " ")
}
