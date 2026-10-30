package engine

import (
	"context"

	"github.com/webgrip/fogbell/internal/alert"
	"github.com/webgrip/fogbell/internal/enrich"
	"github.com/webgrip/fogbell/internal/state"
	"github.com/webgrip/fogbell/internal/store"
)

// run runs enrichers on a subject, counts each outcome, posts replies and files in the thread, and
// returns the lines for the message by slot: "" for a line enricher that ran and found nothing. An
// enricher that fails is counted and has no entry.
func (e *Engine) run(ctx context.Context, list []enrich.Enricher, sub *enrich.Subject, thread bool) map[enrich.Slot]string {
	lines := map[enrich.Slot]string{}
	for _, en := range list {
		res, err := en.Enrich(ctx, sub)
		if err != nil {
			e.Metrics.Enriched(en.Kind(), enrich.Failed)
			e.Log.Warn("enrichment failed", "kind", en.Kind(), "error", err)
			continue
		}
		if thread && res.File != nil {
			if err := e.Slack.Upload(ctx, sub.Channel, sub.TS, res.File.Data, res.File.Name, res.File.Comment); err != nil {
				e.Metrics.Enriched(en.Kind(), enrich.Failed)
				e.Log.Warn("upload failed", "kind", en.Kind(), "error", err)
				continue
			}
		}
		e.Metrics.Enriched(en.Kind(), res.Outcome)
		if thread && res.Reply != "" {
			e.reply(ctx, sub.Channel, sub.TS, res.Reply)
		}
		if l, ok := en.(enrich.Line); ok {
			lines[l.Slot()] = res.Line
		}
	}
	return lines
}

// enrich adds what the first post did not wait for, then edits the message once with the lines.
func (e *Engine) enrich(ctx context.Context, posted store.Entry, key string, g *alert.Group) {
	lines := e.run(ctx, e.enrichers, e.subject(g, posted), true)
	for s, v := range lines {
		if v == "" {
			delete(lines, s)
		}
	}
	if len(lines) == 0 {
		return
	}
	defer e.Store.Lock(key)()
	en, ok := e.Store.Get(key)
	if !ok || en.TS != posted.TS {
		return
	}
	setLines(&en.State, lines)
	if cur := e.Store.Group(key); cur != nil {
		g = cur
	}
	if err := e.update(ctx, en, g); err != nil {
		e.Log.Warn("enrichment update failed", "error", err)
		return
	}
	e.Store.Put(key, en)
}

// refresh redraws the refreshable lines of a firing incident in place, and edits the message only when
// one changed. It needs the group in memory: after a restart that is the next notification.
func (e *Engine) refresh(ctx context.Context, key string, en store.Entry) string {
	g := e.Store.Group(key)
	if g == nil {
		return "unknown"
	}
	var list []enrich.Enricher
	for _, x := range e.enrichers {
		if r, ok := x.(enrich.Refreshable); ok && r.Refresh() {
			list = append(list, x)
		}
	}
	lines := e.run(ctx, list, e.subject(g, en), false)
	defer e.Store.Lock(key)()
	cur, ok := e.Store.Get(key)
	if !ok || cur.TS != en.TS || cur.State.State != "firing" {
		return "changed"
	}
	cur.State.RefreshedAt = e.now()
	if lines[enrich.SlotSpark] == "" {
		delete(lines, enrich.SlotSpark)
	}
	if !changes(&cur.State, lines) {
		e.Store.Put(key, cur)
		e.Metrics.Refreshes.WithLabelValues("same").Inc()
		return "same"
	}
	setLines(&cur.State, lines)
	if err := e.update(ctx, cur, g); err != nil {
		e.Log.Warn("refresh update failed", "error", err)
		return "error"
	}
	e.Store.Put(key, cur)
	e.Metrics.Refreshes.WithLabelValues("edited").Inc()
	return "refreshed"
}

func (e *Engine) subject(g *alert.Group, en store.Entry) *enrich.Subject {
	return enrich.NewSubject(g, en.Channel, en.TS, e.up.Ruler)
}

func slot(st *state.Incident, s enrich.Slot) *string {
	switch s {
	case enrich.SlotSpark:
		return &st.Spark
	case enrich.SlotHistory:
		return &st.History
	case enrich.SlotRelated:
		return &st.Related
	case enrich.SlotChange:
		return &st.Change
	}
	return nil
}

func setLines(st *state.Incident, lines map[enrich.Slot]string) {
	for s, v := range lines {
		if p := slot(st, s); p != nil {
			*p = v
		}
	}
}

func changes(st *state.Incident, lines map[enrich.Slot]string) bool {
	for s, v := range lines {
		if p := slot(st, s); p != nil && *p != v {
			return true
		}
	}
	return false
}
