package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/fogbell-dev/fogbell/internal/alert"
	"github.com/fogbell-dev/fogbell/internal/format"
	"github.com/fogbell-dev/fogbell/internal/render"
)

// fold puts a pod alert on a node whose parent incident (by default KubeNodeNotReady) is open and was
// shown within the fold window into that incident's thread, not into a message of its own: the node
// explains it. Later notifications of the group stay there until the parent resolves. "" when the
// group is not folded.
func (e *Engine) fold(ctx context.Context, key string, g *alert.Group, active []alert.Alert, now float64) string {
	cfg := e.Config.Incidents.Fold
	e.mu.Lock()
	e.init()
	c, isChild := e.children[key]
	e.mu.Unlock()
	if isChild {
		parent, ok := e.get(c.Parent)
		if !ok || parent.TS != c.TS || parent.St.Resolved() {
			e.mu.Lock()
			delete(e.children, key)
			e.mu.Unlock()
			return ""
		}
		switch {
		case g.Resolved():
			e.reply(ctx, c.Channel, c.TS, "↳ ✅ Resolved: "+format.Escape(g.Headline()))
			e.adopt(ctx, c.Parent, key, -1)
			return "nested-resolved"
		case len(active) != c.Count:
			e.reply(ctx, c.Channel, c.TS, "↳ "+format.Escape(g.Headline())+": "+render.DescribeChange(c.Count, g))
			e.adopt(ctx, c.Parent, key, len(active))
			return "nested-updated"
		}
		return "nested-unchanged"
	}
	if g.Resolved() || cfg.ParentAlertname == "" {
		return ""
	}
	type parent struct {
		key string
		en  entry
	}
	var parents []parent
	e.mu.Lock()
	for k, en := range e.index {
		if en.St.Alertname == cfg.ParentAlertname && en.St.State == "firing" && now-en.St.Shown(en.St.PostedAt) <= cfg.Window.Seconds() {
			parents = append(parents, parent{k, en})
		}
	}
	e.mu.Unlock()
	objects := e.Config.ObjectReader()
	if len(parents) == 0 {
		return ""
	}
	for _, a := range active {
		if !objects.Is(a.Labels, "pod") {
			return ""
		}
	}
	nodes := map[string]bool{}
	for _, a := range active {
		nodes[e.nodeOf(ctx, a.Labels)] = true
	}
	if len(nodes) != 1 || nodes[""] {
		return ""
	}
	var node string
	for n := range nodes {
		node = n
	}
	for _, p := range parents {
		for _, o := range p.en.St.ObjectList() {
			if o.Labels[cfg.Label] != node {
				continue
			}
			var lines []string
			for i, a := range active {
				if i == 5 {
					break
				}
				lines = append(lines, e.Render.AlertLine(a, true))
			}
			more := ""
			if len(active) > 5 {
				more = fmt.Sprintf("\n_%d more._", len(active)-5)
			}
			e.reply(ctx, p.en.Channel, p.en.TS, "↳ "+e.Render.Icon(g, false)+" *"+format.Escape(g.Headline())+"* on `"+
				format.Escape(node)+"`, folded in here because the node is not ready:\n"+strings.Join(lines, "\n")+more)
			e.adopt(ctx, p.key, key, len(active))
			e.Metrics.Nested.WithLabelValues(g.Label("alertname")).Inc()
			return "nested"
		}
	}
	return ""
}

// adopt records a child's alert count on the parent incident's metadata, or forgets it (count -1), so
// a restart still knows where its notifications go. It takes the parent's lock while the caller holds
// the child's; a parent never takes a child's lock.
func (e *Engine) adopt(ctx context.Context, parentKey, childKey string, count int) {
	l := e.lock(parentKey)
	l.Lock()
	defer l.Unlock()
	en, ok := e.get(parentKey)
	if !ok {
		return
	}
	kids := en.St.ChildMap()
	e.mu.Lock()
	if count < 0 {
		delete(kids, childKey)
		delete(e.children, childKey)
	} else {
		kids[childKey] = count
		e.children[childKey] = child{Channel: en.Channel, TS: en.TS, Parent: parentKey, Count: count}
	}
	e.mu.Unlock()
	st := en.St
	st.SetChildren(kids)
	if msg := e.Slack.Message(ctx, en.Channel, en.TS); msg != nil {
		e.restate(ctx, en.Channel, en.TS, msg, &st)
	}
	e.put(parentKey, entry{en.Channel, en.TS, st})
}

// nodeOf is the node a pod runs on: its fold label, or kube_pod_info.
func (e *Engine) nodeOf(ctx context.Context, labels map[string]string) string {
	label := e.Config.Incidents.Fold.Label
	if labels[label] != "" {
		return labels[label]
	}
	if e.Prom == nil {
		return ""
	}
	sel := fmt.Sprintf(`kube_pod_info{namespace=%q,pod=%q}`, labels["namespace"], labels["pod"])
	result, err := e.Prom.Query(ctx, sel, 0)
	if err != nil {
		e.Log.Warn("kube_pod_info lookup failed", "error", err)
		return ""
	}
	nodes := map[string]bool{}
	for _, s := range result {
		nodes[s.Metric[label]] = true
	}
	if len(nodes) != 1 {
		return ""
	}
	for n := range nodes {
		return n
	}
	return ""
}
