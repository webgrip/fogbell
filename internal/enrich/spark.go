package enrich

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/webgrip/fogbell/internal/alert"
	"github.com/webgrip/fogbell/internal/format"
	"github.com/webgrip/fogbell/internal/promql"
	"github.com/webgrip/fogbell/internal/spark"
	"github.com/webgrip/fogbell/internal/upstream"
)

const (
	sparkBase    = 3 * 3600
	sparkPoints  = 30
	timelineBase = 12 * 3600
)

// Series draws what a rule measures from a Prometheus-compatible query API.
type Series struct {
	Prom    *upstream.Prometheus
	Objects *alert.Objects
	Now     func() time.Time
}

func (q *Series) now() float64 { return float64(q.Now().UnixNano()) / 1e9 }

func (q *Series) queryRange(ctx context.Context, expr string, window int64, points int) ([]upstream.Series, int64, error) {
	step := max(60, window/int64(points))
	result, err := q.Prom.QueryRange(ctx, expr, q.Now().Unix(), window, step)
	return result, step, err
}

// firingQuery counts the group's firing alerts from ALERTS, matched on the group's labels and, for one
// alert, its object's. "" without an alert name.
func (q *Series) firingQuery(g *alert.Group) string {
	matchers := map[string]string{}
	for k, v := range g.GroupLabels {
		matchers[k] = v
	}
	if active := g.Active(); len(active) == 1 {
		for k, v := range q.Objects.Labels(active[0]) {
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

// Spark is the sparkline line: the series a rule compares, or for a change over a window the series
// that changed, with its range, threshold, trend and a week ago. A rule with nothing to draw gets its
// firing timeline from ALERTS.
type Spark struct{ *Series }

// Kind implements Enricher.
func (*Spark) Kind() string { return "spark" }

// Slot implements Line.
func (*Spark) Slot() Slot { return SlotSpark }

// Refresh implements Refreshable.
func (*Spark) Refresh() bool { return true }

// Enrich implements Enricher.
func (s *Spark) Enrich(ctx context.Context, sub *Subject) (Result, error) {
	raw, err := sub.Expression(ctx)
	if err != nil {
		return Result{}, err
	}
	if expr, changed := promql.Unwrap(promql.PlainSeries(raw)); expr != "" {
		text, err := s.series(ctx, expr, raw, changed, sub.Group.First().Labels)
		if err != nil || text != "" {
			return Result{Outcome: OK, Line: text}, err
		}
	}
	text, err := s.timeline(ctx, sub.Group, raw)
	if err != nil {
		return Result{}, err
	}
	if text == "" {
		return outcome(None)
	}
	return line(Timeline, text)
}

func (s *Spark) series(ctx context.Context, expr, raw, changed string, labels map[string]string) (string, error) {
	window := promql.Window(raw, sparkBase)
	result, _, err := s.queryRange(ctx, expr, window, sparkPoints)
	if err != nil {
		return "", err
	}
	picked := upstream.Pick(result, labels)
	if picked == nil || len(picked.Values) == 0 {
		return "", nil
	}
	samples := picked.Values
	if len(samples) > sparkPoints+1 {
		samples = samples[len(samples)-sparkPoints-1:]
	}
	values := make([]spark.Value, len(samples))
	for i, v := range samples {
		values[i] = spark.Value{V: v.V, Known: !math.IsNaN(v.V)}
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
	lo, hi := minMax(known)
	now := known[len(known)-1]
	parts := []string{head, "min " + format.Number(lo), "max " + format.Number(hi), "now " + format.Number(now)}
	if limit != nil {
		parts = append(parts, spark.Against(op, *limit, now))
	}
	if t := spark.Trend(known); t != "" {
		parts = append(parts, t)
	}
	if week, ok := s.weekAgo(ctx, expr, labels); ok {
		parts = append(parts, "a week ago "+format.Number(week))
	}
	return strings.Join(parts, " · "), nil
}

func (s *Spark) weekAgo(ctx context.Context, expr string, labels map[string]string) (float64, bool) {
	result, err := s.Prom.Query(ctx, expr, s.Now().Unix()-7*86400)
	if err != nil {
		return 0, false
	}
	picked := upstream.Pick(result, labels)
	if picked == nil || len(picked.Values) == 0 || math.IsNaN(picked.Values[0].V) {
		return 0, false
	}
	return picked.Values[0].V, true
}

func (s *Spark) timeline(ctx context.Context, g *alert.Group, raw string) (string, error) {
	query := s.firingQuery(g)
	if query == "" {
		return "", nil
	}
	window := max(timelineBase, promql.Window(raw, sparkBase))
	result, step, err := s.queryRange(ctx, query, window, sparkPoints)
	if err != nil || len(result) == 0 {
		return "", err
	}
	now := s.now()
	values := spark.OnGrid(result[0].Values, now, step, sparkPoints)
	peak := 0.0
	for _, v := range values {
		if v.Known {
			peak = math.Max(peak, v.V)
		}
	}
	if peak == 0 {
		return "", nil
	}
	since := earliestStart(g, now)
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
		return fmt.Sprintf("🚦 `%s` alerts firing at once, last %s · peak %s · now %s · %s", spark.Line(zeroed, &zero),
			spark.WindowText(window), format.Number(peak), format.Number(values[len(values)-1].V), firing), nil
	}
	again := ""
	if n := spark.Runs(values); n > 1 {
		again = fmt.Sprintf(" · %d firings in this window", n)
	}
	return "🚦 `" + spark.Timeline(values) + "` last " + spark.WindowText(window) + " · " + firing + again, nil
}

func earliestStart(g *alert.Group, now float64) float64 {
	since := 0.0
	for _, a := range g.Active() {
		s := a.Start()
		if s == 0 {
			s = now
		}
		if since == 0 || s < since {
			since = s
		}
	}
	if since == 0 {
		return now
	}
	return since
}

func zeroOne(vs []float64) bool {
	for _, v := range vs {
		if v != 0 && v != 1 {
			return false
		}
	}
	return true
}

func minMax(vs []float64) (float64, float64) {
	lo, hi := vs[0], vs[0]
	for _, v := range vs {
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	return lo, hi
}
