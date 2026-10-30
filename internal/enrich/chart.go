package enrich

import (
	"context"

	"github.com/webgrip/fogbell/internal/chart"
	"github.com/webgrip/fogbell/internal/format"
	"github.com/webgrip/fogbell/internal/promql"
	"github.com/webgrip/fogbell/internal/spark"
	"github.com/webgrip/fogbell/internal/upstream"
)

// Chart is a PNG for the thread: what the rule measures over the sparkline's window with its
// threshold, and when it fired. A rule with nothing to draw gets how many of its alerts fired.
type Chart struct{ *Series }

// Kind implements Enricher.
func (*Chart) Kind() string { return "chart" }

// Enrich implements Enricher.
func (c *Chart) Enrich(ctx context.Context, sub *Subject) (Result, error) {
	raw, err := sub.Expression(ctx)
	if err != nil {
		return Result{}, err
	}
	g := sub.Group
	expr, changed := promql.Unwrap(promql.PlainSeries(raw))
	window := promql.Window(raw, sparkBase)
	if expr == "" {
		window = max(timelineBase, window)
	}
	now := c.now()
	var values []spark.Value
	if expr != "" {
		result, step, err := c.queryRange(ctx, expr, window, chart.Points)
		if err != nil {
			return Result{}, err
		}
		var samples []spark.Sample
		if picked := upstream.Pick(result, g.First().Labels); picked != nil {
			samples = picked.Values
		}
		values = spark.OnGrid(samples, now, step, chart.Points)
		if zeroOne(spark.Known(values)) {
			values = nil
		}
	}
	counts := make([]spark.Value, chart.Points)
	if query := c.firingQuery(g); query != "" {
		result, step, err := c.queryRange(ctx, query, window, chart.Points)
		if err != nil {
			return Result{}, err
		}
		var samples []spark.Sample
		if len(result) > 0 {
			samples = result[0].Values
		}
		counts = spark.OnGrid(samples, now, step, chart.Points)
	}
	fired := make([]bool, len(counts))
	anyFired := false
	for i, v := range counts {
		fired[i] = v.Known && v.V != 0
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
		values, limit, what = untilLast(counts), nil, "alerts of this rule firing"
	}
	if len(spark.Known(values)) == 0 || !anyFired && expr == "" {
		return outcome(None)
	}
	png, err := chart.PNG(values, fired, limit, window)
	if err != nil {
		return Result{}, err
	}
	name := g.Label("alertname")
	if name == "" {
		name = "alert"
	}
	comment := "📊 " + format.Escape(name) + ": " + what + " over the last " + spark.WindowText(window)
	if limit != nil {
		comment += ", threshold dashed"
	}
	return Result{Outcome: OK, File: &File{Name: name + ".png", Comment: comment + ", firing shaded.", Data: png}}, nil
}

// untilLast is the counts as values up to the last sample: no sample is no firing, except after the
// last one, where the latest evaluation may not be stored yet.
func untilLast(counts []spark.Value) []spark.Value {
	last := -1
	for i, v := range counts {
		if v.Known {
			last = i
		}
	}
	out := make([]spark.Value, len(counts))
	for i, v := range counts {
		if i <= last {
			out[i] = spark.Value{V: v.V, Known: true}
		}
	}
	return out
}
