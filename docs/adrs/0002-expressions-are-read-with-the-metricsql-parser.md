---
status: "accepted"
date: "2026-10-29"
review-by: "2027-04-29"
---

# A rule's expression is read with the MetricsQL parser, not by hand

## Context and Problem Statement

The sparkline draws what an alert measures: for `x > 15` it draws `x` against a line at 15; for a
change such as `delta((q)[1d:30m]) > 0` it draws `q`; for `absent()`, a set operator or a comparison
of two vectors it draws the firing timeline instead. That needs the expression's structure. The relay
fogbell was ported from scanned the text with a hand-written tokenizer (brackets, quotes, operators),
which is where it went wrong without anyone noticing: a comparison inside a label value, `bool`, a
subquery.

## Decision Drivers

- Rules run on vmalert or Prometheus. vmalert speaks MetricsQL, a superset of PromQL.
- The parser must be the one the ruler uses, not an approximation of it.
- What is drawn must be a query the same backend accepts.

## Considered Options

- `github.com/VictoriaMetrics/metricsql`, the parser vmalert and VictoriaMetrics use.
- `github.com/prometheus/prometheus/promql/parser`.
- A hand-written scanner.

## Decision Outcome

Chosen option: "`metricsql`", because it parses every PromQL expression and every MetricsQL
extension, it is a small module (the Prometheus parser pulls in much of Prometheus), and it prints an
expression back in a form both backends accept.

- `internal/promql` parses once and walks the tree: the left side of one top-level comparison with a
  number, `absent` and `absent_over_time` anywhere, set operators at the top, the change functions
  `delta`, `delta_prometheus`, `idelta`, `changes` and `deriv` with a range argument.
- The drawn query is the parser's own rendering, such as `sum(rate(x[5m])) by(namespace)`.
- An expression that does not parse draws nothing: the timeline is the fallback.

### Consequences

- Good, because the reading matches the ruler's.
- Bad, because a parser release can change how an expression prints; the tests pin the rendering,
  and Renovate does not automerge the module.

### Confirmation

`internal/promql` tests cover each case the hand-written scanner handled, including a comparison
inside a label value, `bool`, subqueries, comments and set operators.

## More Information

- 2026-10-29: accepted with the v0.1 scope.
