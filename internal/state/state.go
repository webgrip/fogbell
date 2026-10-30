// Package state is the incident state fogbell keeps on the Slack message itself, as message metadata.
//
// The field names and the encoding of nested values as JSON strings are schema version 1
// (docs/adrs/0001-the-slack-message-is-the-only-store.md). Slack metadata payloads hold flat values, so
// labels, objects, contract fields and children travel as JSON-encoded strings.
package state

import (
	"encoding/json"
	"math"
	"strconv"

	"github.com/webgrip/fogbell/internal/alert"
)

// Int is an integer that also reads a JSON number written with a fraction, such as 3.0.
type Int int

// UnmarshalJSON reads any JSON number, or a quoted one.
func (n *Int) UnmarshalJSON(b []byte) error {
	s := string(b)
	if s == "null" {
		*n = 0
		return nil
	}
	if len(s) >= 2 && s[0] == '"' {
		s = s[1 : len(s)-1]
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return err
	}
	*n = Int(math.Round(f))
	return nil
}

// Incident is the state of one incident message.
type Incident struct {
	GroupKey      string  `json:"group_key"`
	State         string  `json:"state"`
	Count         Int     `json:"count"`
	StartedAt     float64 `json:"started_at,omitempty"`
	Alertname     string  `json:"alertname,omitempty"`
	GroupLabels   string  `json:"group_labels,omitempty"`
	Flaps         Int     `json:"flaps,omitempty"`
	Objects       string  `json:"objects,omitempty"`
	Severity      string  `json:"severity,omitempty"`
	Receiver      string  `json:"receiver,omitempty"`
	Team          string  `json:"team,omitempty"`
	PostedAt      float64 `json:"posted_at,omitempty"`
	FiredAt       float64 `json:"fired_at,omitempty"`
	Fields        string  `json:"fields,omitempty"`
	WeekCount     Int     `json:"week_count,omitempty"`
	Children      string  `json:"children,omitempty"`
	ResolvedAt    float64 `json:"resolved_at,omitempty"`
	AckedBy       string  `json:"acked_by,omitempty"`
	SilenceID     string  `json:"silence_id,omitempty"`
	SilenceUntil  float64 `json:"silence_until,omitempty"`
	SilenceBy     string  `json:"silence_by,omitempty"`
	SilenceReason string  `json:"silence_reason,omitempty"`
	SilenceAt     float64 `json:"silence_at,omitempty"`
	ExpiryWarned  string  `json:"expiry_warned,omitempty"`
	Escalated     float64 `json:"escalated,omitempty"`
	TicketURL     string  `json:"ticket_url,omitempty"`
	TicketID      string  `json:"ticket_id,omitempty"`
	NoiseBy       string  `json:"noise_by,omitempty"`
	NoiseVotes    Int     `json:"noise_votes,omitempty"`
	RefreshedAt   float64 `json:"refreshed_at,omitempty"`
	Spark         string  `json:"spark,omitempty"`
	History       string  `json:"history,omitempty"`
	Related       string  `json:"related,omitempty"`
	Change        string  `json:"change,omitempty"`
	// Investigation is the agent platform's id of an investigation started from the message, with
	// its link and whether its findings were posted.
	Investigation     string `json:"investigation_id,omitempty"`
	InvestigationURL  string `json:"investigation_url,omitempty"`
	InvestigationBy   string `json:"investigation_by,omitempty"`
	InvestigationDone bool   `json:"investigation_done,omitempty"`
}

// Resolved reports whether the incident is closed.
func (s *Incident) Resolved() bool { return s.State == "resolved" }

// Firings is how often the incident fired within the reopen window; 1 when it never re-fired.
func (s *Incident) Firings() int {
	if s.Flaps == 0 {
		return 1
	}
	return int(s.Flaps)
}

// Shown is when this firing was shown: the latest re-fire, else the first post, else when it started.
func (s *Incident) Shown(fallback float64) float64 {
	switch {
	case s.FiredAt != 0:
		return s.FiredAt
	case s.StartedAt != 0:
		return s.StartedAt
	default:
		return fallback
	}
}

// LabelSet decodes the group labels.
func (s *Incident) LabelSet() map[string]string {
	out := map[string]string{}
	_ = json.Unmarshal([]byte(s.GroupLabels), &out)
	return out
}

// ObjectList decodes the objects a silence can be narrowed to.
func (s *Incident) ObjectList() []alert.Object {
	var out []alert.Object
	_ = json.Unmarshal([]byte(s.Objects), &out)
	return out
}

// SetObjects encodes the objects.
func (s *Incident) SetObjects(objs []alert.Object) {
	if objs == nil {
		objs = []alert.Object{}
	}
	b, _ := json.Marshal(objs)
	s.Objects = string(b)
}

// FieldMap decodes the first alert's contract fields.
func (s *Incident) FieldMap() map[string]string {
	out := map[string]string{}
	_ = json.Unmarshal([]byte(s.Fields), &out)
	return out
}

// ChildMap decodes the child group keys folded into this incident's thread, with their alert counts.
func (s *Incident) ChildMap() map[string]int {
	raw := map[string]Int{}
	_ = json.Unmarshal([]byte(s.Children), &raw)
	out := make(map[string]int, len(raw))
	for k, v := range raw {
		out[k] = int(v)
	}
	return out
}

// SetChildren encodes the children.
func (s *Incident) SetChildren(kids map[string]int) {
	b, _ := json.Marshal(kids)
	s.Children = string(b)
}

// Digest is the state of a daily digest message.
type Digest struct {
	Day string `json:"day"`
}

// NoiseReport is the state of a weekly noise report message.
type NoiseReport struct {
	Week string `json:"week"`
}
