package enrich

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/webgrip/fogbell/internal/format"
	"github.com/webgrip/fogbell/internal/state"
	"github.com/webgrip/fogbell/internal/store"
	"github.com/webgrip/fogbell/internal/upstream"
)

// History is how the rule behaved in this channel over the last week: how often, how long it lasted,
// noise votes and who picked it up last.
type History struct {
	Store *store.Store
	Now   func() time.Time
}

// Kind implements Enricher.
func (*History) Kind() string { return "history" }

// Slot implements Line.
func (*History) Slot() Slot { return SlotHistory }

// Enrich implements Enricher.
func (h *History) Enrich(ctx context.Context, sub *Subject) (Result, error) {
	name := sub.Group.Label("alertname")
	if name == "" {
		return outcome(None)
	}
	var past []*state.Incident
	for _, m := range h.Store.Incidents(ctx, sub.Channel, float64(h.Now().Unix())-7*86400) {
		if m.Msg.TS != sub.TS && m.State.Alertname == name {
			past = append(past, m.State)
		}
	}
	if len(past) == 0 {
		return outcome(None)
	}
	firings, votes := 0, 0
	var took []float64
	for _, st := range past {
		firings += st.Firings()
		votes += int(st.NoiseVotes)
		if st.Resolved() && st.ResolvedAt != 0 {
			took = append(took, st.ResolvedAt-st.Shown(st.ResolvedAt))
		}
	}
	parts := []string{fmt.Sprintf("🕘 Fired %d time%s before in 7 days", firings, format.Plural(firings))}
	if len(took) > 0 {
		sort.Float64s(took)
		parts = append(parts, "usually over in "+format.Duration(took[len(took)/2]))
	}
	if votes > 0 {
		parts = append(parts, fmt.Sprintf("🙈 %d noise vote%s", votes, format.Plural(votes)))
	}
	sort.SliceStable(past, func(i, j int) bool { return past[i].PostedAt > past[j].PostedAt })
	picked := "nobody pressed 👀 on any of them"
	for _, st := range past {
		if st.AckedBy != "" {
			picked = "last picked up by <@" + st.AckedBy + ">"
			break
		}
	}
	return line(OK, strings.Join(append(parts, picked), " · "))
}

// Related names other alerts firing now in the same namespace or on the same node: three incidents
// with one cause read as one.
type Related struct {
	AM    *upstream.Alertmanager
	Quiet func(alertname string) bool
}

// Kind implements Enricher.
func (*Related) Kind() string { return "related" }

// Slot implements Line.
func (*Related) Slot() Slot { return SlotRelated }

// Refresh implements Refreshable.
func (*Related) Refresh() bool { return true }

type scope struct{ key, where, value string }

// Enrich implements Enricher.
func (r *Related) Enrich(ctx context.Context, sub *Subject) (Result, error) {
	active := sub.Group.Active()
	var scopes []scope
	for _, s := range []scope{{key: "namespace", where: "in"}, {key: "node", where: "on"}} {
		values := map[string]bool{}
		for _, a := range active {
			values[a.Labels[s.key]] = true
		}
		if len(values) == 1 && !values[""] {
			for v := range values {
				s.value = v
			}
			scopes = append(scopes, s)
		}
	}
	if len(scopes) == 0 {
		return outcome(Skipped)
	}
	mine := map[string]bool{}
	for _, a := range active {
		mine[a.Fingerprint] = true
	}
	firing, err := r.AM.Firing(ctx)
	if err != nil {
		return Result{}, err
	}
	var parts []string
	for _, s := range scopes {
		if part := r.neighbours(s, firing, mine); part != "" {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return outcome(None)
	}
	return line(OK, "🧩 Also firing "+strings.Join(parts, " · "))
}

func (r *Related) neighbours(s scope, firing []upstream.FiringAlert, mine map[string]bool) string {
	names := map[string]int{}
	for _, a := range firing {
		n := a.Labels["alertname"]
		if mine[a.Fingerprint] || a.Labels[s.key] != s.value || r.Quiet(n) {
			continue
		}
		if n == "" {
			n = "?"
		}
		names[n]++
	}
	type count struct {
		name string
		n    int
	}
	top := make([]count, 0, len(names))
	for n, c := range names {
		top = append(top, count{n, c})
	}
	if len(top) == 0 {
		return ""
	}
	sort.Slice(top, func(i, j int) bool {
		if top[i].n != top[j].n {
			return top[i].n > top[j].n
		}
		return top[i].name < top[j].name
	})
	var shown []string
	for _, c := range top[:min(4, len(top))] {
		text := format.Escape(c.name)
		if c.n > 1 {
			text += fmt.Sprintf(" ×%d", c.n)
		}
		shown = append(shown, text)
	}
	more := ""
	if len(top) > 4 {
		more = fmt.Sprintf(" and %d more", len(top)-4)
	}
	return s.where + " `" + format.Escape(s.value) + "`: " + strings.Join(shown, ", ") + more
}
