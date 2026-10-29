package engine

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/webgrip/fogbell/internal/render"
	"github.com/webgrip/fogbell/internal/slack"
	"github.com/webgrip/fogbell/internal/state"
	"github.com/webgrip/fogbell/internal/upstream"
)

func TestOneMessagePerIncident(t *testing.T) {
	h := newHarness(t, "")
	if got := h.notify("c", firing()); got != "posted" {
		t.Fatal(got)
	}
	if got := h.notify("c", group("", 3, "", "")); got != "updated" {
		t.Fatal(got)
	}
	if !strings.Contains(h.only().Text, "(3)") {
		t.Fatal(h.only().Text)
	}
	if got := h.slack.LastReply(); got != "➕ 2 more firing, 3 in total." {
		t.Fatal(got)
	}
}

func TestResolveClosesTheSameMessage(t *testing.T) {
	h := newHarness(t, "")
	h.notify("c", firing())
	h.now += 1800
	if got := h.notify("c", group("resolved", 1, "", "")); got != "resolved" {
		t.Fatal(got)
	}
	m := h.only()
	if !strings.Contains(m.Text, "✅") || h.st(m).State != "resolved" {
		t.Fatal(m.Text)
	}
	if !strings.Contains(h.slack.LastReply(), "Resolved after") {
		t.Fatal(h.slack.LastReply())
	}
}

func TestRefireWithinTheReopenWindowReopens(t *testing.T) {
	h := newHarness(t, "")
	h.notify("c", firing())
	h.notify("c", group("resolved", 1, "", ""))
	h.now += 600
	if got := h.notify("c", firing()); got != "reopened" {
		t.Fatal(got)
	}
	if h.st(h.only()).Flaps != 2 || !strings.Contains(h.slack.LastReply(), "2nd time") {
		t.Fatal(h.slack.LastReply())
	}
}

func TestRefireLaterIsANewIncident(t *testing.T) {
	h := newHarness(t, "")
	h.notify("c", firing())
	h.notify("c", group("resolved", 1, "", ""))
	h.now += 7200
	if got := h.notify("c", firing()); got != "posted" {
		t.Fatal(got)
	}
	if len(h.slack.Messages) != 2 || h.st(h.slack.Newest()).WeekCount != 2 {
		t.Fatal(len(h.slack.Messages))
	}
}

func TestRestartFindsTheOpenIncident(t *testing.T) {
	h := newHarness(t, "")
	h.notify("c", firing())
	h.restart()
	if got := h.notify("c", group("", 2, "", "")); got != "updated" {
		t.Fatal(got)
	}
	h.only()
}

func TestAdoptsAnEarlierEventType(t *testing.T) {
	h := newHarness(t, "slack:\n  adoptEvents: [legacy_alert]\n")
	raw, _ := json.Marshal(state.Incident{GroupKey: `{}:{alertname="X"}`, State: "firing", Count: 1, Alertname: "X"})
	h.slack.Put("CC", slack.Message{Text: "*old*", Attachments: []slack.Attachment{{Blocks: []slack.Block{{Type: "section", Text: slack.Mrkdwn("x")}}}},
		Metadata: &slack.Metadata{EventType: "legacy_alert", EventPayload: raw}})
	if got := h.notify("c", group("", 2, "", "")); got != "updated" {
		t.Fatal(got)
	}
	if m := h.only(); m.Metadata.EventType != "fogbell_incident" {
		t.Fatal(m.Metadata.EventType)
	}
}

func TestPageMentionsTheChannelOnce(t *testing.T) {
	h := newHarness(t, "")
	h.notify("c", group("", 1, "", "page"))
	if !strings.HasPrefix(h.only().Text, "<!channel>") {
		t.Fatal(h.only().Text)
	}
	h.notify("c", group("", 2, "", "page"))
	if strings.Contains(h.slack.LastReply(), "<!channel>") {
		t.Fatal(h.slack.LastReply())
	}
}

func TestWithoutHistoryScopesItStillThreads(t *testing.T) {
	h := newHarness(t, "")
	h.slack.Fail["conversations.history"] = "missing_scope"
	if got := h.notify("c", firing()); got != "posted" {
		t.Fatal(got)
	}
	if got := h.notify("c", group("", 2, "", "")); got != "updated" {
		t.Fatal(got)
	}
	h.only()
}

func TestStrayResolveIsIgnored(t *testing.T) {
	h := newHarness(t, "")
	if got := h.notify("c", group("resolved", 1, "", "")); got != "ignored-resolve" || len(h.slack.Messages) != 0 {
		t.Fatal(got)
	}
}

func TestAckAndSilence(t *testing.T) {
	h := newHarness(t, "")
	h.notify("c", firing())
	if got := h.press("ack", "", "", ""); got != "ack" {
		t.Fatal(got)
	}
	if got := h.press("silence", "24h", "", ""); got != "silence" {
		t.Fatal(got)
	}
	if m := h.am.matchers(0); len(m) != 2 || m["alertname"] != "X" || m["namespace"] != "ns" {
		t.Fatal(m)
	}
	if h.am.created[0].CreatedBy != "slack:pat" {
		t.Fatal(h.am.created[0].CreatedBy)
	}
	m := h.only()
	st := h.st(m)
	if st.AckedBy != "U1" || st.SilenceID != "sid-1" {
		t.Fatal(st)
	}
	line := blockOf(m, "state").Elements[0].Content
	if !strings.Contains(line, "<@U1> is on it") || !strings.Contains(line, "Silenced by <@U1>") {
		t.Fatal(line)
	}
	got := ids(m)
	if !contains(got, "unsilence") || contains(got, "ack") || !contains(got, "link_runbook") {
		t.Fatal(got)
	}
}

func TestAskLinkWhileFiringOnly(t *testing.T) {
	h := newHarness(t, "")
	h.notify("c", firing())
	var chat *slack.Element
	for _, e := range render.RowElements(blockOf(h.only(), "actions").Elements) {
		if e.ActionID == "link_chat" {
			chat = &e
		}
	}
	if chat == nil {
		t.Fatal("no ask link")
	}
	u, _ := url.Parse(chat.URL)
	q := u.Query().Get("q")
	if !strings.Contains(q, "About the alert X") || !strings.Contains(q, "namespace=ns") || u.Query().Get("model") != "ops" {
		t.Fatal(chat.URL)
	}
	h.notify("c", group("resolved", 1, "", ""))
	if contains(ids(h.only()), "link_chat") {
		t.Fatal("ask link on a resolved incident")
	}
}

type fakeTracker struct {
	token   bool
	fail    bool
	members map[string]string
	created [][2]string
	member  []string
}

func newTracker() *fakeTracker {
	return &fakeTracker{token: true, members: map[string]string{"pat@example.com": "42"}}
}

func (f *fakeTracker) Name() string    { return "Tracker" }
func (f *fakeTracker) Available() bool { return f.token }
func (f *fakeTracker) Member(_ context.Context, email string) (string, error) {
	return f.members[strings.ToLower(email)], nil
}
func (f *fakeTracker) Create(_ context.Context, name, markdown, member string) (upstream.Ticket, error) {
	if f.fail {
		return upstream.Ticket{}, errTimeout
	}
	f.created = append(f.created, [2]string{name, markdown})
	f.member = append(f.member, member)
	n := len(f.created)
	return upstream.Ticket{ID: "t" + string(rune('0'+n)), URL: "https://tracker.example/t/t" + string(rune('0'+n))}, nil
}

type timeoutError struct{}

func (timeoutError) Error() string { return "timed out" }

var errTimeout = timeoutError{}

func TestTheRowIsFiveAndTheRestIsOneMenu(t *testing.T) {
	h := newHarness(t, "")
	h.e.Tracker = newTracker()
	h.notify("c", firing())
	row := blockOf(h.only(), "actions").Elements
	if len(row) > render.RowMax {
		t.Fatal(len(row))
	}
	var first []string
	for _, e := range row[:4] {
		first = append(first, e.ActionID)
	}
	if strings.Join(first, ",") != "link_object,link_runbook,ack,silence" {
		t.Fatal(first)
	}
	menu := row[len(row)-1]
	var opts []string
	for _, o := range menu.Options {
		opts = append(opts, o.Value)
	}
	if menu.Type != "overflow" || strings.Join(opts, ",") != "link_dashboard,link_chat,noise,ticket" {
		t.Fatal(opts)
	}
	if !strings.HasPrefix(menu.Options[0].URL, "https://") {
		t.Fatal(menu.Options[0])
	}
}

func TestAMenuOptionActsLikeItsButton(t *testing.T) {
	h := newHarness(t, "")
	h.notify("c", firing())
	if got := h.press("more", "noise", "", ""); got != "noise" {
		t.Fatal(got)
	}
	if got := h.press("more", "link_dashboard", "", ""); got != "link" {
		t.Fatal(got)
	}
	row := blockOf(h.only(), "actions").Elements
	if len(row) > render.RowMax || !contains(ids(h.only()), "link_dashboard") {
		t.Fatal(row)
	}
}

func TestSilenceOneObjectAddsItsLabels(t *testing.T) {
	h := newHarness(t, "")
	h.notify("c", firing())
	var values []string
	for _, e := range blockOf(h.only(), "actions").Elements {
		if e.ActionID == "silence" {
			for _, o := range e.Options {
				values = append(values, o.Value)
			}
		}
	}
	if strings.Join(values, ",") != "1h|0,24h|0,7d|0" {
		t.Fatal(values)
	}
	h.press("silence", "24h|0", "", "")
	if m := h.am.matchers(0); m["pod"] != "p0" || len(m) != 3 {
		t.Fatal(m)
	}
	if !strings.Contains(h.slack.LastReply(), "Matchers: `alertname=X` `namespace=ns` `pod=p0`") {
		t.Fatal(h.slack.LastReply())
	}
}

func TestSilenceSeveralObjectsOffersEachAndTheGroup(t *testing.T) {
	h := newHarness(t, "")
	h.notify("c", group("", 3, "", ""))
	var labels, values []string
	for _, e := range blockOf(h.only(), "actions").Elements {
		if e.ActionID == "silence" {
			for _, g := range e.OptionGroups {
				labels = append(labels, g.Label.Text)
				for _, o := range g.Options {
					values = append(values, o.Value)
				}
			}
		}
	}
	if strings.Join(labels, ",") != "Only ns/p0,Only ns/p1,Only ns/p2,All 3 in this group" {
		t.Fatal(labels)
	}
	seen := map[string]bool{}
	for _, v := range values {
		if seen[v] {
			t.Fatal("duplicate value", v)
		}
		seen[v] = true
	}
	h.press("silence", "1h|2", "", "")
	if m := h.am.matchers(0); m["pod"] != "p2" {
		t.Fatal(m)
	}
	h.press("unsilence", "", "", "")
	h.press("silence", "1h", "", "")
	if m := h.am.matchers(1); len(m) != 2 {
		t.Fatal(m)
	}
	if !strings.Contains(h.slack.LastReply(), "Matchers: `alertname=X` `namespace=ns`") {
		t.Fatal(h.slack.LastReply())
	}
}

func TestSilenceAsksWhyAndTheReasonBecomesTheComment(t *testing.T) {
	h := newHarness(t, "")
	h.notify("c", firing())
	ts := h.firstTS()
	var pressed Interaction
	_ = json.Unmarshal([]byte(`{"type":"block_actions","trigger_id":"T1","channel":{"id":"CC"},"container":{"message_ts":"`+ts+
		`"},"user":{"id":"U1","username":"pat"},"actions":[{"action_id":"silence","selected_option":{"value":"24h|0"}}]}`), &pressed)
	if got := h.e.Interact(context.Background(), &pressed); got != "silence-asked" {
		t.Fatal(got)
	}
	if len(h.am.created) != 0 {
		t.Fatal("silenced before the answer")
	}
	var view map[string]any
	_ = json.Unmarshal(h.slack.Views[0], &view)
	if view["callback_id"] != "silence_reason" {
		t.Fatal(view)
	}
	submitted := Interaction{Type: "view_submission", User: User{ID: "U2", Username: "kim"},
		View: &View{CallbackID: "silence_reason", PrivateMetadata: view["private_metadata"].(string)}}
	submitted.View.State.Values = map[string]map[string]struct {
		Value string `json:"value"`
	}{"reason": {"reason": {Value: "Fix merged,\n waiting for rollout"}}}
	if got := h.e.Interact(context.Background(), &submitted); got != "silence" {
		t.Fatal(got)
	}
	if c := h.am.created[0].Comment; !strings.HasPrefix(c, "Fix merged, waiting for rollout (from Slack by kim: https://slack.com/archives/") {
		t.Fatal(c)
	}
	reply := h.slack.LastReply()
	if !strings.Contains(reply, "Why: _Fix merged, waiting for rollout_") || !strings.Contains(reply, "<@U2> silenced this for 24h") {
		t.Fatal(reply)
	}
	if got := h.e.Interact(context.Background(), &submitted); got != "already-silenced" || len(h.am.created) != 1 {
		t.Fatal(got)
	}
}

func TestSilenceWithoutAModalWhenSlackRefusesIt(t *testing.T) {
	h := newHarness(t, "")
	h.slack.Fail["views.open"] = "expired_trigger_id"
	h.notify("c", firing())
	var pressed Interaction
	_ = json.Unmarshal([]byte(`{"type":"block_actions","trigger_id":"T1","channel":{"id":"CC"},"container":{"message_ts":"`+h.firstTS()+
		`"},"user":{"id":"U1","username":"pat"},"actions":[{"action_id":"silence","selected_option":{"value":"1h|0"}}]}`), &pressed)
	if got := h.e.Interact(context.Background(), &pressed); got != "silence" || len(h.am.created) != 1 {
		t.Fatal(got)
	}
	if strings.Contains(h.slack.LastReply(), "Why:") {
		t.Fatal(h.slack.LastReply())
	}
	if c := h.am.created[0].Comment; !strings.HasPrefix(c, "From Slack by pat: ") {
		t.Fatal(c)
	}
}

func TestNoTicketButtonWithoutAToken(t *testing.T) {
	h := newHarness(t, "")
	tr := newTracker()
	tr.token = false
	h.e.Tracker = tr
	h.notify("c", firing())
	if contains(ids(h.only()), "ticket") {
		t.Fatal(ids(h.only()))
	}
}

func TestTicketOnceThenALink(t *testing.T) {
	h := newHarness(t, "")
	tr := newTracker()
	h.e.Tracker = tr
	h.notify("c", firing())
	if got := h.press("ticket", "", "", ""); got != "ticket" {
		t.Fatal(got)
	}
	name, md := tr.created[0][0], tr.created[0][1]
	if name != "A thing broke: ns/p0" {
		t.Fatal(name)
	}
	for _, want := range []string{"- **Slack thread:** https://slack.com/archives/CC/p", "## Impact\n\nNobody can work.", "## First move\n\nFix it.", "`X` (warning)"} {
		if !strings.Contains(md, want) {
			t.Fatal(want, md)
		}
	}
	if !strings.Contains(h.slack.LastReply(), "<https://tracker.example/t/t1|A thing broke: ns/p0>") {
		t.Fatal(h.slack.LastReply())
	}
	if got := ids(h.only()); contains(got, "ticket") || !contains(got, "link_ticket") {
		t.Fatal(got)
	}
	if h.st(h.only()).TicketURL != "https://tracker.example/t/t1" || tr.member[0] != "42" {
		t.Fatal(h.st(h.only()).TicketURL, tr.member)
	}
	h.press("ticket", "", "", "")
	if len(tr.created) != 1 || !strings.Contains(h.slack.LastReply(), "There is a ticket already: <https://tracker.example/t/t1|Tracker>") {
		t.Fatal(h.slack.LastReply())
	}
}

func TestTicketWithoutAMatchIsUnassignedAndSaysSo(t *testing.T) {
	for _, slackFails := range []bool{true, false} {
		h := newHarness(t, "")
		tr := newTracker()
		if slackFails {
			h.slack.Fail["users.info"] = "missing_scope"
		} else {
			tr.members = map[string]string{}
		}
		h.e.Tracker = tr
		h.notify("c", firing())
		if got := h.press("ticket", "", "", ""); got != "ticket" || tr.member[0] != "" {
			t.Fatal(got, tr.member)
		}
		if !strings.Contains(h.slack.LastReply(), "not assigned") {
			t.Fatal(h.slack.LastReply())
		}
	}
}

func TestTicketOnAResolvedIncident(t *testing.T) {
	h := newHarness(t, "")
	h.e.Tracker = newTracker()
	h.notify("c", firing())
	h.notify("c", group("resolved", 1, "", ""))
	got := ids(h.only())
	if !contains(got, "ticket") || contains(got, "ack") {
		t.Fatal(got)
	}
}

func TestTicketFailureIsSaidInTheThread(t *testing.T) {
	h := newHarness(t, "")
	tr := newTracker()
	tr.fail = true
	h.e.Tracker = tr
	h.notify("c", firing())
	if got := h.press("ticket", "", "", ""); got != "ticket-failed" {
		t.Fatal(got)
	}
	if !strings.Contains(h.slack.LastReply(), "Tracker did not create the ticket <@U1> asked for") || h.st(h.only()).TicketURL != "" {
		t.Fatal(h.slack.LastReply())
	}
}

func TestNoiseVoteOncePerPerson(t *testing.T) {
	h := newHarness(t, "")
	h.notify("c", firing())
	for _, c := range []struct{ user, want string }{{"U1", "noise"}, {"U1", "noise-already"}, {"U2", "noise"}} {
		if got := h.press("noise", "", "", c.user); got != c.want {
			t.Fatal(c, got)
		}
	}
	st := h.st(h.only())
	if st.NoiseVotes != 2 || st.NoiseBy != "U1,U2" {
		t.Fatal(st)
	}
	if !strings.Contains(blockOf(h.only(), "state").Elements[0].Content, "🙈 2 noise votes") {
		t.Fatal("no vote line")
	}
	if counter(h.e, "noise", "X") != 2 {
		t.Fatal(counter(h.e, "noise", "X"))
	}
}

func TestTimeToAckAndToResolve(t *testing.T) {
	h := newHarness(t, "")
	h.notify("c", firing())
	h.now += 240
	h.press("ack", "", "", "")
	h.press("ack", "", "", "U2")
	h.now += 1560
	h.notify("c", group("resolved", 1, "", ""))
	if counter(h.e, "incidents", "X", "warning", "") != 1 {
		t.Fatal("incidents")
	}
	want := `
# HELP fogbell_ack_seconds Seconds from a firing being shown to the first I'm on it.
# TYPE fogbell_ack_seconds histogram
fogbell_ack_seconds_bucket{alertname="X",severity="warning",team="",le="60"} 0
fogbell_ack_seconds_bucket{alertname="X",severity="warning",team="",le="300"} 1
fogbell_ack_seconds_bucket{alertname="X",severity="warning",team="",le="600"} 1
fogbell_ack_seconds_bucket{alertname="X",severity="warning",team="",le="1800"} 1
fogbell_ack_seconds_bucket{alertname="X",severity="warning",team="",le="3600"} 1
fogbell_ack_seconds_bucket{alertname="X",severity="warning",team="",le="7200"} 1
fogbell_ack_seconds_bucket{alertname="X",severity="warning",team="",le="14400"} 1
fogbell_ack_seconds_bucket{alertname="X",severity="warning",team="",le="43200"} 1
fogbell_ack_seconds_bucket{alertname="X",severity="warning",team="",le="86400"} 1
fogbell_ack_seconds_bucket{alertname="X",severity="warning",team="",le="259200"} 1
fogbell_ack_seconds_bucket{alertname="X",severity="warning",team="",le="604800"} 1
fogbell_ack_seconds_bucket{alertname="X",severity="warning",team="",le="+Inf"} 1
fogbell_ack_seconds_sum{alertname="X",severity="warning",team=""} 240
fogbell_ack_seconds_count{alertname="X",severity="warning",team=""} 1
`
	if err := testutil.CollectAndCompare(h.e.Metrics.AckSeconds, strings.NewReader(want)); err != nil {
		t.Fatal(err)
	}
	if n := testutil.CollectAndCount(h.e.Metrics.ResolveSecs); n != 1 {
		t.Fatal(n)
	}
}

func TestARefireRestartsTheClock(t *testing.T) {
	h := newHarness(t, "")
	h.notify("c", firing())
	h.notify("c", group("resolved", 1, "", ""))
	h.now += 1200
	h.notify("c", firing())
	h.now += 30
	h.press("ack", "", "", "")
	if counter(h.e, "incidents", "X", "warning", "") != 2 {
		t.Fatal("incidents")
	}
	if got := ackSum(t, h.e); got != 30 {
		t.Fatal(got)
	}
}

func ackSum(t *testing.T, e *Engine) float64 {
	t.Helper()
	mfs, err := e.Metrics.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() == "fogbell_ack_seconds" {
			return mf.Metric[0].Histogram.GetSampleSum()
		}
	}
	return -1
}
