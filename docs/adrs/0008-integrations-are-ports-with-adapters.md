---
status: "accepted"
date: "2026-10-30"
review-by: "2027-04-30"
---

# Integrations are ports with adapters, and the engine knows only the ports

## Context and Problem Statement

The first port of the relay put every lookup, button and upstream in one engine type: each lookup
repeated its own metric and error handling, the rule expression was read twice per incident, and a
new button or system meant editing the same file. People also want fogbell to reach further: hand
an incident to an agent platform for an investigation, file tickets elsewhere, tell other systems
what happened. How is fogbell shaped so those are additions, not rewrites?

## Decision Drivers

- A new system must not touch the incident lifecycle.
- Every optional lookup must fail the same way: counted, logged, skipped.
- The engine must be testable without any of the systems it talks to.

## Considered Options

- Ports with adapters: an interface per seam, implementations in their own packages.
- Plugins loaded at runtime (Go plugins, WASM, subprocesses).
- Keep one engine type and add cases.

## Decision Outcome

Chosen option: "Ports with adapters", because each seam is small and known, and compiled-in
adapters keep one static binary.

- `enrich.Enricher` (`Kind`, `Enrich`), optionally a `Line` with a slot on the message and
  `Refreshable` when it is redrawn while the incident fires. One runner counts every outcome in
  `fogbell_enrichment_total`, posts replies and files, and collects lines. The rule expression is
  read once per incident through the `Subject`.
- `engine.Tracker` for tickets, `investigate.Investigator` for investigations, `events.Sink` for
  everything that happens.
- `store.Store` owns the incidents: the index over the Slack messages, the scans and the locks.
- Buttons are entries in an action registry; maintenance is a list of tasks with a due check
  re-run under the incident's lock.
- The lifecycle is a pure `Decide` function over the latest message and the notification.
- `internal/app` builds the adapters the configuration turns on; the engine never reads
  configuration to decide which systems exist.

### Consequences

- Good, because adding an adapter is one package and one line in `app`.
- Good, because the decision table and each adapter are tested alone.
- Bad, because there are more packages to find one's way in; [index](../index.md) maps them.

### Confirmation

- `TestDecideIsTheLifecycleTable` pins the lifecycle; the engine tests run through the ports with
  fakes; each adapter has its own tests against `httptest`.
- `internal/engine` imports no adapter package except through the interfaces it declares or the
  ones in `enrich`, `investigate` and `events`.

## More Information

- 2026-10-30: accepted with the redesign that added Ploeg investigations and CloudEvents.
