package enrich

import (
	"context"
	"fmt"
	"strings"

	"github.com/webgrip/fogbell/internal/alert"
	"github.com/webgrip/fogbell/internal/format"
	"github.com/webgrip/fogbell/internal/redact"
	"github.com/webgrip/fogbell/internal/upstream"
)

const (
	logLines   = 15
	logLineMax = 240
	logBlock   = 2800
)

// LogFields are the VictoriaLogs stream fields that name a pod.
type LogFields struct {
	Namespace, Pod, Container string
}

// Logs is the last lines of the one pod an incident is about, as a thread reply, with obvious
// secrets redacted.
type Logs struct {
	Logs    *upstream.Logs
	Objects *alert.Objects
	Fields  LogFields
}

// Kind implements Enricher.
func (*Logs) Kind() string { return "logs" }

type pod struct{ ns, name, container string }

// Enrich implements Enricher.
func (l *Logs) Enrich(ctx context.Context, sub *Subject) (Result, error) {
	p, ok := l.onePod(sub.Group)
	if !ok {
		return outcome(Skipped)
	}
	stream := l.Fields.Namespace + "=" + upstream.LogsQLString(p.ns) + "," + l.Fields.Pod + "=" + upstream.LogsQLString(p.name)
	where := p.ns + "/" + p.name
	if p.container != "" {
		stream += "," + l.Fields.Container + "=" + upstream.LogsQLString(p.container)
		where += " (" + p.container + ")"
	}
	rows, about, err := l.rows(ctx, "_stream:{"+stream+"}", sub.Group.Active())
	if err != nil {
		return Result{}, err
	}
	if len(rows) == 0 {
		return outcome(None)
	}
	lines := make([]string, len(rows))
	for i, r := range rows {
		lines[i] = logLine(r)
	}
	return Result{Outcome: OK, Reply: fmt.Sprintf("📜 Last %d log lines of %s%s in the hour before this post, oldest first, obvious secrets redacted:\n",
		len(rows), format.Escape(where), format.Escape(about)) + codeBlock(lines)}, nil
}

func (l *Logs) onePod(g *alert.Group) (pod, bool) {
	pods := map[pod]bool{}
	for _, a := range g.Active() {
		if !l.Objects.Is(a.Labels, "pod") {
			return pod{}, false
		}
		pods[pod{a.Labels["namespace"], a.Labels["pod"], a.Labels["container"]}] = true
	}
	for p := range pods {
		return p, len(pods) == 1
	}
	return pod{}, false
}

// rows narrows a controller's logs to the object its alert is about (name and exported_namespace),
// and falls back to the whole pod when the controller does not log those fields.
func (l *Logs) rows(ctx context.Context, selector string, active []alert.Alert) ([]upstream.Row, string, error) {
	objs := map[[2]string]bool{}
	for _, a := range active {
		objs[[2]string{a.Labels["exported_namespace"], a.Labels["name"]}] = true
	}
	if len(objs) == 1 {
		for o := range objs {
			if o[0] == "" || o[1] == "" {
				break
			}
			rows, err := l.query(ctx, selector+" namespace:="+upstream.LogsQLString(o[0])+" name:="+upstream.LogsQLString(o[1]))
			if err != nil || len(rows) > 0 {
				return rows, " about " + o[0] + "/" + o[1], err
			}
		}
	}
	rows, err := l.query(ctx, selector)
	return rows, "", err
}

func (l *Logs) query(ctx context.Context, selector string) ([]upstream.Row, error) {
	return l.Logs.Query(ctx, fmt.Sprintf("%s _time:1h | sort by (_time) desc | limit %d | fields _time, _msg, error, err", selector, logLines))
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

func codeBlock(lines []string) string {
	out := make([]string, len(lines))
	for i, l := range lines {
		l = redact.Line(strings.TrimRight(l, "\n"))
		if len([]rune(l)) > logLineMax {
			l = format.Cut(l, logLineMax) + " …"
		}
		out[i] = strings.ReplaceAll(format.Escape(l), "```", "ˋˋˋ")
	}
	text := strings.Join(out, "\n")
	for len([]rune(text)) > logBlock && strings.Contains(text, "\n") {
		text = text[strings.Index(text, "\n")+1:]
	}
	return "```\n" + text + "\n```"
}
