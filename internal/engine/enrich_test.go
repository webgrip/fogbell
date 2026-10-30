package engine

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/webgrip/fogbell/internal/alert"
	"github.com/webgrip/fogbell/internal/chart"
	"github.com/webgrip/fogbell/internal/upstream"
)

var starts = alert.ParseTime("2026-10-27T08:15:00Z")

func newEnrichHarness(t *testing.T, extra string) *harness {
	h := newHarness(t, extra)
	h.now = starts + 120
	return h
}

type fakeKube struct {
	ks, hr []map[string]any
	fail   int
	calls  int
}

func (k *fakeKube) Get(_ context.Context, path string, out any) error {
	k.calls++
	if k.fail != 0 {
		return &upstream.StatusError{URL: path, Code: k.fail}
	}
	items := k.ks
	if strings.Contains(path, "helm") {
		items = k.hr
	}
	raw, _ := json.Marshal(map[string]any{"items": items})
	return json.Unmarshal(raw, out)
}

type applied struct {
	sha string
	at  float64
}

func kustomization(name, ns string, history ...applied) map[string]any {
	var hs []map[string]any
	for _, a := range history {
		hs = append(hs, map[string]any{"firstReconciled": iso(a.at), "metadata": map[string]string{"revision": "refs/heads/main@sha1:" + a.sha}})
	}
	return map[string]any{"metadata": map[string]string{"name": name, "namespace": "flux-system"},
		"spec":   map[string]any{"targetNamespace": ns, "sourceRef": map[string]string{"kind": "GitRepository", "name": "flux-system"}},
		"status": map[string]any{"history": hs}}
}

const fluxConfig = "enrich:\n  flux:\n    enabled: true\n    commitURL: https://git.example/-/commit/\n    pathURL: https://git.example/-/commits/main/\n"

func TestTheChangeBeforeTheAlert(t *testing.T) {
	h := newEnrichHarness(t, fluxConfig)
	h.kube(&fakeKube{ks: []map[string]any{
		kustomization("ns-app", "ns", applied{strings.Repeat("a", 40), starts - 720}, applied{strings.Repeat("b", 40), starts - 9000}),
		kustomization("other", "elsewhere", applied{strings.Repeat("c", 40), starts - 60})}})
	h.notify("c", firing())
	want := "🔧 Kustomization `ns-app` applied <https://git.example/-/commit/" + strings.Repeat("a", 40) + "|aaaaaaaa> 12 min before this alert"
	if got := enriched(h.only()); len(got) != 1 || got[0] != want {
		t.Fatal(got)
	}
	h.notify("c", group("", 2, "", ""))
	if got := enriched(h.only()); len(got) != 1 {
		t.Fatal("the line did not survive the re-render", got)
	}
}

func TestAChangeReconciledAgainNamesItsPath(t *testing.T) {
	h := newEnrichHarness(t, fluxConfig)
	ks := kustomization("grafana", "ns", applied{strings.Repeat("e", 40), starts - 900})
	ks["spec"].(map[string]any)["path"] = "./apps/grafana/app"
	ks["status"].(map[string]any)["history"].([]map[string]any)[0]["totalReconciliations"] = 14
	h.kube(&fakeKube{ks: []map[string]any{ks}})
	h.notify("c", firing())
	want := "🔧 Kustomization `grafana` applied a change in <https://git.example/-/commits/main/apps/grafana/app|apps/grafana/app> 15 min before this alert"
	if got := enriched(h.only()); len(got) != 1 || got[0] != want {
		t.Fatal(got)
	}
}

func TestHelmUpgradeAndNothingAfterTheStart(t *testing.T) {
	h := newEnrichHarness(t, fluxConfig)
	hr := map[string]any{"metadata": map[string]string{"name": "web", "namespace": "ns"}, "spec": map[string]any{},
		"status": map[string]any{"history": []map[string]string{{"chartName": "web", "chartVersion": "1.2.3", "lastDeployed": iso(starts - 300)}}}}
	h.kube(&fakeKube{ks: []map[string]any{kustomization("ns-app", "ns", applied{strings.Repeat("d", 40), starts + 60})}, hr: []map[string]any{hr}})
	h.notify("c", firing())
	if got := enriched(h.only()); len(got) != 1 || got[0] != "🔧 HelmRelease `web` upgraded to `web 1.2.3` 5 min before this alert" {
		t.Fatal(got)
	}
}

func TestNoChangeInTheWindowAddsNothing(t *testing.T) {
	h := newEnrichHarness(t, fluxConfig)
	h.kube(&fakeKube{ks: []map[string]any{kustomization("ns-app", "ns", applied{strings.Repeat("a", 40), starts - 7200})}})
	h.notify("c", firing())
	if got := enriched(h.only()); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestWithoutRBACItPostsAndCounts(t *testing.T) {
	h := newEnrichHarness(t, fluxConfig)
	h.kube(&fakeKube{fail: 403})
	if got := h.notify("c", firing()); got != "posted" || len(enriched(h.only())) != 0 {
		t.Fatal(got)
	}
	if counter(h.e, "enrichment", "change", "forbidden") != 1 {
		t.Fatal("forbidden not counted")
	}
}

func (h *harness) withLogs() {
	h.wired.Logs = &upstream.Logs{URL: h.up.URL + "/vl", HTTP: h.up.Client()}
	h.e.Wire(h.wired)
}

func (h *harness) kube(k *fakeKube) {
	h.wired.Kube = k
	h.e.Wire(h.wired)
}

const (
	sparkPoints  = 30
	timelineBase = 12 * 3600
)

func logRows(msgs ...string) string {
	var out []string
	for i := len(msgs) - 1; i >= 0; i-- {
		raw, _ := json.Marshal(map[string]string{"_time": fmt.Sprintf("2026-10-27T08:1%d:00Z", i), "_msg": msgs[i]})
		out = append(out, string(raw))
	}
	return strings.Join(out, "\n")
}

func TestLastLogLinesOfThePod(t *testing.T) {
	h := newEnrichHarness(t, "")
	h.withLogs()
	h.up.on("/vl/select/logsql/query", "", ok(logRows("starting", "connect db: password=hunter22",
		"Authorization: Bearer abcdefghijklmnop", strings.Repeat("x", 400), "panic: ``` boom")))
	h.notify("c", firing())
	q := h.up.reqs()[0].query.Get("query")
	if !strings.Contains(q, `_stream:{k8s_namespace="ns",k8s_pod="p0"}`) || !strings.Contains(q, "limit 15") {
		t.Fatal(q)
	}
	text := h.slack.LastReply()
	if !strings.HasPrefix(text, "📜 Last 5 log lines of ns/p0") {
		t.Fatal(text)
	}
	body := strings.SplitN(text, "```\n", 2)[1]
	if strings.Index(body, "starting") > strings.Index(body, "panic") {
		t.Fatal("not oldest first")
	}
	for _, bad := range []string{"hunter22", "abcdefghijklmnop", strings.Repeat("x", 241), "``` boom"} {
		if strings.Contains(body, bad) {
			t.Fatal(bad)
		}
	}
	if !strings.Contains(body, "password=[redacted]") || !strings.Contains(body, strings.Repeat("x", 240)+" …") {
		t.Fatal(body)
	}
}

func TestLogLineCarriesItsErrorField(t *testing.T) {
	h := newEnrichHarness(t, "")
	h.withLogs()
	row, _ := json.Marshal(map[string]string{"_time": "2026-10-27T08:10:00Z", "_msg": "Reconciler error", "error": `cannot find secret data for key: "token"`})
	h.up.on("/vl/select/logsql/query", "", ok(string(row)))
	h.notify("c", firing())
	if !strings.Contains(h.up.reqs()[0].query.Get("query"), "error, err") {
		t.Fatal(h.up.reqs()[0].query)
	}
	if !strings.Contains(h.slack.LastReply(), `Reconciler error · cannot find secret data for key: "token"`) {
		t.Fatal(h.slack.LastReply())
	}
}

func TestControllerLogsNarrowedToTheObjectOrFallBack(t *testing.T) {
	h := newEnrichHarness(t, "")
	h.withLogs()
	g := firing()
	g.Alerts[0].Labels["exported_namespace"], g.Alerts[0].Labels["name"] = "app", "creds"
	h.up.on("/vl/select/logsql/query", "", ok(logRows("Reconciler error")))
	h.notify("c", g)
	if q := h.up.reqs()[0].query.Get("query"); !strings.Contains(q, `namespace:="app" name:="creds"`) || len(h.up.reqs()) != 1 {
		t.Fatal(q)
	}
	if !strings.Contains(h.slack.LastReply(), "of ns/p0 about app/creds") {
		t.Fatal(h.slack.LastReply())
	}

	h2 := newEnrichHarness(t, "")
	h2.withLogs()
	h2.up.on("/vl/select/logsql/query", "", ok(""), ok(logRows("starting")))
	h2.notify("c", g)
	if len(h2.up.reqs()) != 2 || strings.Contains(h2.up.reqs()[1].query.Get("query"), "name:=") {
		t.Fatal(h2.up.reqs())
	}
	if !strings.HasPrefix(h2.slack.LastReply(), "📜 Last 1 log lines of ns/p0 in") {
		t.Fatal(h2.slack.LastReply())
	}
}

func TestLogsOnlyForOnePodAndNeverFailThePost(t *testing.T) {
	h := newEnrichHarness(t, "")
	h.withLogs()
	h.notify("c", group("", 3, "", ""))
	if len(h.up.reqs()) != 0 || len(h.slack.Replies) != 0 {
		t.Fatal(h.up.reqs())
	}
	h2 := newEnrichHarness(t, "")
	h2.withLogs()
	h2.up.on("/vl/select/logsql/query", "", fail(503))
	if got := h2.notify("c", firing()); got != "posted" || len(h2.slack.Replies) != 0 {
		t.Fatal(got)
	}
	if counter(h2.e, "enrichment", "logs", "error") != 1 {
		t.Fatal("logs error not counted")
	}
}

const gen = "http://vmalert-vmalert-7894f78754-sgnr6:8080/vmalert/alert?group_id=11&alert_id=22"

func sparkGroup(generator string) *alert.Group {
	g := firing()
	g.Alerts[0].GeneratorURL = generator
	return g
}

func rule(expr string) answer {
	raw, _ := json.Marshal(map[string]string{"expression": expr})
	return ok(string(raw))
}

func seriesOf(metric map[string]string, values ...float64) string {
	var pairs [][2]any
	for i, v := range values {
		pairs = append(pairs, [2]any{i, fmt.Sprint(v)})
	}
	raw, _ := json.Marshal(map[string]any{"data": map[string]any{"result": []map[string]any{{"metric": metric, "values": pairs}}}})
	return string(raw)
}

const empty = `{"data": {"result": []}}`

func TestSparklineOfTheExpression(t *testing.T) {
	h := newEnrichHarness(t, "")
	h.withMetrics()
	h.up.on("/vmalert/api/v1/alert", "", rule("sum by (namespace) (rate(errors[5m])) > 3"))
	h.up.on("/vm/api/v1/query_range", "", ok(`{"data": {"result": [
		{"metric": {"namespace": "other"}, "values": [[1, "100"], [2, "100"]]},
		{"metric": {"namespace": "ns"}, "values": [[1, "1"], [2, "2"], [3, "NaN"], [4, "8"]]}]}}`))
	h.up.on("/vm/api/v1/query", "", ok(`{"data": {"result": [{"metric": {"namespace": "ns"}, "value": [1, "2.5"]}]}}`))
	h.notify("c", sparkGroup(gen))
	reqs := h.up.reqs()
	if reqs[1].query.Get("query") != "sum(rate(errors[5m])) by(namespace)" {
		t.Fatal(reqs[1].query)
	}
	if reqs[2].query.Get("time") != fmt.Sprint(int64(h.now)-7*86400) {
		t.Fatal(reqs[2].query)
	}
	want := "📈 `▁▂ █` last 3 h · min 1 · max 8 · now 8 · threshold > 3 (2.7×) · a week ago 2.5"
	if got := enriched(h.only()); len(got) != 1 || got[0] != want {
		t.Fatal(got)
	}
}

func TestTheThresholdSetsTheScaleAndTheTrendShows(t *testing.T) {
	h := newEnrichHarness(t, "")
	h.withMetrics()
	h.up.on("/vmalert/api/v1/alert", "", rule("x > 100"))
	h.up.on("/vm/api/v1/query_range", "", ok(seriesOf(map[string]string{}, 10, 20, 30, 40, 50, 60)))
	h.up.on("/vm/api/v1/query", "", fail(503))
	h.notify("c", sparkGroup(gen))
	want := "📈 `▁▁▂▃▄▅` last 3 h · min 10 · max 60 · now 60 · threshold > 100 · ↗ rising"
	if got := enriched(h.only()); len(got) != 1 || got[0] != want {
		t.Fatal(got)
	}
}

func TestAChangeDrawsWhatChanged(t *testing.T) {
	h := newEnrichHarness(t, "")
	h.withMetrics()
	h.up.on("/vmalert/api/v1/alert", "", rule("delta_prometheus((\n  sum by (team) (q[2h])\n)[1d:30m]) > 0"))
	h.up.on("/vm/api/v1/query_range", "", ok(seriesOf(map[string]string{"team": "a"}, 2, 2, 3, 5, 7, 9)))
	h.up.on("/vm/api/v1/query", "", ok(empty))
	h.notify("c", sparkGroup(gen))
	q := h.up.reqs()[1].query
	if q.Get("query") != "sum(q[2h]) by(team)" {
		t.Fatal(q)
	}
	var start, end int64
	if _, err := fmt.Sscan(q.Get("start")+" "+q.Get("end"), &start, &end); err != nil {
		t.Fatal(err)
	}
	if end-start != 4*86400 {
		t.Fatal(end - start)
	}
	want := "📈 `▁▁▂▄▆█` last 4 d, the value before `delta_prometheus()` · min 2 · max 9 · now 9 · ↗ rising"
	if got := enriched(h.only()); len(got) != 1 || got[0] != want {
		t.Fatal(got)
	}
}

func (h *harness) alertsSeries(values ...*float64) string {
	now, step := int64(h.now), int64(timelineBase/sparkPoints)
	var pairs [][2]any
	for i, v := range values {
		if v != nil {
			pairs = append(pairs, [2]any{now - int64(len(values)-1-i)*step, fmt.Sprint(*v)})
		}
	}
	raw, _ := json.Marshal(map[string]any{"data": map[string]any{"result": []map[string]any{{"metric": map[string]string{}, "values": pairs}}}})
	return string(raw)
}

func f(v float64) *float64 { return &v }

func TestAStateGetsItsFiringTimeline(t *testing.T) {
	h := newEnrichHarness(t, "")
	h.withMetrics()
	h.up.on("/vmalert/api/v1/alert", "", rule("up == 0"))
	fired := make([]*float64, 21)
	fired = append(fired, f(1), f(1), nil, nil, nil, f(1), f(1), f(1), f(1))
	h.up.on("/vm/api/v1/query_range", "count(ALERTS", ok(h.alertsSeries(fired...)))
	h.up.on("/vm/api/v1/query_range", "", ok(seriesOf(map[string]string{}, 0, 1, 1)))
	if got := h.notify("c", sparkGroup(gen)); got != "posted" {
		t.Fatal(got)
	}
	reqs := h.up.reqs()
	if q := reqs[len(reqs)-1].query.Get("query"); q != `count(ALERTS{alertname="X",namespace="ns",pod="p0",alertstate="firing"})` {
		t.Fatal(q)
	}
	line := enriched(h.only())[0]
	if !strings.HasPrefix(line, "🚦 `"+strings.Repeat("▁", 21)+"██▁▁▁████` last 12 h · firing since <!date^") {
		t.Fatal(line)
	}
	if !strings.HasSuffix(line, "(2 min) · 2 firings in this window") {
		t.Fatal(line)
	}
	if counter(h.e, "enrichment", "spark", "timeline") != 1 {
		t.Fatal("timeline not counted")
	}
}

func TestOtherRulersAndCompoundRulesGetTheTimeline(t *testing.T) {
	h := newEnrichHarness(t, "enrich:\n  ruler:\n    generatorHost: '^vmalert-vmalert-[0-9a-f]{6,10}-[0-9a-z]{5}$'\n")
	h.withMetrics()
	g := sparkGroup("http://vmalert-vmalert-audit-7bd66dbc6-jrbcv:8080/vmalert/alert?group_id=1&alert_id=2")
	second := g.Alerts[0]
	second.Labels = map[string]string{"alertname": "X", "severity": "warning", "namespace": "ns", "pod": "p1"}
	g.Alerts = append(g.Alerts, second)
	values := make([]*float64, 25)
	values = append(values, f(1), f(1), f(2), f(2), f(3))
	h.up.on("/vm/api/v1/query_range", "count(ALERTS", ok(h.alertsSeries(values...)))
	h.notify("c", g)
	if len(h.up.reqs()) != 1 {
		t.Fatal("asked the ruler for another ruler's alert", h.up.reqs())
	}
	if line := enriched(h.only())[0]; !strings.HasPrefix(line, "🚦 `"+strings.Repeat("▁", 25)+"▃▃▆▆█` alerts firing at once, last 12 h · peak 3 · now 3 · firing since") {
		t.Fatal(line)
	}

	h1 := newEnrichHarness(t, "enrich:\n  ruler:\n    generatorHost: '^vmalert-vmalert-[0-9a-f]{6,10}-[0-9a-z]{5}$'\n")
	h1.withMetrics()
	h1.up.on("/vm/api/v1/query_range", "count(ALERTS", ok(empty))
	h1.notify("c", sparkGroup("http://vmalert-vmalert-audit-7bd66dbc6-jrbcv:8080/vmalert/alert?group_id=1&alert_id=2"))
	for _, r := range h1.up.reqs() {
		if strings.HasPrefix(r.path, "/vmalert") {
			t.Fatal("asked the ruler about a generator it does not own")
		}
	}

	h2 := newEnrichHarness(t, "")
	h2.withMetrics()
	h2.up.on("/vmalert/api/v1/alert", "", rule("sum(a) > 0 unless sum(b) > 0"))
	h2.up.on("/vm/api/v1/query_range", "count(ALERTS", ok(empty))
	h2.notify("c", sparkGroup(gen))
	if len(h2.up.reqs()) != 2 || len(enriched(h2.only())) != 0 {
		t.Fatal(h2.up.reqs())
	}
}

func TestAPrometheusGeneratorURLCarriesTheExpression(t *testing.T) {
	h := newEnrichHarness(t, "")
	h.withMetrics()
	h.up.on("/vm/api/v1/query_range", "", ok(seriesOf(map[string]string{}, 1, 2, 3, 9)))
	h.up.on("/vm/api/v1/query", "", ok(empty))
	h.notify("c", sparkGroup("http://prometheus:9090/graph?g0.expr=x+%3E+5&g0.tab=1"))
	if q := h.up.reqs()[0].query.Get("query"); q != "x" {
		t.Fatal(h.up.reqs())
	}
	if got := enriched(h.only()); len(got) != 1 || !strings.Contains(got[0], "threshold > 5 (1.8×)") {
		t.Fatal(got)
	}
}

func TestSparklineQueryFailureStillPosts(t *testing.T) {
	h := newEnrichHarness(t, "")
	h.withMetrics()
	h.up.on("/vmalert/api/v1/alert", "", rule("up == 0"))
	h.up.on("/vm/api/v1/query_range", "", fail(504))
	if got := h.notify("c", sparkGroup(gen)); got != "posted" || len(enriched(h.only())) != 0 {
		t.Fatal(got)
	}
}

func TestAChartInTheThread(t *testing.T) {
	h := newEnrichHarness(t, "enrich:\n  charts: true\n")
	h.withMetrics()
	h.up.on("/vmalert/api/v1/alert", "", rule("x > 5"))
	now, step := int64(h.now), int64(3*3600/chart.Points)
	var series, fired [][2]any
	for i := 0; i < 240; i++ {
		series = append(series, [2]any{now - int64(239-i)*step, fmt.Sprint(float64(i) / 20)})
		if i >= 200 {
			fired = append(fired, [2]any{now - int64(239-i)*step, "1"})
		}
	}
	wrap := func(p [][2]any) string {
		raw, _ := json.Marshal(map[string]any{"data": map[string]any{"result": []map[string]any{{"metric": map[string]string{}, "values": p}}}})
		return string(raw)
	}
	h.up.on("/vm/api/v1/query_range", "count(ALERTS", ok(wrap(fired)))
	h.up.on("/vm/api/v1/query_range", "", ok(wrap(series)))
	h.up.on("/vm/api/v1/query", "", ok(empty))
	h.notify("c", sparkGroup(gen))
	if len(h.slack.Uploads) != 1 {
		t.Fatal(h.slack.Uploads)
	}
	up := h.slack.Uploads[0]
	if !strings.HasPrefix(string(up.Data), "\x89PNG\r\n\x1a\n") {
		t.Fatal("not a PNG")
	}
	if w, ht := binary.BigEndian.Uint32(up.Data[16:20]), binary.BigEndian.Uint32(up.Data[20:24]); w != chart.Width || ht != chart.Height {
		t.Fatal(w, ht)
	}
	if up.Filename != "X.png" || up.ThreadTS != h.firstTS() {
		t.Fatal(up)
	}
	if up.Comment != "📊 X: the rule's value over the last 3 h, threshold dashed, firing shaded." {
		t.Fatal(up.Comment)
	}
	if counter(h.e, "enrichment", "chart", "ok") != 1 {
		t.Fatal("chart not counted")
	}
}

func TestNoChartWithoutTheSwitchOrWhenTheUploadFails(t *testing.T) {
	h := newEnrichHarness(t, "")
	h.withMetrics()
	h.up.on("/vm/api/v1/query_range", "", ok(empty))
	h.notify("c", firing())
	if len(h.slack.Uploads) != 0 {
		t.Fatal("charted without the switch")
	}
	h2 := newEnrichHarness(t, "enrich:\n  charts: true\n")
	h2.withMetrics()
	h2.slack.Fail["upload"] = "refused"
	raw, _ := json.Marshal(map[string]any{"data": map[string]any{"result": []map[string]any{{"metric": map[string]string{}, "values": [][2]any{{int64(h2.now), "1"}}}}}})
	h2.up.on("/vm/api/v1/query_range", "count(ALERTS", ok(string(raw)))
	if got := h2.notify("c", firing()); got != "posted" || len(h2.slack.Uploads) != 0 {
		t.Fatal(got, h2.slack.Uploads)
	}
	if counter(h2.e, "enrichment", "chart", "error") != 1 {
		t.Fatal("upload failure not counted")
	}
}

func TestAFiringIncidentIsRedrawnInPlace(t *testing.T) {
	h := newEnrichHarness(t, "")
	h.withMetrics()
	h.up.on("/vmalert/api/v1/alert", "", rule("x > 3"))
	h.up.on("/vm/api/v1/query", "", ok(empty))
	h.up.on("/vm/api/v1/query_range", "", ok(seriesOf(map[string]string{}, 1, 2, 4)), ok(seriesOf(map[string]string{}, 1, 2, 4)),
		ok(seriesOf(map[string]string{}, 1, 2, 4, 9)))
	h.notify("c", sparkGroup(gen))
	h.now += 200
	if got := h.maintain(); contains(got, "refreshed") {
		t.Fatal(got)
	}
	h.now += 120
	if got := h.maintain(); !contains(got, "same") {
		t.Fatal(got)
	}
	h.now += 301
	if got := h.maintain(); !contains(got, "refreshed") {
		t.Fatal(got)
	}
	if !strings.Contains(enriched(h.only())[0], "now 9") {
		t.Fatal(enriched(h.only()))
	}
	h.notify("c", group("resolved", 1, "", ""))
	before := len(h.up.reqs())
	h.now += 600
	h.maintain()
	if len(h.up.reqs()) != before {
		t.Fatal("a resolved incident was redrawn")
	}
}

func TestTheRulesLastWeekInThisChannel(t *testing.T) {
	h := newEnrichHarness(t, "")
	for i, c := range []struct {
		acked string
		votes int
	}{{"U1", 0}, {"", 2}} {
		h.now = starts + 120 + float64(i)*86400
		h.notify("c", group("", 1, fmt.Sprintf("k%d", i), ""))
		ts := h.slack.Newest().TS
		h.slack.Lock()
		m := h.slack.Messages[ts]
		var st map[string]any
		_ = json.Unmarshal(m.Metadata.EventPayload, &st)
		st["acked_by"], st["noise_votes"], st["state"], st["resolved_at"] = c.acked, c.votes, "resolved", st["fired_at"].(float64)+840
		m.Metadata.EventPayload, _ = json.Marshal(st)
		h.slack.Unlock()
	}
	h.now += 3600
	h.notify("c", group("", 1, "k9", ""))
	newest := h.slack.Newest()
	want := "🕘 Fired 2 times before in 7 days · usually over in 14 min · 🙈 2 noise votes · last picked up by <@U1>"
	if got := enriched(newest); len(got) != 1 || got[0] != want {
		t.Fatal(got)
	}
	if strings.Contains(textOf(newest), "time this week") {
		t.Fatal(textOf(newest))
	}
}

func TestAlsoFiringBesideIt(t *testing.T) {
	h := newEnrichHarness(t, "enrich:\n  related: true\n")
	g := firing()
	g.Alerts[0].Fingerprint = "mine"
	g.Alerts[0].Labels["node"] = "worker02"
	h.am.firing = []upstream.FiringAlert{
		{Fingerprint: "mine", Labels: map[string]string{"alertname": "X", "namespace": "ns", "node": "worker02"}},
		{Fingerprint: "a", Labels: map[string]string{"alertname": "KubePodCrashLooping", "namespace": "ns"}},
		{Fingerprint: "b", Labels: map[string]string{"alertname": "KubePodCrashLooping", "namespace": "ns"}},
		{Fingerprint: "c", Labels: map[string]string{"alertname": "NodeHighLoad", "node": "worker02"}},
		{Fingerprint: "d", Labels: map[string]string{"alertname": "Watchdog", "namespace": "ns"}},
		{Fingerprint: "e", Labels: map[string]string{"alertname": "Other", "namespace": "elsewhere"}},
	}
	h.notify("c", g)
	want := "🧩 Also firing in `ns`: KubePodCrashLooping ×2 · on `worker02`: NodeHighLoad"
	if got := enriched(h.only()); len(got) != 1 || got[0] != want {
		t.Fatal(got)
	}
	h2 := newEnrichHarness(t, "enrich:\n  related: true\n")
	h2.notify("c", firing())
	if len(enriched(h2.only())) != 0 || counter(h2.e, "enrichment", "related", "none") != 1 {
		t.Fatal(enriched(h2.only()))
	}
}

func TestWithoutUpstreamsNothingIsLookedUp(t *testing.T) {
	h := newEnrichHarness(t, "")
	if got := h.notify("c", firing()); got != "posted" || len(enriched(h.only())) != 0 || len(h.up.reqs()) != 0 {
		t.Fatal(got)
	}
}
