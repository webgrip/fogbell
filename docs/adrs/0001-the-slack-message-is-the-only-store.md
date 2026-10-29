---
status: "accepted"
date: "2026-10-29"
review-by: "2027-04-29"
---

# The Slack message is the only store (state schema version 1)

## Context and Problem Statement

An incident has state beyond what Alertmanager knows: which message tracks it, who acknowledged it,
which silence was made from it, how often it re-fired, the noise votes, the ticket. fogbell must find
that state again after a restart, and the digest and noise report must read a week of it. Where does
it live?

## Decision Drivers

- One small binary that an operator can run without provisioning anything.
- A restart must not post a second message for an open incident.
- What people see in the channel and what fogbell believes must not drift apart.
- An earlier relay with the same fields must be adoptable without a migration.

## Considered Options

- Slack message metadata on each incident message.
- A database (Postgres or SQLite on a volume).
- Alertmanager's notification log, as its own `update_message` does.
- A Kubernetes ConfigMap or custom resource per incident.

## Decision Outcome

Chosen option: "Slack message metadata", because the message is the thing the state describes, it
survives fogbell, and reading the channel's history rebuilds everything.

- Each incident message carries metadata with event type `slack.events.incident` and a flat payload.
  Slack metadata payloads hold flat values, so labels, objects, contract fields and children are
  JSON-encoded strings.
- The field names and encodings in `internal/state` are **schema version 1**. A field is never
  renamed or retyped. A new field is optional; a message without it reads as its zero value.
  Integers are read leniently (`3.0` and `"3"` are 3), because a payload may have been written by
  another implementation.
- After a restart fogbell reads `historyWindow` of each alert channel once; the newest message per
  group key wins. Digests and noise reports carry their own event types, which dedupe them.
- `slack.adoptEvents` reads further event types as incidents, so threads another relay wrote are
  taken over ([adopting](../adopting.md)).

### Consequences

- Good, because there is nothing to back up, migrate or provision.
- Good, because the state is visible: a message's metadata is what fogbell acts on.
- Bad, because without `channels:history` a restart forgets open incidents; fogbell then keeps them
  in memory and counts `fogbell_history_unavailable_total`.
- Bad, because a week of history is read by paging the channel, which is slow in a channel with
  thousands of messages a week. A channel that busy needs grouping, not a database.
- Bad, because Alertmanager's notification groups are not stored: after a restart a firing group's
  alerts are known again only on its next notification, which Alertmanager repeats.

### Confirmation

- `internal/state` tests decode a payload written in the version-1 shape, including numbers written
  with a fraction and as strings.
- `TestRestartFindsTheOpenIncident` and `TestAdoptsAnEarlierEventType` in `internal/engine` read
  incidents back from the fake Slack after a restart.

## More Information

- 2026-10-29: accepted with the v0.1 scope. The field names are those of the single-file relay
  fogbell was ported from, so its threads stay readable.
