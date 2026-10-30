package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/webgrip/fogbell/internal/events"
	"github.com/webgrip/fogbell/internal/investigate"
	"github.com/webgrip/fogbell/internal/state"
)

func TestDecideIsTheLifecycleTable(t *testing.T) {
	open := &state.Incident{State: "firing"}
	closed := &state.Incident{State: "resolved", ResolvedAt: 1000}
	for _, c := range []struct {
		found    bool
		latest   *state.Incident
		resolved bool
		now      float64
		want     Step
	}{
		{false, &state.Incident{}, false, 2000, Post},
		{false, &state.Incident{}, true, 2000, Ignore},
		{true, open, false, 2000, Update},
		{true, open, true, 2000, Resolve},
		{true, closed, false, 1000 + 3599, Reopen},
		{true, closed, false, 1000 + 3601, Post},
		{true, closed, true, 1500, Ignore},
	} {
		if got := Decide(c.found, c.latest, c.resolved, c.now, 3600); got != c.want {
			t.Errorf("Decide(%v, %v, %v, %v) = %v, want %v", c.found, c.latest.State, c.resolved, c.now, got, c.want)
		}
	}
}

type fakeInvestigator struct {
	started []investigate.Request
	status  investigate.Status
	err     error
}

func (f *fakeInvestigator) Name() string    { return "Crew" }
func (f *fakeInvestigator) Available() bool { return true }
func (f *fakeInvestigator) Start(_ context.Context, r investigate.Request) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.started = append(f.started, r)
	return "42", nil
}
func (f *fakeInvestigator) Status(context.Context, string) (investigate.Status, error) {
	return f.status, nil
}

type recorder struct{ kinds []string }

func (r *recorder) Send(_ context.Context, kind string, _ events.Incident) {
	r.kinds = append(r.kinds, kind)
}

func TestAnInvestigationIsStartedOnceAndItsFindingsLandInTheThread(t *testing.T) {
	h := newHarness(t, "")
	inv := &fakeInvestigator{status: investigate.Status{State: "active"}}
	rec := &recorder{}
	h.e.Investigator, h.e.Events = inv, rec
	h.notify("c", firing())
	if !contains(ids(h.only()), "investigate") {
		t.Fatal(ids(h.only()))
	}
	if got := h.press("investigate", "", "", ""); got != "investigate" {
		t.Fatal(got)
	}
	if got := h.press("investigate", "", "", "U2"); got != "investigation-running" || len(inv.started) != 1 {
		t.Fatal(got, len(inv.started))
	}
	brief := inv.started[0].Description
	for _, want := range []string{"Find out why, without changing anything", "`X` (warning)", "https://slack.com/archives/CC/p", "## Impact\n\nNobody can work.", "Do not change anything."} {
		if !strings.Contains(brief, want) {
			t.Fatal(want, "\n", brief)
		}
	}
	if contains(ids(h.only()), "investigate") {
		t.Fatal("the button stays after the press")
	}
	if got := h.maintain(); !contains(got, "investigating") {
		t.Fatal(got)
	}
	inv.status = investigate.Status{Done: true, State: "done", URL: "https://crew.example/w/42", Findings: "The disk is full.\n*Fix:* grow the volume"}
	h.now += 120
	if got := h.maintain(); !contains(got, "investigated") {
		t.Fatal(got)
	}
	if r := h.slack.LastReply(); !strings.Contains(r, "Crew finished the investigation <@U1> asked for (done): <https://crew.example/w/42|the work item>") ||
		!strings.Contains(r, "The disk is full.") || !strings.Contains(r, "An agent wrote this") {
		t.Fatal(r)
	}
	if !contains(ids(h.only()), "link_investigation") {
		t.Fatal(ids(h.only()))
	}
	if got := h.maintain(); contains(got, "investigated") {
		t.Fatal("findings posted twice")
	}
	if strings.Join(rec.kinds, ",") != "opened,investigated" {
		t.Fatal(rec.kinds)
	}
}

func TestARefusedInvestigationIsSaidInTheThread(t *testing.T) {
	h := newHarness(t, "")
	h.e.Investigator = &fakeInvestigator{err: investigate.ErrRefused}
	h.notify("c", firing())
	if got := h.press("investigate", "", "", ""); got != "investigate-failed" {
		t.Fatal(got)
	}
	if !strings.Contains(h.slack.LastReply(), "it refused, most likely a spent allowance") {
		t.Fatal(h.slack.LastReply())
	}
}

func TestEveryLifecycleStepIsAnEvent(t *testing.T) {
	h := newHarness(t, "")
	rec := &recorder{}
	h.e.Events = rec
	h.notify("c", firing())
	h.notify("c", group("", 2, "", ""))
	h.press("ack", "", "", "")
	h.press("silence", "1h|0", "", "")
	h.press("unsilence", "", "", "")
	h.notify("c", group("resolved", 1, "", ""))
	h.now += 60
	h.notify("c", firing())
	if got := strings.Join(rec.kinds, ","); got != "opened,updated,acknowledged,silenced,unsilenced,resolved,reopened" {
		t.Fatal(got)
	}
}
