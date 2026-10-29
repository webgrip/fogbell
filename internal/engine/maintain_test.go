package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fogbell-dev/fogbell/internal/alert"
	"github.com/fogbell-dev/fogbell/internal/slack"
	"github.com/fogbell-dev/fogbell/internal/state"
	"github.com/fogbell-dev/fogbell/internal/upstream"
)

func (h *harness) maintain() []string { return h.e.Maintain(context.Background()) }

func TestSilenceEndingWhileFiringGetsAWarning(t *testing.T) {
	h := newHarness(t, "")
	h.notify("c", firing())
	h.press("silence", "24h|0", "", "")
	if got := h.maintain(); len(got) != 0 {
		t.Fatal(got)
	}
	h.now += 86400 - 3500
	if got := h.maintain(); strings.Join(got, ",") != "warned" {
		t.Fatal(got)
	}
	if r := h.slack.LastReply(); !strings.Contains(r, "⏳ The silence <@U1> set ends at") || !strings.Contains(r, "this still fires") {
		t.Fatal(r)
	}
	if got := h.maintain(); len(got) != 0 {
		t.Fatal("warned twice", got)
	}
	if h.st(h.only()).ExpiryWarned != "sid-1" {
		t.Fatal(h.st(h.only()).ExpiryWarned)
	}
}

func TestAOneHourSilenceWarnsTenMinutesAhead(t *testing.T) {
	h := newHarness(t, "")
	h.notify("c", firing())
	h.press("silence", "1h|0", "", "")
	h.now += 120
	if got := h.maintain(); len(got) != 0 {
		t.Fatal(got)
	}
	h.now += 3600 - 120 - 500
	if got := h.maintain(); strings.Join(got, ",") != "warned" {
		t.Fatal(got)
	}
}

func TestNoWarningOnceResolvedOrLiftedElsewhere(t *testing.T) {
	h := newHarness(t, "")
	h.notify("c", firing())
	h.press("silence", "24h|0", "", "")
	h.am.mu.Lock()
	h.am.active["sid-1"] = false
	h.am.mu.Unlock()
	h.now += 86400 - 600
	replies := len(h.slack.Replies)
	if got := h.maintain(); strings.Join(got, ",") != "gone" || len(h.slack.Replies) != replies {
		t.Fatal(got)
	}
	h.notify("c", group("resolved", 1, "", ""))
	if got := h.maintain(); len(got) != 0 {
		t.Fatal(got)
	}
}

func (h *harness) roster(people ...string) {
	h.e.Roster = func() Roster {
		return Roster{People: people, EscalateAfterMinutes: 15, RotationStart: "2026-10-26"}
	}
}

func TestAnUnpickedPageEscalatesToThePersonOnCall(t *testing.T) {
	h := newHarness(t, "")
	h.roster("UA", "UB")
	h.notify("c", group("", 1, "", "page"))
	h.now += 14 * 60
	if got := h.maintain(); len(got) != 0 {
		t.Fatal(got)
	}
	h.now += 60
	if got := h.maintain(); strings.Join(got, ",") != "escalated" {
		t.Fatal(got)
	}
	if !strings.HasPrefix(h.slack.LastReply(), "📟 <@UA>, you are on call this week") {
		t.Fatal(h.slack.LastReply())
	}
	if got := h.maintain(); len(got) != 0 {
		t.Fatal("escalated twice")
	}
}

func TestNoEscalationWhenPickedUpOrNotAPage(t *testing.T) {
	h := newHarness(t, "")
	h.roster("UA", "UB")
	h.notify("c", group("", 1, "", "page"))
	h.press("ack", "", "", "")
	h.now += 3600
	if got := h.maintain(); len(got) != 0 {
		t.Fatal(got)
	}
	h2 := newHarness(t, "")
	h2.roster("UA")
	h2.notify("c", group("", 1, "w", "warnings"))
	h2.now += 3600
	if got := h2.maintain(); len(got) != 0 {
		t.Fatal(got)
	}
	h3 := newHarness(t, "")
	h3.roster()
	h3.notify("c", group("", 1, "", "page"))
	h3.now += 7200
	if got := h3.maintain(); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestWeeklyRotationHandsOverMondayMorning(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Amsterdam")
	r := Roster{People: []string{"UA", "UB", "UC"}, RotationStart: "2026-10-26"}
	monday0830 := time.Date(2026, 11, 2, 8, 30, 0, 0, loc)
	for _, c := range []struct {
		at   time.Time
		want string
	}{{monday0830, "UA"}, {monday0830.Add(time.Hour), "UB"}, {monday0830.Add(time.Hour + 14*24*time.Hour), "UA"}} {
		if got := OnCall(r, c.at, loc, 9, 0); got != c.want {
			t.Fatal(c.at, got)
		}
	}
	if got := OnCall(Roster{}, monday0830, loc, 9, 0); got != "" {
		t.Fatal(got)
	}
}

func critical(status string) *alert.Group {
	g := group(status, 1, "", "page")
	g.CommonLabels["severity"] = "critical"
	return g
}

func TestPostmortemOnlyOnAResolvedCritical(t *testing.T) {
	h := newHarness(t, "")
	h.notify("c", critical(""))
	if contains(ids(h.only()), "postmortem") {
		t.Fatal("postmortem while firing")
	}
	h.notify("c", critical("resolved"))
	if !contains(ids(h.only()), "postmortem") {
		t.Fatal("no postmortem button")
	}
	h2 := newHarness(t, "")
	h2.notify("c", group("", 1, "w", ""))
	h2.notify("c", group("resolved", 1, "w", ""))
	if contains(ids(h2.only()), "postmortem") {
		t.Fatal("postmortem on a warning")
	}
}

func TestPostmortemDraftFromTheThread(t *testing.T) {
	h := newHarness(t, "")
	h.slack.ReplyClock = func() float64 { return h.now }
	h.notify("c", critical(""))
	h.now += 120
	h.press("ack", "", "", "")
	h.now += 1500
	h.notify("c", critical("resolved"))
	h.now += 600
	h.press("noise", "", "", "")
	if got := h.press("postmortem", "", "", ""); got != "postmortem" {
		t.Fatal(got)
	}
	text := h.slack.LastReply()
	if !strings.HasPrefix(text, "📝 Postmortem draft for <@U1>") {
		t.Fatal(text)
	}
	draft := strings.SplitN(text, "```\n", 2)[1]
	for _, want := range []string{"# Postmortem draft: A thing broke", "- Alert: `X` (critical) on ns/p0", "- Picked up by: @U1",
		"## Impact\n\nNobody can work.", "- 08:15 Alert started firing", "@U1 is on it.", "✅ Resolved after",
		"- First move per the alert: Fix it.", "## Root cause"} {
		if !strings.Contains(draft, want) {
			t.Fatal(want, "\n", draft)
		}
	}
	if strings.Contains(draft, "<@") {
		t.Fatal("raw mention in the draft")
	}
	var timeline []string
	for _, l := range strings.Split(draft, "\n") {
		if strings.HasPrefix(l, "- ") && len(l) > 4 && l[2] >= '0' && l[2] <= '9' {
			timeline = append(timeline, l)
		}
	}
	for i := 1; i < len(timeline); i++ {
		if timeline[i][2:7] < timeline[i-1][2:7] {
			t.Fatal("timeline out of order", timeline)
		}
	}
	if !strings.HasSuffix(strings.TrimSuffix(timeline[len(timeline)-1], "\n"), "says this alert is noise (1 vote). It counts toward the weekly noise report.") {
		t.Fatal(timeline[len(timeline)-1])
	}
}

func TestPostmortemWithoutTheHistoryScopeStillDrafts(t *testing.T) {
	h := newHarness(t, "")
	h.slack.Fail["conversations.replies"] = "missing_scope"
	h.notify("c", critical(""))
	h.notify("c", critical("resolved"))
	if got := h.press("postmortem", "", "", ""); got != "postmortem" || !strings.Contains(h.slack.LastReply(), "## Timeline (UTC)") {
		t.Fatal(got)
	}
}

func TestDigestOnceADay(t *testing.T) {
	h := newHarness(t, "")
	h.notify("c", firing())
	for _, want := range []string{"posted", "already"} {
		if got, err := h.e.Digest(context.Background(), "c", "2026-10-27"); err != nil || got != want {
			t.Fatal(got, err)
		}
	}
}

func iso(ts float64) string { return time.Unix(int64(ts), 0).UTC().Format(time.RFC3339) }

func TestDigestListsWhatIsSilenced(t *testing.T) {
	h := newHarness(t, "channels:\n  alerts: [c]\n  silenceOverview: warn\n")
	until := iso(h.now + 7200)
	h.am.silences = []upstream.Silence{
		{CreatedBy: "slack:pat", EndsAt: until, Comment: "Fix merged (from Slack by pat: https://slack.com/archives/CC/p1793089000000100)",
			Matchers: []upstream.Matcher{{Name: "alertname", Value: "X"}, {Name: "pod", Value: "p0"}}},
		{CreatedBy: "slack:kim", EndsAt: until, Comment: "From Slack by kim: https://slack.com/archives/C9/p1"},
		{CreatedBy: "pat@example.com", EndsAt: until, Comment: "node maintenance", Matchers: []upstream.Matcher{{Name: "node", Value: "worker02"}}},
		{CreatedBy: "slack:pat", EndsAt: until, Comment: "From Slack by pat: https://slack.com/archives/CC/p2"},
	}
	for i := range h.am.silences[:3] {
		h.am.silences[i].Status.State = "active"
	}
	h.am.silences[3].Status.State = "expired"
	h.notify("c", firing())
	if _, err := h.e.Digest(context.Background(), "c", "2026-10-27"); err != nil {
		t.Fatal(err)
	}
	text := h.slack.Newest().Text
	for _, want := range []string{"*Silenced now* (1)", "• `alertname=X` `pod=p0` · pat · until <!date^",
		"_Fix merged_ · <https://slack.com/archives/CC/p1793089000000100|message>"} {
		if !strings.Contains(text, want) {
			t.Fatal(want, "\n", text)
		}
	}
	if strings.Contains(text, "kim") || strings.Contains(text, "worker02") {
		t.Fatal(text)
	}
	h.notify("warn", group("", 1, "k2", ""))
	if _, err := h.e.Digest(context.Background(), "warn", "2026-10-28"); err != nil {
		t.Fatal(err)
	}
	text = h.slack.Newest().Text
	if !strings.Contains(text, "`node=worker02` · pat@example.com") || !strings.Contains(text, "_node maintenance_") {
		t.Fatal(text)
	}
}

func TestDigestSurvivesAlertmanagerDown(t *testing.T) {
	h := newHarness(t, "")
	h.notify("c", firing())
	h.am.down = true
	if got, err := h.e.Digest(context.Background(), "c", "2026-10-27"); err != nil || got != "posted" {
		t.Fatal(got, err)
	}
}

func (h *harness) seed(name, key string, st state.Incident) {
	st.GroupKey, st.Alertname = key, name
	if st.State == "" {
		st.State = "firing"
	}
	raw, _ := json.Marshal(st)
	h.slack.Put("CC", slack.Message{Text: name, Metadata: &slack.Metadata{EventType: "fogbell_incident", EventPayload: raw}})
}

func TestNoiseReportRanksAndPostsOnceAWeek(t *testing.T) {
	h := newHarness(t, "")
	h.seed("Voted", "a", state.Incident{NoiseVotes: 2, Flaps: 1})
	h.seed("Flappy", "b", state.Incident{Flaps: 4})
	h.seed("Short", "c", state.Incident{State: "resolved", StartedAt: h.now - 900, ResolvedAt: h.now - 600})
	h.seed("Fine", "d", state.Incident{State: "resolved", StartedAt: h.now - 9000, ResolvedAt: h.now - 600})
	if got, err := h.e.NoiseReport(context.Background(), "hyg", "2026-W44"); err != nil || got != "posted" {
		t.Fatal(got, err)
	}
	var lines []string
	for _, l := range strings.Split(h.slack.Newest().Text, "\n") {
		if strings.HasPrefix(l, "•") {
			lines = append(lines, l)
		}
	}
	var names []string
	for _, l := range lines {
		names = append(names, strings.SplitN(strings.SplitN(l, "|", 2)[1], ">", 2)[0])
	}
	if strings.Join(names, ",") != "Voted,Flappy,Short" {
		t.Fatal(names)
	}
	if !strings.Contains(lines[0], "2 noise votes") || !strings.Contains(lines[1], "3 re-fires") || !strings.Contains(lines[2], "1 short-lived") {
		t.Fatal(lines)
	}
	if got, _ := h.e.NoiseReport(context.Background(), "hyg", "2026-W44"); got != "already" {
		t.Fatal(got)
	}
}

func TestNoiseReportOnAQuietWeek(t *testing.T) {
	h := newHarness(t, "")
	if got, err := h.e.NoiseReport(context.Background(), "hyg", "2026-W44"); err != nil || got != "posted" {
		t.Fatal(got, err)
	}
	if !strings.Contains(h.slack.Newest().Text, "none") {
		t.Fatal(h.slack.Newest().Text)
	}
}

func TestTickPostsAtTheDigestTimeInItsZone(t *testing.T) {
	h := newHarness(t, "channels:\n  alerts: [c]\n  digest: [c]\n  noiseReport: hyg\nschedule:\n  timezone: Europe/Amsterdam\n")
	h.notify("c", firing())
	loc, _ := time.LoadLocation("Europe/Amsterdam")
	if got := h.e.Tick(context.Background(), time.Date(2026, 11, 2, 8, 58, 0, 0, loc)); len(got) != 0 {
		t.Fatal(got)
	}
	got := h.e.Tick(context.Background(), time.Date(2026, 11, 2, 9, 3, 0, 0, loc))
	if strings.Join(got, ",") != "digest c: posted,noise report: posted" {
		t.Fatal(got)
	}
	if got := h.e.Tick(context.Background(), time.Date(2026, 11, 3, 9, 11, 0, 0, loc)); len(got) != 0 {
		t.Fatal(got)
	}
}
