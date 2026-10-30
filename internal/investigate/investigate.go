// Package investigate hands an incident to an agent platform for an investigation and reads the
// findings back. Ploeg is the first platform: fogbell admits a manual-origin Work Item to a team
// whose plan investigates, read-only, and posts the Runs' findings in the incident's thread.
package investigate

import (
	"context"
	"errors"
)

// Request is an investigation of one incident.
type Request struct {
	// ID is unique per incident firing; a repeat returns the same investigation.
	ID          string
	Title       string
	Description string
}

// Status is where an investigation stands.
type Status struct {
	Done     bool
	State    string
	Findings string
	URL      string
}

// ErrRefused is an investigation the platform would not start, such as a spent allowance.
var ErrRefused = errors.New("investigation refused")

// Investigator starts investigations and reads their findings.
type Investigator interface {
	Name() string
	Available() bool
	Start(ctx context.Context, r Request) (id string, err error)
	Status(ctx context.Context, id string) (Status, error)
}
