package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/webgrip/fogbell/internal/alert"
)

func teamGroup(team string) *alert.Group {
	g := group("", 1, `{}:{team="`+team+`"}`, "teams")
	g.CommonLabels["team"] = team
	return g
}

const teamConfig = "channels:\n  alerts: [teams]\n  teams: {team-b: team-b-alerts, team-d: '#team-d-alerts'}\n"

func TestATeamWithAChannelGetsIt(t *testing.T) {
	h := newHarness(t, teamConfig)
	for _, team := range []string{"team-b", "team-x"} {
		if _, err := h.e.Notify(context.Background(), "teams", teamGroup(team), true); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Join(h.slack.Posted, ","); got != "CTEAMB,CTEAMS" {
		t.Fatal(got)
	}
}

func TestTeamChannelsOnlyOnAByTeamReceiver(t *testing.T) {
	h := newHarness(t, teamConfig)
	h.notify("alerts", teamGroup("team-b"))
	if got := strings.Join(h.slack.Posted, ","); got != "CALERTS" {
		t.Fatal(got)
	}
}

func TestAChannelWithoutTheAppFallsBack(t *testing.T) {
	h := newHarness(t, teamConfig)
	h.slack.Closed["CTEAMB"] = true
	if got, err := h.e.Notify(context.Background(), "teams", teamGroup("team-b"), true); err != nil || got != "posted" {
		t.Fatal(got, err)
	}
	if got := strings.Join(h.slack.Posted, ","); got != "CTEAMS" {
		t.Fatal(got)
	}
	if counter(h.e, "degraded", "team_channel") != 1 {
		t.Fatal("fallback not counted")
	}
	if got, _ := h.e.Notify(context.Background(), "teams", teamGroup("team-b"), true); got != "updated" {
		t.Fatal(got)
	}
}

func TestTeamChannelsJoinTheReports(t *testing.T) {
	h := newHarness(t, teamConfig+"  digest: [warn]\n")
	if got := strings.Join(h.e.Config.DigestChannels(), ","); got != "warn,team-b-alerts,team-d-alerts" {
		t.Fatal(got)
	}
}

func nodeDown(node string) *alert.Group {
	return &alert.Group{GroupKey: "node", Receiver: "page", Status: "firing",
		GroupLabels:  map[string]string{"alertname": "KubeNodeNotReady"},
		CommonLabels: map[string]string{"alertname": "KubeNodeNotReady", "severity": "critical", "node": node},
		Alerts: []alert.Alert{{Status: "firing", StartsAt: "2026-10-27T08:15:00Z", EndsAt: "0001-01-01T00:00:00Z",
			Labels:      map[string]string{"alertname": "KubeNodeNotReady", "severity": "critical", "node": node},
			Annotations: map[string]string{"summary": "A node is not ready", "subject": node}}}}
}

func podDown(key string, n int, node, status string) *alert.Group {
	g := group(status, n, key, "")
	for i := range g.Alerts {
		if node != "" {
			g.Alerts[i].Labels["node"] = node
		}
	}
	return g
}

func newFoldHarness(t *testing.T) (*harness, string) {
	h := newHarness(t, "channels:\n  alerts: [alerts, warnings]\n")
	h.notify("alerts", nodeDown("worker02"))
	return h, h.firstTS()
}

func (h *harness) thread(ts string) []string {
	h.slack.Lock()
	defer h.slack.Unlock()
	var out []string
	for _, r := range h.slack.Replies {
		if r.ThreadTS == ts {
			out = append(out, r.Text)
		}
	}
	return out
}

func TestPodAlertsOnTheNodeFoldIntoItsThread(t *testing.T) {
	h, nodeTS := newFoldHarness(t)
	h.now += 300
	if got := h.notify("warnings", podDown("pods", 1, "worker02", "")); got != "nested" {
		t.Fatal(got)
	}
	if len(h.slack.Messages) != 1 {
		t.Fatal("a folded alert got its own message")
	}
	th := h.thread(nodeTS)
	if !strings.Contains(th[len(th)-1], "↳ 🟡 *A thing broke* on `worker02`, folded in here") {
		t.Fatal(th)
	}
	if got := h.notify("warnings", podDown("pods", 1, "worker02", "")); got != "nested-unchanged" {
		t.Fatal(got)
	}
	if got := h.notify("warnings", podDown("pods", 2, "worker02", "")); got != "nested-updated" {
		t.Fatal(got)
	}
	th = h.thread(nodeTS)
	if !strings.Contains(th[len(th)-1], "➕ 1 more firing") {
		t.Fatal(th)
	}
	h.restart()
	h.maintain()
	if got := h.notify("warnings", podDown("pods", 1, "worker02", "resolved")); got != "nested-resolved" {
		t.Fatal(got)
	}
	th = h.thread(nodeTS)
	if !strings.Contains(th[len(th)-1], "↳ ✅ Resolved: A thing broke") || len(h.slack.Messages) != 1 {
		t.Fatal(th)
	}
}

func TestTheNodeIsFoundThroughKubePodInfo(t *testing.T) {
	h, _ := newFoldHarness(t)
	h.withMetrics()
	h.up.on("/vm/api/v1/query", `kube_pod_info{namespace="ns",pod="p0"}`, ok(`{"data": {"result": [{"metric": {"node": "worker02"}}]}}`))
	if got := h.notify("warnings", podDown("pods", 1, "", "")); got != "nested" {
		t.Fatal(got)
	}
}

func TestOtherNodesLateAlertsAndUnknownNodesPostAsUsual(t *testing.T) {
	h, nodeTS := newFoldHarness(t)
	for _, c := range []struct {
		key, node string
	}{{"a", "worker03"}, {"b", ""}} {
		if got := h.notify("warnings", podDown(c.key, 1, c.node, "")); got != "posted" {
			t.Fatal(c, got)
		}
	}
	disk := podDown("d", 1, "worker02", "")
	delete(disk.Alerts[0].Labels, "pod")
	if got := h.notify("warnings", disk); got != "posted" {
		t.Fatal(got)
	}
	h.now += 1801
	if got := h.notify("warnings", podDown("c", 1, "worker02", "")); got != "posted" {
		t.Fatal(got)
	}
	if th := h.thread(nodeTS); len(th) != 0 {
		t.Fatal(th)
	}
}

func TestAfterTheNodeResolvesThePodAlertStandsAlone(t *testing.T) {
	h, _ := newFoldHarness(t)
	h.notify("warnings", podDown("pods", 1, "worker02", ""))
	resolved := nodeDown("worker02")
	resolved.Status = "resolved"
	h.notify("alerts", resolved)
	if got := h.notify("warnings", podDown("pods", 2, "worker02", "")); got != "posted" {
		t.Fatal(got)
	}
}
