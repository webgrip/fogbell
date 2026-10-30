package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/webgrip/fogbell/internal/spark"
)

// Prometheus is a Prometheus-compatible query API, such as VictoriaMetrics.
type Prometheus struct {
	URL  string
	HTTP *http.Client
}

// Series is one series of a query result.
type Series struct {
	Metric map[string]string
	Values []spark.Sample
}

type rawSeries struct {
	Metric map[string]string `json:"metric"`
	Values [][2]any          `json:"values"`
	Value  [2]any            `json:"value"`
}

func sample(p [2]any) (spark.Sample, bool) {
	t, ok := p[0].(float64)
	if !ok {
		return spark.Sample{}, false
	}
	s, ok := p[1].(string)
	if !ok {
		return spark.Sample{}, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return spark.Sample{}, false
	}
	return spark.Sample{T: t, V: v}, true
}

func (p *Prometheus) get(ctx context.Context, path string, q url.Values) ([]rawSeries, error) {
	var body struct {
		Data struct {
			Result []rawSeries `json:"result"`
		} `json:"data"`
	}
	if err := do(ctx, p.HTTP, http.MethodGet, join(p.URL, path)+"?"+q.Encode(), nil, nil, &body); err != nil {
		return nil, err
	}
	return body.Data.Result, nil
}

// QueryRange is an expression over [end-window, end] at step.
func (p *Prometheus) QueryRange(ctx context.Context, expr string, end, window, step int64) ([]Series, error) {
	raw, err := p.get(ctx, "/api/v1/query_range", url.Values{"query": {expr}, "start": {strconv.FormatInt(end-window, 10)},
		"end": {strconv.FormatInt(end, 10)}, "step": {strconv.FormatInt(step, 10)}})
	if err != nil {
		return nil, err
	}
	out := make([]Series, len(raw))
	for i, r := range raw {
		out[i].Metric = r.Metric
		for _, v := range r.Values {
			if s, ok := sample(v); ok {
				out[i].Values = append(out[i].Values, s)
			}
		}
	}
	return out, nil
}

// Query is an instant query at a time; each series carries one value.
func (p *Prometheus) Query(ctx context.Context, expr string, at int64) ([]Series, error) {
	q := url.Values{"query": {expr}}
	if at != 0 {
		q.Set("time", strconv.FormatInt(at, 10))
	}
	raw, err := p.get(ctx, "/api/v1/query", q)
	if err != nil {
		return nil, err
	}
	out := make([]Series, len(raw))
	for i, r := range raw {
		out[i].Metric = r.Metric
		if s, ok := sample(r.Value); ok {
			out[i].Values = []spark.Sample{s}
		}
	}
	return out, nil
}

// Pick is the series of a result that is about an alert: every label the two share agrees, and the
// most shared wins. With one series and no agreement, that one.
func Pick(result []Series, labels map[string]string) *Series {
	best, bestShared := -1, -1
	for i, s := range result {
		shared, ok := 0, true
		for k, v := range s.Metric {
			if k == "__name__" {
				continue
			}
			if lv, has := labels[k]; has {
				shared++
				if lv != v {
					ok = false
				}
			}
		}
		if ok && shared > bestShared {
			best, bestShared = i, shared
		}
	}
	if best < 0 {
		if len(result) == 1 {
			return &result[0]
		}
		return nil
	}
	return &result[best]
}

// PodNode is the node a pod runs on, from kube_pod_info; "" when it is not exactly one.
func (p *Prometheus) PodNode(ctx context.Context, namespace, pod, label string) (string, error) {
	result, err := p.Query(ctx, fmt.Sprintf(`kube_pod_info{namespace=%q,pod=%q}`, namespace, pod), 0)
	if err != nil {
		return "", err
	}
	nodes := map[string]bool{}
	for _, s := range result {
		nodes[s.Metric[label]] = true
	}
	for n := range nodes {
		if len(nodes) == 1 {
			return n, nil
		}
	}
	return "", nil
}

// Ruler reads a rule's expression for an alert.
type Ruler struct {
	URL           string
	GeneratorHost *regexp.Regexp
	HTTP          *http.Client
}

// Expression is the expression of the rule that generated an alert. A Prometheus generator URL carries
// it in g0.expr; a vmalert one names the rule, which the ruler API is asked for when the generator's
// host matches GeneratorHost. "" when neither applies.
func (r *Ruler) Expression(ctx context.Context, generatorURL string) (string, error) {
	gen, err := url.Parse(generatorURL)
	if err != nil || generatorURL == "" {
		return "", nil
	}
	q := gen.Query()
	if expr := q.Get("g0.expr"); expr != "" {
		return expr, nil
	}
	if r == nil || r.URL == "" || q.Get("group_id") == "" || q.Get("alert_id") == "" {
		return "", nil
	}
	if r.GeneratorHost != nil && !r.GeneratorHost.MatchString(gen.Hostname()) {
		return "", nil
	}
	var rule struct {
		Expression string `json:"expression"`
	}
	target := join(r.URL, "/api/v1/alert") + "?" + url.Values{"group_id": {q.Get("group_id")}, "alert_id": {q.Get("alert_id")}}.Encode()
	if err := do(ctx, r.HTTP, http.MethodGet, target, nil, nil, &rule); err != nil {
		return "", err
	}
	return rule.Expression, nil
}

// Logs is the VictoriaLogs query API.
type Logs struct {
	URL  string
	HTTP *http.Client
}

// Row is a log record with the fields fogbell asks for.
type Row struct {
	Time  string `json:"_time"`
	Msg   string `json:"_msg"`
	Error string `json:"error"`
	Err   string `json:"err"`
}

// Query runs a LogsQL query and returns its rows, oldest first.
func (l *Logs) Query(ctx context.Context, query string) ([]Row, error) {
	body := url.Values{"query": {query}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, join(l.URL, "/select/logsql/query"), strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := l.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return nil, &StatusError{URL: redactURL(req.URL.String()), Code: resp.StatusCode}
	}
	var rows []Row
	dec := json.NewDecoder(resp.Body)
	for dec.More() {
		var r Row
		if err := dec.Decode(&r); err != nil {
			return nil, err
		}
		rows = append(rows, r)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Time < rows[j].Time })
	return rows, nil
}

// LogsQLString quotes a value for LogsQL.
func LogsQLString(v string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v) + `"`
}
