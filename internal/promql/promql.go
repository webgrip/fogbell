// Package promql reads an alert rule's expression with the MetricsQL parser, a superset of PromQL, to
// find what is worth drawing: the series a rule compares with a number, and the threshold.
package promql

import (
	"strings"

	"github.com/VictoriaMetrics/metricsql"
)

func parse(expr string) metricsql.Expr {
	var kept []string
	for _, line := range strings.Split(expr, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			kept = append(kept, line)
		}
	}
	text := strings.TrimSpace(strings.Join(kept, "\n"))
	if text == "" {
		return nil
	}
	e, err := metricsql.Parse(text)
	if err != nil {
		return nil
	}
	return e
}

func isSetOp(op string) bool {
	switch strings.ToLower(op) {
	case "and", "or", "unless":
		return true
	}
	return false
}

func comparison(e metricsql.Expr) (*metricsql.BinaryOpExpr, bool) {
	b, ok := e.(*metricsql.BinaryOpExpr)
	if !ok || !metricsql.IsBinaryOpCmp(b.Op) {
		return nil, false
	}
	return b, true
}

func usesAbsent(e metricsql.Expr) bool {
	found := false
	metricsql.VisitAll(e, func(x metricsql.Expr) {
		if f, ok := x.(*metricsql.FuncExpr); ok && (f.Name == "absent" || f.Name == "absent_over_time") {
			found = true
		}
	})
	return found
}

// PlainSeries is the part of a rule's expression worth drawing: the left side of one comparison with a
// number ("x > 15" draws x), or the whole expression when it compares nothing. "" when it is not one
// series per object: absent(), a set operator, two comparisons, or a comparison of two vectors.
func PlainSeries(expr string) string {
	e := parse(expr)
	if e == nil || usesAbsent(e) {
		return ""
	}
	if b, ok := e.(*metricsql.BinaryOpExpr); ok && isSetOp(b.Op) {
		return ""
	}
	b, ok := comparison(e)
	if !ok {
		if hasTopComparison(e) {
			return ""
		}
		return string(e.AppendString(nil))
	}
	if _, isNumber := b.Right.(*metricsql.NumberExpr); !isNumber {
		return ""
	}
	if hasTopComparison(b.Left) {
		return ""
	}
	return string(b.Left.AppendString(nil))
}

func hasTopComparison(e metricsql.Expr) bool {
	b, ok := e.(*metricsql.BinaryOpExpr)
	if !ok {
		return false
	}
	if metricsql.IsBinaryOpCmp(b.Op) || isSetOp(b.Op) {
		return true
	}
	return hasTopComparison(b.Left) || hasTopComparison(b.Right)
}

// Threshold is the operator and number of an expression that compares one series with a number.
func Threshold(expr string) (op string, limit float64, ok bool) {
	e := parse(expr)
	if e == nil {
		return "", 0, false
	}
	b, isCmp := comparison(e)
	if !isCmp || hasTopComparison(b.Left) {
		return "", 0, false
	}
	n, isNumber := b.Right.(*metricsql.NumberExpr)
	if !isNumber {
		return "", 0, false
	}
	return b.Op, n.N, true
}

var changeFuncs = map[string]bool{"delta": true, "delta_prometheus": true, "idelta": true, "changes": true, "deriv": true}

// Unwrap is the series a change function reads: "delta((x)[1d:30m])" is ("x", "delta"). A change of
// something drawn says what happened; the change itself is a step. (expr, "") when it is not one.
func Unwrap(expr string) (series, fn string) {
	e := parse(expr)
	f, ok := e.(*metricsql.FuncExpr)
	if !ok || !changeFuncs[f.Name] || len(f.Args) != 1 {
		return expr, ""
	}
	r, ok := f.Args[0].(*metricsql.RollupExpr)
	if !ok || r.Window == nil {
		return expr, ""
	}
	return string(r.Expr.AppendString(nil)), f.Name
}

// LongestRange is the longest range or subquery window the expression reads, in seconds.
func LongestRange(expr string) int64 {
	e := parse(expr)
	if e == nil {
		return 0
	}
	var longest int64
	metricsql.VisitAll(e, func(x metricsql.Expr) {
		if r, ok := x.(*metricsql.RollupExpr); ok && r.Window != nil {
			if ms := r.Window.Duration(60_000); ms/1000 > longest {
				longest = ms / 1000
			}
		}
	})
	return longest
}

// Window is how many seconds to draw: base, or four times the longest range the expression reads, so a
// rule over a day is not judged on three hours of it. A week at most.
func Window(expr string, base int64) int64 {
	return min(7*86400, max(base, 4*LongestRange(expr)))
}
