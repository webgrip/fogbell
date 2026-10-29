package engine

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/fogbell-dev/fogbell/internal/alert"
	"github.com/fogbell-dev/fogbell/internal/chart"
	"github.com/fogbell-dev/fogbell/internal/format"
	"github.com/fogbell-dev/fogbell/internal/promql"
	"github.com/fogbell-dev/fogbell/internal/redact"
	"github.com/fogbell-dev/fogbell/internal/spark"
	"github.com/fogbell-dev/fogbell/internal/state"
	"github.com/fogbell-dev/fogbell/internal/upstream"
)

const (
	sparkHours   = 3
	sparkPoints  = 30
	logLines     = 15
	logLineMax   = 240
	timelineBase = 12 * 3600
)

// enrich adds what the first post did not wait for: the pod's last log lines as a thread reply; a
// sparkline or firing timeline, the rule's last week in this channel, what else fires beside it and
// the change before it as lines on the message; and the chart in the thread. Every step is optional
// and never fails the incident.
func (e *Engine) enrich(ctx context.Context, channel, ts, key string, g *alert.Group) {
	logs, err := e.lastLogs(ctx, g)
	if err != nil {
		e.Metrics.Enriched("logs", "error")
		e.Log.Warn("enrich logs failed", "error", err)
	}
	if logs != "" {
		e.reply(ctx, channel, ts, logs)
	}
	extra := map[string]string{}
	for _, step := range []struct {
		name string
		fn   func(context.Context, *alert.Group) (string, error)
	}{
		{"spark", e.spark},
		{"history", func(ctx context.Context, g *alert.Group) (string, error) {
			return e.ruleHistory(ctx, channel, ts, g), nil
		}},
		{"related", e.related},
		{"change", e.changeBefore},
	} {
		line, err := step.fn(ctx, g)
		if err != nil {
			e.Metrics.Enriched(step.name, "error")
			e.Log.Warn("enrich failed", "kind", step.name, "error", err)
			continue
		}
		if line != "" {
			extra[step.name] = line
		}
	}
	result, err := e.chart(ctx, channel, ts, g)
	if err != nil {
		result = "error"
		e.Log.Warn("enrich chart failed", "error", err)
	}
	e.Metrics.Enriched("chart", result)
	if len(extra) == 0 {
		return
	}
	l := e.lock(key)
	l.Lock()
	defer l.Unlock()
	en, ok := e.get(key)
	if !ok || en.TS != ts {
		return
	}
	st := en.St
	setLines(&st, extra)
	cur := e.group(key)
	if cur == nil {
		cur = g
	}
	if err := e.update(ctx, en.Channel, ts, cur, &st); err != nil {
		e.Log.Warn("enrich update failed", "error", err)
		return
	}
	e.put(key, entry{en.Channel, ts, st})
}

func setLines(st *state.Incident, lines map[string]string) {
	for k, v := range lines {
		switch k {
		case "spark":
			st.Spark = v
		case "history":
			st.History = v
		case "related":
			st.Related = v
		case "change":
			st.Change = v
		}
	}
}

func (e *Engine) ruleExpression(ctx context.Context, g *alert.Group) (string, error) {
	active := g.Active()
	if len(active) != 1 || e.Ruler == nil {
		return "", nil
	}
	return e.Ruler.Expression(ctx, active[0].GeneratorURL)
}

func (e *Engine) queryRange(ctx context.Context, expr string, window int64, points int) ([]upstream.Series, int64, error) {
	step := max(60, window/int64(points))
	result, err := e.Prom.QueryRange(ctx, expr, e.Now().Unix(), window, step)
	return result, step, err
}

// spark draws what the alert measures: the series it compares (or, for a change over a window, the
// series that changed) with min, max, now, its threshold, the trend and a week ago. A rule with
// nothing to draw (a 0/1 state, absent(), a set operator, another ruler) gets its firing timeline
// instead. "" when neither works.
func (e *Engine) spark(ctx context.Context, g *alert.Group) (string, error) {
	if e.Prom == nil {
		e.Metrics.Enriched("spark", "disabled")
		return "", nil
	}
	raw, err := e.ruleExpression(ctx, g)
	if err != nil {
		return "", err
	}
	expr, changed := promql.Unwrap(promql.PlainSeries(raw))
	if expr != "" {
		line, err := e.seriesSpark(ctx, expr, raw, changed, g.First().Labels)
		if err != nil {
			return "", err
		}
		if line != "" {
			e.Metrics.Enriched("spark", "ok")
			return line, nil
		}
	}
	line, err := e.firingTimeline(ctx, g, raw)
	if err != nil {
		return "", err
	}
	if line != "" {
		e.Metrics.Enriched("spark", "timeline")
	} else {
		e.Metrics.Enriched("spark", "none")
	}
	return line, nil
}

func (e *Engine) seriesSpark(ctx context.Context, expr, raw, changed string, labels map[string]string) (string, error) {
	window := promql.Window(raw, sparkHours*3600)
	result, _, err := e.queryRange(ctx, expr, window, sparkPoints)
	if err != nil {
		return "", err
	}
	series := upstream.Pick(result, labels)
	if series == nil || len(series.Values) == 0 {
		return "", nil
	}
	samples := series.Values
	if len(samples) > sparkPoints+1 {
		samples = samples[len(samples)-sparkPoints-1:]
	}
	values := make([]spark.Value, len(samples))
	for i, s := range samples {
		values[i] = spark.Value{V: s.V, Known: !math.IsNaN(s.V)}
	}
	known := spark.Known(values)
	if len(known) == 0 || zeroOne(known) {
		return "", nil
	}
	var limit *float64
	op := ""
	if changed == "" {
		if o, n, ok := promql.Threshold(raw); ok {
			op, limit = o, &n
		}
	}
	head := "📈 `" + spark.Line(values, limit) + "` last " + spark.WindowText(window)
	if changed != "" {
		head += ", the value before `" + changed + "()`"
	}
	lo, hi := known[0], known[0]
	for _, v := range known {
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	now := known[len(known)-1]
	parts := []string{head, "min " + format.Number(lo), "max " + format.Number(hi), "now " + format.Number(now)}
	if limit != nil {
		parts = append(parts, spark.Against(op, *limit, now))
	}
	if t := spark.Trend(known); t != "" {
		parts = append(parts, t)
	}
	if week, ok := e.weekAgo(ctx, expr, labels); ok {
		parts = append(parts, "a week ago "+format.Number(week))
	}
	return strings.Join(parts, " · "), nil
}

func zeroOne(vs []float64) bool {
	for _, v := range vs {
		if v != 0 && v != 1 {
			return false
		}
	}
	return true
}

func (e *Engine) weekAgo(ctx context.Context, expr string, labels map[string]string) (float64, bool) {
	result, err := e.Prom.Query(ctx, expr, e.Now().Unix()-7*86400)
	if err != nil {
		e.Log.Warn("week-ago lookup failed", "error", err)
		return 0, false
	}
	s := upstream.Pick(result, labels)
	if s == nil || len(s.Values) == 0 || math.IsNaN(s.Values[0].V) {
		return 0, false
	}
	return s.Values[0].V, true
}

// firingQuery counts the group's firing alerts from ALERTS, matched on the group's labels and, for one
// alert, its object's. "" without an alert name.
func (e *Engine) firingQuery(g *alert.Group) string {
	matchers := map[string]string{}
	for k, v := range g.GroupLabels {
		matchers[k] = v
	}
	if active := g.Active(); len(active) == 1 {
		for k, v := range e.Config.ObjectReader().Labels(active[0]) {
			matchers[k] = v
		}
	}
	if matchers["alertname"] == "" {
		return ""
	}
	keys := make([]string, 0, len(matchers))
	for k := range matchers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	sel := make([]string, len(keys))
	for i, k := range keys {
		sel[i] = k + "=" + upstream.LogsQLString(matchers[k])
	}
	return "count(ALERTS{" + strings.Join(sel, ",") + `,alertstate="firing"})`
}

func (e *Engine) firingTimeline(ctx context.Context, g *alert.Group, raw string) (string, error) {
	query := e.firingQuery(g)
	if query == "" {
		return "", nil
	}
	window := max(timelineBase, promql.Window(raw, sparkHours*3600))
	result, step, err := e.queryRange(ctx, query, window, sparkPoints)
	if err != nil || len(result) == 0 {
		return "", err
	}
	now := e.now()
	values := spark.OnGrid(result[0].Values, now, step, sparkPoints)
	peak := 0.0
	for _, v := range values {
		if v.Known && v.V > peak {
			peak = v.V
		}
	}
	if peak == 0 {
		return "", nil
	}
	since := 0.0
	for _, a := range g.Active() {
		s := startOr(a.Start(), now)
		if since == 0 || s < since {
			since = s
		}
	}
	when := format.Date(since, format.Long, format.LongFallback)
	if now-since < 86400 {
		when = format.Date(since, format.Time, format.TimeFallback)
	}
	firing := "firing since " + when + " (" + format.Duration(now-since) + ")"
	if peak > 1 {
		zeroed := make([]spark.Value, len(values))
		for i, v := range values {
			zeroed[i] = spark.Value{V: v.V, Known: true}
		}
		zero := 0.0
		last := values[len(values)-1].V
		return fmt.Sprintf("🚦 `%s` alerts firing at once, last %s · peak %s · now %s · %s", spark.Line(zeroed, &zero),
			spark.WindowText(window), format.Number(peak), format.Number(last), firing), nil
	}
	again := ""
	if n := spark.Runs(values); n > 1 {
		again = fmt.Sprintf(" · %d firings in this window", n)
	}
	return "🚦 `" + spark.Timeline(values) + "` last " + spark.WindowText(window) + " · " + firing + again, nil
}

// chart uploads a PNG into the thread: what the rule measures over the sparkline's window with its
// threshold, and when it fired. A rule with nothing to draw gets how many of its alerts fired.
func (e *Engine) chart(ctx context.Context, channel, ts string, g *alert.Group) (string, error) {
	if !e.Config.Enrich.Charts || e.Prom == nil {
		return "disabled", nil
	}
	raw, err := e.ruleExpression(ctx, g)
	if err != nil {
		return "", err
	}
	expr, changed := promql.Unwrap(promql.PlainSeries(raw))
	query := e.firingQuery(g)
	window := promql.Window(raw, sparkHours*3600)
	if expr == "" {
		window = max(timelineBase, window)
	}
	now := e.now()
	var values []spark.Value
	if expr != "" {
		result, step, err := e.queryRange(ctx, expr, window, chart.Points)
		if err != nil {
			return "", err
		}
		var samples []spark.Sample
		if s := upstream.Pick(result, g.First().Labels); s != nil {
			samples = s.Values
		}
		values = spark.OnGrid(samples, now, step, chart.Points)
		if zeroOne(spark.Known(values)) {
			values = nil
		}
	}
	counts := make([]spark.Value, chart.Points)
	if query != "" {
		result, step, err := e.queryRange(ctx, query, window, chart.Points)
		if err != nil {
			return "", err
		}
		var samples []spark.Sample
		if len(result) > 0 {
			samples = result[0].Values
		}
		counts = spark.OnGrid(samples, now, step, chart.Points)
	}
	fired := make([]bool, len(counts))
	anyFired := false
	for i, c := range counts {
		fired[i] = c.Known && c.V != 0
		anyFired = anyFired || fired[i]
	}
	var limit *float64
	if changed == "" {
		if _, n, ok := promql.Threshold(raw); ok {
			limit = &n
		}
	}
	what := "the rule's value"
	if changed != "" {
		what = "the value before " + changed + "()"
	}
	if len(values) == 0 {
		last := -1
		for i, c := range counts {
			if c.Known {
				last = i
			}
		}
		values = make([]spark.Value, len(counts))
		for i, c := range counts {
			if last >= 0 && i <= last {
				values[i] = spark.Value{V: c.V, Known: true}
			}
		}
		limit, what = nil, "alerts of this rule firing"
	}
	if len(spark.Known(values)) == 0 || !anyFired && expr == "" {
		return "none", nil
	}
	png, err := chart.PNG(values, fired, limit, window)
	if err != nil {
		return "", err
	}
	name := g.Label("alertname")
	if name == "" {
		name = "alert"
	}
	comment := "📊 " + format.Escape(name) + ": " + what + " over the last " + spark.WindowText(window)
	if limit != nil {
		comment += ", threshold dashed"
	}
	comment += ", firing shaded."
	if err := e.Slack.Upload(ctx, channel, ts, png, name+".png", comment); err != nil {
		return "", err
	}
	return "ok", nil
}

// ruleHistory says how this rule behaved in this channel over the last week: how often, how long it
// lasted, noise votes and who picked it up last.
func (e *Engine) ruleHistory(ctx context.Context, channel, ts string, g *alert.Group) string {
	name := g.Label("alertname")
	var past []*state.Incident
	if name != "" {
		for _, m := range e.history(ctx, channel, e.now()-7*86400) {
			if st, ok := e.incident(&m); ok && m.TS != ts && st.Alertname == name {
				past = append(past, st)
			}
		}
	}
	if len(past) == 0 {
		e.Metrics.Enriched("history", "none")
		return ""
	}
	firings := 0
	var took []float64
	votes := 0
	for _, st := range past {
		firings += st.Firings()
		if st.Resolved() && st.ResolvedAt != 0 {
			took = append(took, st.ResolvedAt-st.Shown(st.ResolvedAt))
		}
		votes += int(st.NoiseVotes)
	}
	parts := []string{fmt.Sprintf("🕘 Fired %d time%s before in 7 days", firings, format.Plural(firings))}
	if len(took) > 0 {
		sort.Float64s(took)
		parts = append(parts, "usually over in "+format.Duration(took[len(took)/2]))
	}
	if votes > 0 {
		parts = append(parts, fmt.Sprintf("🙈 %d noise vote%s", votes, format.Plural(votes)))
	}
	sort.SliceStable(past, func(i, j int) bool { return past[i].PostedAt > past[j].PostedAt })
	picked := ""
	for _, st := range past {
		if st.AckedBy != "" {
			picked = st.AckedBy
			break
		}
	}
	if picked != "" {
		parts = append(parts, "last picked up by <@"+picked+">")
	} else {
		parts = append(parts, "nobody pressed 👀 on any of them")
	}
	e.Metrics.Enriched("history", "ok")
	return strings.Join(parts, " · ")
}

// related names other alerts firing now in the same namespace or on the same node: three incidents
// with one cause read as one.
func (e *Engine) related(ctx context.Context, g *alert.Group) (string, error) {
	if !e.Config.Enrich.Related {
		e.Metrics.Enriched("related", "disabled")
		return "", nil
	}
	active := g.Active()
	type scope struct{ key, where, value string }
	var scopes []scope
	for _, s := range []struct{ key, where string }{{"namespace", "in"}, {"node", "on"}} {
		values := map[string]bool{}
		for _, a := range active {
			values[a.Labels[s.key]] = true
		}
		if len(values) == 1 && !values[""] {
			for v := range values {
				scopes = append(scopes, scope{s.key, s.where, v})
			}
		}
	}
	if len(scopes) == 0 {
		e.Metrics.Enriched("related", "skipped")
		return "", nil
	}
	mine := map[string]bool{}
	for _, a := range active {
		mine[a.Fingerprint] = true
	}
	firing, err := e.AM.Firing(ctx)
	if err != nil {
		return "", err
	}
	var parts []string
	for _, s := range scopes {
		names := map[string]int{}
		for _, a := range firing {
			if !mine[a.Fingerprint] && a.Labels[s.key] == s.value && !e.Config.Quiet(a.Labels["alertname"]) {
				n := a.Labels["alertname"]
				if n == "" {
					n = "?"
				}
				names[n]++
			}
		}
		type count struct {
			name string
			n    int
		}
		var top []count
		for n, c := range names {
			top = append(top, count{n, c})
		}
		sort.Slice(top, func(i, j int) bool {
			if top[i].n != top[j].n {
				return top[i].n > top[j].n
			}
			return top[i].name < top[j].name
		})
		if len(top) == 0 {
			continue
		}
		var shown []string
		for i, c := range top {
			if i == 4 {
				break
			}
			if c.n > 1 {
				shown = append(shown, fmt.Sprintf("%s ×%d", format.Escape(c.name), c.n))
			} else {
				shown = append(shown, format.Escape(c.name))
			}
		}
		more := ""
		if len(top) > 4 {
			more = fmt.Sprintf(" and %d more", len(top)-4)
		}
		parts = append(parts, s.where+" `"+format.Escape(s.value)+"`: "+strings.Join(shown, ", ")+more)
	}
	if len(parts) == 0 {
		e.Metrics.Enriched("related", "none")
		return "", nil
	}
	e.Metrics.Enriched("related", "ok")
	return "🧩 Also firing " + strings.Join(parts, " · "), nil
}

// lastLogs is the last lines of the one pod a group is about, as a thread reply; "" otherwise.
func (e *Engine) lastLogs(ctx context.Context, g *alert.Group) (string, error) {
	if e.Logs == nil {
		e.Metrics.Enriched("logs", "disabled")
		return "", nil
	}
	cfg := e.Config.Enrich.Logs
	objects := e.Config.ObjectReader()
	active := g.Active()
	type pod struct{ ns, name, container string }
	pods := map[pod]bool{}
	for _, a := range active {
		if !objects.Is(a.Labels, "pod") {
			e.Metrics.Enriched("logs", "skipped")
			return "", nil
		}
		pods[pod{a.Labels["namespace"], a.Labels["pod"], a.Labels["container"]}] = true
	}
	if len(pods) != 1 {
		e.Metrics.Enriched("logs", "skipped")
		return "", nil
	}
	var p pod
	for k := range pods {
		p = k
	}
	stream := cfg.Namespace + "=" + upstream.LogsQLString(p.ns) + "," + cfg.Pod + "=" + upstream.LogsQLString(p.name)
	if p.container != "" {
		stream += "," + cfg.Container + "=" + upstream.LogsQLString(p.container)
	}
	where := p.ns + "/" + p.name
	if p.container != "" {
		where += " (" + p.container + ")"
	}
	type object struct{ ns, name string }
	objs := map[object]bool{}
	for _, a := range active {
		objs[object{a.Labels["exported_namespace"], a.Labels["name"]}] = true
	}
	var rows []upstream.Row
	about := ""
	if len(objs) == 1 {
		var o object
		for k := range objs {
			o = k
		}
		if o.ns != "" && o.name != "" {
			var err error
			rows, err = e.queryLogs(ctx, "_stream:{"+stream+"} namespace:="+upstream.LogsQLString(o.ns)+" name:="+upstream.LogsQLString(o.name))
			if err != nil {
				return "", err
			}
			if len(rows) > 0 {
				about = " about " + o.ns + "/" + o.name
			}
		}
	}
	if len(rows) == 0 {
		var err error
		if rows, err = e.queryLogs(ctx, "_stream:{"+stream+"}"); err != nil {
			return "", err
		}
	}
	if len(rows) == 0 {
		e.Metrics.Enriched("logs", "none")
		return "", nil
	}
	e.Metrics.Enriched("logs", "ok")
	lines := make([]string, len(rows))
	for i, r := range rows {
		lines[i] = logLine(r)
	}
	return fmt.Sprintf("📜 Last %d log lines of %s%s in the hour before this post, oldest first, obvious secrets redacted:\n",
		len(rows), format.Escape(where), format.Escape(about)) + logBlock(lines), nil
}

func (e *Engine) queryLogs(ctx context.Context, selector string) ([]upstream.Row, error) {
	return e.Logs.Query(ctx, fmt.Sprintf("%s _time:1h | sort by (_time) desc | limit %d | fields _time, _msg, error, err", selector, logLines))
}

func logLine(r upstream.Row) string {
	errText := r.Error
	if errText == "" {
		errText = r.Err
	}
	if errText != "" && !strings.Contains(r.Msg, errText) {
		return r.Msg + " · " + errText
	}
	return r.Msg
}

func logBlock(lines []string) string {
	out := make([]string, len(lines))
	for i, l := range lines {
		l = redact.Line(strings.TrimRight(l, "\n"))
		if len([]rune(l)) > logLineMax {
			l = format.Cut(l, logLineMax) + " …"
		}
		out[i] = strings.ReplaceAll(format.Escape(l), "```", "ˋˋˋ")
	}
	text := strings.Join(out, "\n")
	for len([]rune(text)) > 2800 && strings.Contains(text, "\n") {
		text = text[strings.Index(text, "\n")+1:]
	}
	return "```\n" + text + "\n```"
}

// fluxLists are the Flux objects whose status.history says what was applied and when.
var fluxLists = []struct{ kind, path string }{
	{"Kustomization", "/apis/kustomize.toolkit.fluxcd.io/v1/kustomizations"},
	{"HelmRelease", "/apis/helm.toolkit.fluxcd.io/v2/helmreleases"},
}

type fluxCache struct {
	at    float64
	items map[string][]fluxObject
}

type fluxObject struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		TargetNamespace string `json:"targetNamespace"`
		Path            string `json:"path"`
		SourceRef       struct {
			Name string `json:"name"`
		} `json:"sourceRef"`
	} `json:"spec"`
	Status struct {
		History []struct {
			FirstReconciled      string `json:"firstReconciled"`
			TotalReconciliations int    `json:"totalReconciliations"`
			Metadata             struct {
				Revision string `json:"revision"`
			} `json:"metadata"`
			LastDeployed string `json:"lastDeployed"`
			ChartName    string `json:"chartName"`
			ChartVersion string `json:"chartVersion"`
		} `json:"history"`
	} `json:"status"`
}

// Change is one change Flux applied into a namespace.
type Change struct {
	Kind, Name, What, SHA, Path string
	At                          float64
}

func (e *Engine) fluxItems(ctx context.Context) (map[string][]fluxObject, error) {
	now := e.now()
	e.mu.Lock()
	cache := e.flux
	e.mu.Unlock()
	if cache.items != nil && now-cache.at < 60 {
		return cache.items, nil
	}
	items := map[string][]fluxObject{}
	for _, l := range fluxLists {
		var list struct {
			Items []fluxObject `json:"items"`
		}
		if err := e.Kube.Get(ctx, l.path, &list); err != nil {
			return nil, err
		}
		items[l.kind] = list.Items
	}
	e.mu.Lock()
	e.flux = fluxCache{at: now, items: items}
	e.mu.Unlock()
	return items, nil
}

// FluxChanges is every change Flux applied into a namespace. An entry's revision is the latest commit
// that reconciled that content, not the one that brought it: every later push overwrites it. So the
// commit is named only for an entry reconciled once; otherwise the change is the Kustomization's path.
func FluxChanges(items map[string][]fluxObject, namespace, ownSource string) []Change {
	var out []Change
	target := func(o fluxObject) string {
		if o.Spec.TargetNamespace != "" {
			return o.Spec.TargetNamespace
		}
		return o.Metadata.Namespace
	}
	for _, ks := range items["Kustomization"] {
		if target(ks) != namespace {
			continue
		}
		own := ks.Spec.SourceRef.Name == ownSource
		path := strings.Trim(strings.TrimPrefix(ks.Spec.Path, "./"), "/")
		for _, h := range ks.Status.History {
			rev := h.Metadata.Revision
			sha := ""
			if i := strings.LastIndex(rev, "sha1:"); i >= 0 && h.TotalReconciliations <= 1 {
				sha = rev[i+5:]
			}
			c := Change{Kind: "Kustomization", Name: ks.Metadata.Name, At: alert.ParseTime(h.FirstReconciled)}
			switch {
			case sha != "":
				c.What = sha[:min(8, len(sha))]
			case own && path != "":
				c.What = path
			default:
				c.What = rev
				if i := strings.LastIndex(rev, "@"); i > 0 {
					c.What = rev[:i]
				}
			}
			if own {
				c.SHA, c.Path = sha, path
			}
			out = append(out, c)
		}
	}
	for _, hr := range items["HelmRelease"] {
		if target(hr) != namespace {
			continue
		}
		for _, h := range hr.Status.History {
			out = append(out, Change{Kind: "HelmRelease", Name: hr.Metadata.Name, At: alert.ParseTime(h.LastDeployed),
				What: strings.TrimSpace(h.ChartName + " " + h.ChartVersion)})
		}
	}
	return out
}

// ChangeLine is "🔧 Kustomization `app` applied `1a2b3c4d` 12 min before this alert".
func ChangeLine(c Change, start float64, commitURL, pathURL string) string {
	before := format.Duration(math.Max(60, start-c.At))
	what := "`" + format.Escape(c.What) + "`"
	verb := "applied"
	if c.Kind == "HelmRelease" {
		verb = "upgraded to"
	}
	switch {
	case c.SHA != "" && commitURL != "":
		what = "<" + commitURL + c.SHA + "|" + format.Escape(c.What) + ">"
	case c.Path != "":
		verb = "applied a change in"
		if pathURL != "" {
			what = "<" + pathURL + c.Path + "|" + format.Escape(c.Path) + ">"
		}
	}
	return "🔧 " + c.Kind + " `" + format.Escape(c.Name) + "` " + verb + " " + what + " " + before + " before this alert"
}

func (e *Engine) changeBefore(ctx context.Context, g *alert.Group) (string, error) {
	if e.Kube == nil {
		e.Metrics.Enriched("change", "disabled")
		return "", nil
	}
	active := g.Active()
	namespaces := map[string]bool{}
	for _, a := range active {
		namespaces[a.Labels["namespace"]] = true
	}
	if len(namespaces) != 1 || namespaces[""] {
		e.Metrics.Enriched("change", "skipped")
		return "", nil
	}
	var ns string
	for n := range namespaces {
		ns = n
	}
	now := e.now()
	start := 0.0
	for _, a := range active {
		s := startOr(a.Start(), now)
		if start == 0 || s < start {
			start = s
		}
	}
	items, err := e.fluxItems(ctx)
	if err != nil {
		var se *upstream.StatusError
		if errors.As(err, &se) && (se.Code == 401 || se.Code == 403) {
			e.Metrics.Enriched("change", "forbidden")
			e.Log.Warn("flux read refused; no change line", "code", se.Code)
			return "", nil
		}
		return "", err
	}
	cfg := e.Config.Enrich.Flux
	var best *Change
	for _, c := range FluxChanges(items, ns, cfg.OwnSource) {
		if c.At >= start-cfg.Window.Seconds() && c.At <= start && (best == nil || c.At > best.At) {
			best = &c
		}
	}
	if best == nil {
		e.Metrics.Enriched("change", "none")
		return "", nil
	}
	e.Metrics.Enriched("change", "ok")
	return ChangeLine(*best, start, cfg.CommitURL, cfg.PathURL), nil
}
