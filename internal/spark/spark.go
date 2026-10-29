// Package spark draws a series as text: a unicode sparkline, a firing timeline, its trend and how far
// it is past its threshold.
package spark

import (
	"fmt"
	"math"
	"strings"

	"github.com/webgrip/fogbell/internal/format"
)

const bars = "▁▂▃▄▅▆▇█"

// Value is a point of a series; Known is false for a moment without a sample.
type Value struct {
	V     float64
	Known bool
}

// Of turns numbers into known values.
func Of(vs ...float64) []Value {
	out := make([]Value, len(vs))
	for i, v := range vs {
		out[i] = Value{V: v, Known: true}
	}
	return out
}

// Known is the values that have a sample.
func Known(values []Value) []float64 {
	var out []float64
	for _, v := range values {
		if v.Known {
			out = append(out, v.V)
		}
	}
	return out
}

// Line draws bars scaled to the values and, when given, the threshold, so a value just past it does
// not fill the height.
func Line(values []Value, limit *float64) string {
	known := Known(values)
	if limit != nil {
		known = append(known, *limit)
	}
	if len(known) == 0 {
		return ""
	}
	lo, hi := minMax(known)
	span := hi - lo
	runes := []rune(bars)
	var b strings.Builder
	for _, v := range values {
		switch {
		case !v.Known:
			b.WriteRune(' ')
		case span == 0:
			b.WriteRune(runes[3])
		default:
			b.WriteRune(runes[min(7, int((v.V-lo)/span*8))])
		}
	}
	return b.String()
}

// Timeline draws firing (█) or not (▁) per point.
func Timeline(values []Value) string {
	var b strings.Builder
	for _, v := range values {
		if v.Known && v.V != 0 {
			b.WriteString("█")
		} else {
			b.WriteString("▁")
		}
	}
	return b.String()
}

// Runs is how many separate firings the points show.
func Runs(values []Value) int {
	n := 0
	for i, v := range values {
		on := v.Known && v.V != 0
		prev := i > 0 && values[i-1].Known && values[i-1].V != 0
		if on && !prev {
			n++
		}
	}
	return n
}

// Trend is rising, falling or steady over the last third of the values, judged against their range.
func Trend(values []float64) string {
	if len(values) < 4 {
		return ""
	}
	lo, hi := minMax(values)
	span := hi - lo
	change := values[len(values)-1] - values[len(values)-max(2, len(values)/3)]
	if span == 0 || math.Abs(change) < 0.1*span {
		return "→ steady"
	}
	if change > 0 {
		return "↗ rising"
	}
	return "↘ falling"
}

// Against says how far the value is past the threshold: "threshold > 15 (1.5×)".
func Against(op string, limit, now float64) string {
	text := "threshold " + op + " " + format.Number(limit)
	switch {
	case (op == ">" || op == ">=") && limit > 0 && now/limit >= 1.1:
		text += fmt.Sprintf(" (%.1f×)", now/limit)
	case (op == "<" || op == "<=") && limit > 0 && now/limit >= 0 && now/limit <= 0.9:
		text += fmt.Sprintf(" (%.0f%% of it)", now/limit*100)
	}
	return text
}

// WindowText renders a window as "3 h" or, from two days, "4 d".
func WindowText(seconds int64) string {
	if seconds >= 2*86400 {
		return fmt.Sprintf("%d d", seconds/86400)
	}
	return fmt.Sprintf("%d h", seconds/3600)
}

// OnGrid places [t, v] samples on points slots a step apart, ending now. The query API aligns
// timestamps to the step and leaves out moments without a value, so each sample is placed by its
// distance from now.
func OnGrid(samples []Sample, now float64, step int64, points int) []Value {
	values := make([]Value, points)
	for _, s := range samples {
		back := int(math.Round((now - s.T) / float64(step)))
		if back >= 0 && back < points && !math.IsNaN(s.V) {
			values[points-1-back] = Value{V: s.V, Known: true}
		}
	}
	return values
}

// Sample is a timestamped value.
type Sample struct {
	T float64
	V float64
}

func minMax(vs []float64) (float64, float64) {
	lo, hi := vs[0], vs[0]
	for _, v := range vs[1:] {
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	return lo, hi
}
