package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/webgrip/fogbell/internal/alert"
	"github.com/webgrip/fogbell/internal/format"
	"github.com/webgrip/fogbell/internal/render"
	"github.com/webgrip/fogbell/internal/store"
)

// fold puts a pod alert on a node whose parent incident (by default KubeNodeNotReady) is open and was
// shown within the fold window into that incident's thread, not into a message of its own: the node
// explains it. Later notifications of the group stay there until the parent resolves. "" when the
// group is not folded.
func (e *Engine) fold(ctx context.Context, key string, g *alert.Group, now float64) string {
	active := g.Active()
	if c, ok := e.Store.Child(key); ok {
		return e.folded(ctx, key, c, g, active)
	}
	cfg := e.Config.Incidents.Fold
	if g.Resolved() || cfg.ParentAlertname == "" || !e.allPods(active) {
		return ""
	}
	parents := map[string]store.Entry{}
	e.Store.Each(func(k string, en store.Entry) {
		st := en.State
		if st.Alertname == cfg.ParentAlertname && st.State == "firing" && now-st.Shown(st.PostedAt) <= cfg.Window.Seconds() {
			parents[k] = en
		}
	})
	if len(parents) == 0 {
		return ""
	}
	node := e.oneNode(ctx, active)
	if node == "" {
		return ""
	}
	var parentKey string
	var parent store.Entry
	for k, en := range parents {
		for _, o := range en.State.ObjectList() {
			if o.Labels[cfg.Label] == node {
				parentKey, parent = k, en
			}
		}
	}
	if parentKey == "" {
		return ""
	}
	var lines []string
	for _, a := range active[:min(5, len(active))] {
		lines = append(lines, e.Render.AlertLine(a, true))
	}
	more := ""
	if len(active) > 5 {
		more = fmt.Sprintf("\n_%d more._", len(active)-5)
	}
	e.reply(ctx, parent.Channel, parent.TS, "↳ "+e.Render.Icon(g, false)+" *"+format.Escape(g.Headline())+"* on `"+
		format.Escape(node)+"`, folded in here because the node is not ready:\n"+strings.Join(lines, "\n")+more)
	e.adopt(ctx, parentKey, key, len(active))
	e.Metrics.Nested.WithLabelValues(g.Label("alertname")).Inc()
	return "nested"
}

func (e *Engine) folded(ctx context.Context, key string, c store.Child, g *alert.Group, active []alert.Alert) string {
	parent, ok := e.Store.Get(c.Parent)
	if !ok || parent.TS != c.TS || parent.State.Resolved() {
		e.Store.SetChild(key, store.Child{Count: -1})
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

func (e *Engine) allPods(active []alert.Alert) bool {
	for _, a := range active {
		if !e.Config.ObjectReader().Is(a.Labels, "pod") {
			return false
		}
	}
	return len(active) > 0
}

func (e *Engine) oneNode(ctx context.Context, active []alert.Alert) string {
	nodes := map[string]bool{}
	for _, a := range active {
		nodes[e.nodeOf(ctx, a.Labels)] = true
	}
	if len(nodes) != 1 || nodes[""] {
		return ""
	}
	for n := range nodes {
		return n
	}
	return ""
}

// adopt records a child's alert count on the parent incident's metadata, or forgets it (count -1),
// so a restart still knows where its notifications go.
func (e *Engine) adopt(ctx context.Context, parentKey, childKey string, count int) {
	defer e.Store.Lock(parentKey)()
	en, ok := e.Store.Get(parentKey)
	if !ok {
		return
	}
	kids := en.State.ChildMap()
	if count < 0 {
		delete(kids, childKey)
	} else {
		kids[childKey] = count
	}
	e.Store.SetChild(childKey, store.Child{Channel: en.Channel, TS: en.TS, Parent: parentKey, Count: count})
	en.State.SetChildren(kids)
	if msg := e.Slack.Message(ctx, en.Channel, en.TS); msg != nil {
		e.restate(ctx, en, msg)
	}
	e.Store.Put(parentKey, en)
}

// nodeOf is the node a pod runs on: its fold label, or kube_pod_info.
func (e *Engine) nodeOf(ctx context.Context, labels map[string]string) string {
	label := e.Config.Incidents.Fold.Label
	if labels[label] != "" || e.up.Prom == nil {
		return labels[label]
	}
	node, err := e.up.Prom.PodNode(ctx, labels["namespace"], labels["pod"], label)
	if err != nil {
		e.Log.Warn("kube_pod_info lookup failed", "error", err)
	}
	return node
}
