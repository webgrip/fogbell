// Package enrich holds the lookups that add context to an incident after its first post: each one an
// Enricher, optional, read-only, and never able to fail the incident.
package enrich

import (
	"context"
	"sync"

	"github.com/webgrip/fogbell/internal/alert"
	"github.com/webgrip/fogbell/internal/upstream"
)

// Outcomes an enricher reports, counted in fogbell_enrichment_total{kind,result}.
const (
	OK       = "ok"
	None     = "none"
	Skipped  = "skipped"
	Timeline = "timeline"
	Denied   = "forbidden"
	Failed   = "error"
)

// Slot is where a line lands in the incident state.
type Slot string

// The line slots, in the order the message shows them.
const (
	SlotSpark   Slot = "spark"
	SlotHistory Slot = "history"
	SlotRelated Slot = "related"
	SlotChange  Slot = "change"
)

// File is a file for the thread.
type File struct {
	Name, Comment string
	Data          []byte
}

// Result is what an enricher adds: a line on the message, a reply in the thread, a file, or nothing.
type Result struct {
	Outcome string
	Line    string
	Reply   string
	File    *File
}

// Subject is the incident an enricher looks at.
type Subject struct {
	Group   *alert.Group
	Channel string
	TS      string

	ruler *upstream.Ruler
	once  sync.Once
	expr  string
	err   error
}

// NewSubject is a subject whose rule expression is read from ruler, once, when an enricher asks.
func NewSubject(g *alert.Group, channel, ts string, ruler *upstream.Ruler) *Subject {
	return &Subject{Group: g, Channel: channel, TS: ts, ruler: ruler}
}

// Expression is the expression of the rule behind a group of one alert; "" when unknown.
func (s *Subject) Expression(ctx context.Context) (string, error) {
	s.once.Do(func() {
		active := s.Group.Active()
		if len(active) == 1 && s.ruler != nil {
			s.expr, s.err = s.ruler.Expression(ctx, active[0].GeneratorURL)
		}
	})
	return s.expr, s.err
}

// Enricher looks one thing up for an incident.
type Enricher interface {
	// Kind names the enricher in metrics and logs.
	Kind() string
	Enrich(ctx context.Context, s *Subject) (Result, error)
}

// Line is an enricher whose result is a line on the message.
type Line interface {
	Enricher
	Slot() Slot
}

// Refreshable marks a line that is redrawn while the incident fires.
type Refreshable interface {
	Line
	Refresh() bool
}

func line(outcome, text string) (Result, error) { return Result{Outcome: outcome, Line: text}, nil }

func outcome(o string) (Result, error) { return Result{Outcome: o}, nil }
