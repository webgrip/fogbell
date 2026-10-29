---
status: "accepted"
date: "2026-10-29"
review-by: "2027-04-29"
---

# One replica, with the per-incident lock in memory

## Context and Problem Statement

Two notifications for the same group, or a notification and a button press, must not both read a
message's state and both write it back: one edit would be lost, or two messages posted. Where is the
lock?

## Decision Drivers

- No database ([ADR-0001](0001-the-slack-message-is-the-only-store.md)).
- Alertmanager retries a webhook that failed, so a short outage delays a message and loses none.
- The load is small: a few notifications a minute, a few presses an hour.

## Considered Options

- One replica, a mutex per group key in memory, `Recreate` rollouts.
- Several replicas with a Kubernetes Lease per group key.
- Several replicas with leader election, followers proxying to the leader.

## Decision Outcome

Chosen option: "One replica", because it is the least machinery that is correct, and Alertmanager's
retries already cover the seconds a restart takes.

- A mutex per group key serialises notifications and presses on one incident. Different incidents
  run in parallel. A folded child takes its parent's lock while holding its own; a parent never takes
  a child's, so the order cannot deadlock.
- The chart pins `replicas: 1` and `strategy: Recreate`, so two pods never overlap.
- Each press re-reads the message under the lock, so two people pressing at once make one
  acknowledgement, one silence and one ticket.

### Consequences

- Good, because there is nothing to coordinate.
- Bad, because a node failure means no posts until the pod is rescheduled; route an alert on fogbell
  itself around it ([metrics](../metrics.md)).

### Confirmation

- The chart's Deployment has `replicas: 1` and `Recreate`; `helm lint` and the chart test render it.
- `TestTimeToAckAndToResolve` presses twice and counts one acknowledgement; `TestTicketOnceThenALink`
  presses twice and creates one ticket. The suite runs with `-race`.

## More Information

- 2026-10-29: accepted with the v0.1 scope.
