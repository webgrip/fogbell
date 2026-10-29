package render

import (
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/webgrip/fogbell/internal/alert"
	"github.com/webgrip/fogbell/internal/config"
	"github.com/webgrip/fogbell/internal/slack"
	"github.com/webgrip/fogbell/internal/state"
)

const cfgText = `
alertmanager: {url: http://am.example}
objects:
  - name: pod
    labels: [namespace, pod]
    exclude: {pod: '^(kube-state-metrics|node-exporter)-'}
    button: Logs
    url: 'https://grafana.example/explore?q={{ printf "{namespace=%q,pod=%q}" .Labels.namespace .Labels.pod | queryescape }}&from={{ .FromMs }}'
  - name: node
    labels: [node]
    button: Node
    url: 'https://grafana.example/d/nodes?var-node={{ .Value | queryescape }}'
  - name: project
    labels: [project]
incidents:
  icons:
    - match: {alertgroup: 'audit|security'}
      icon: "🔐"
`

func renderer(t *testing.T) *Renderer {
	t.Helper()
	c, err := config.Parse([]byte(cfgText))
	if err != nil {
		t.Fatal(err)
	}
	return &Renderer{Config: c}
}

func fixture(t *testing.T, name string) *alert.Group {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/alerts/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var g alert.Group
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	return &g
}

func text(atts []slack.Attachment) string {
	var out []string
	for _, b := range atts[0].Blocks {
		if b.Text != nil {
			out = append(out, b.Text.Text)
		}
		for _, e := range b.Elements {
			if e.Label != nil {
				out = append(out, e.Label.Text)
			}
			out = append(out, e.Content, e.URL)
		}
	}
	return strings.Join(out, "\n")
}

func actions(atts []slack.Attachment) []slack.Element {
	for _, b := range atts[0].Blocks {
		if b.BlockID == "actions" {
			return b.Elements
		}
	}
	return nil
}

func TestSubjectLeadsAndLinks(t *testing.T) {
	r := renderer(t)
	g := fixture(t, "team-subject")
	atts := r.Render(g, &state.Incident{}, true, Features{})
	body := text(atts)
	if strings.Contains(body, "A merge request has waited a week") {
		t.Fatal("the headline is the message text, not a block")
	}
	if got := r.Title(g, false); got != "🟡 A merge request has waited a week for its first review · web" {
		t.Fatal(got)
	}
	for _, want := range []string{"*<https://git.example/acme/shop/-/merge_requests/6|shop!6>*", "→ Review it or close it"} {
		if !strings.Contains(body, want) {
			t.Fatal(want, body)
		}
	}
	if strings.Contains(body, "The author is blocked") {
		t.Fatal("no impact line")
	}
	row := actions(atts)
	if row[0].Label.Text != "Open" || row[0].Style != "primary" {
		t.Fatal(row[0])
	}
	var ids []string
	for _, e := range RowElements(row) {
		ids = append(ids, e.ActionID)
	}
	if !strings.Contains(strings.Join(ids, ","), "ack") || !strings.Contains(strings.Join(ids, ","), "silence") {
		t.Fatal(ids)
	}
	if strings.Contains(text(r.Render(g, &state.Incident{}, false, Features{})), "I'm on it") {
		t.Fatal("buttons that act without a signing secret")
	}
}

func TestPodLinkIsItsTemplateAndExportersGetNone(t *testing.T) {
	r := renderer(t)
	u, button := r.ObjectLink(fixture(t, "pod").Alerts[0])
	if button != "Logs" {
		t.Fatal(button)
	}
	p, _ := url.Parse(u)
	if got := p.Query().Get("q"); got != `{namespace="shop",pod="web-7d9f-abcde"}` {
		t.Fatal(got)
	}
	if !strings.Contains(u, "from=1793085300000") {
		t.Fatal(u)
	}
	exporter := fixture(t, "exporter-pod").Alerts[0]
	if u, b := r.ObjectLink(exporter); b != "Node" || !strings.Contains(u, "var-node=worker1") {
		t.Fatal("an exporter pod's object is its node", u, b)
	}
	if got := r.Config.ObjectReader().Subject(exporter); got != "worker1" {
		t.Fatal(got)
	}
}

func TestBundleShowsTenAndTheActionOnce(t *testing.T) {
	body := text(renderer(t).Render(fixture(t, "more-than-ten"), &state.Incident{}, true, Features{}))
	if !strings.Contains(body, "_10 of 12 shown") || strings.Count(body, "→ Read the previous container's log") != 1 {
		t.Fatal(body)
	}
}

func TestResolvedHasNoButtonsThatAct(t *testing.T) {
	r := renderer(t)
	g := fixture(t, "resolved")
	atts := r.Render(g, &state.Incident{ResolvedAt: 1793090400, StartedAt: 1793088900}, true, Features{})
	if got := r.Title(g, true); got != "✅ Resolved: A node is not ready" {
		t.Fatal(got)
	}
	if !strings.Contains(text(atts), "Resolved after 25 min") || strings.Contains(text(atts), "I'm on it") {
		t.Fatal(text(atts))
	}
	if atts[0].Color != "#2da44e" {
		t.Fatal(atts[0].Color)
	}
}

func TestIconsFollowTheConfigThenSeverity(t *testing.T) {
	r := renderer(t)
	g := fixture(t, "node")
	if got := r.Icon(g, false); got != "🔴" {
		t.Fatal(got)
	}
	g.CommonLabels["alertgroup"] = "security"
	if got := r.Icon(g, false); got != "🔐" {
		t.Fatal(got)
	}
	g.CommonLabels["alertgroup"] = "securityx"
	if got := r.Icon(g, false); got != "🔴" {
		t.Fatal("an icon match is anchored", got)
	}
}

func TestEscapesSlackControlCharacters(t *testing.T) {
	g := fixture(t, "pod")
	g.Alerts[0].Annotations["description"] = "a <b> & c"
	if body := text(renderer(t).Render(g, &state.Incident{}, false, Features{})); !strings.Contains(body, "a &lt;b&gt; &amp; c") {
		t.Fatal(body)
	}
}

func TestActionRowRoundTrips(t *testing.T) {
	var els []slack.Element
	for _, id := range []string{"noise", "link_chat", "silence", "ack", "link_runbook", "link_object", "ticket"} {
		els = append(els, slack.Button(id, id, ""))
	}
	row := ActionRow(els)
	if len(row) != RowMax || row[RowMax-1].Type != "overflow" {
		t.Fatal(row)
	}
	back := RowElements(row)
	if len(back) != len(els) || back[0].ActionID != "link_object" || back[len(back)-1].ActionID != "ticket" {
		t.Fatal(back)
	}
}
