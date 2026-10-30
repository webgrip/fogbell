---
description: Rules for this repository
---
# fogbell

fogbell turns Alertmanager notifications into one Slack thread per incident, with buttons that act
on it, and keeps its state on the Slack message as metadata. `main` is the trunk; semantic-release tags it. Run `mise run setup` once and every tool through `mise exec --`. Start at
[docs/index.md](https://forgejo.webgrip.dev/webgrip/fogbell/src/branch/main/docs/index.md).

## Commits

Conventional Commits; stage only the paths you changed, never `git add -A`, `git add .` or
`git commit -a`. Sign off every commit with `git commit -s` (DCO, checked in CI). Releases are cut from
commit types by semantic-release; do not edit versions in `Chart.yaml` or anywhere else.

## Facts the tree won't tell you

- The metadata payload on Slack messages is a stored format. Its field names and the JSON-string
  encoding of nested values are schema version 1 (`internal/state`): never rename or retype a field.
  A new field is optional and absent on old messages; read it with its zero value in mind.
- `cmd/fogbell` reads flags; `internal/app` builds everything from the configuration; logic lives in
  the other `internal/` packages. A new system is an adapter on a port (`enrich.Enricher`,
  `engine.Tracker`, `investigate.Investigator`, `events.Sink`), wired in `internal/app`, never a case
  in the engine.
- No comments in Go code. Doc comments that tools read are fine.
- Every enrichment is optional and never fails an incident: the runner in `internal/engine` counts
  each outcome in `fogbell_enrichment_total{kind,result}`; an enricher returns an outcome or an error
  and never counts itself.
- The lifecycle is `engine.Decide`; a button is an entry in the action registry; time-driven work is
  a maintenance task with a due check.
- Never post, log or put a secret value in an error or a URL. Log lines posted to Slack go through
  `internal/redact` first.
- Tests fake Slack with `internal/slacktest` and every other service with `net/http/httptest`. A bug
  fix lands with a test that fails on the old code.
- Message text is part of the contract: tests assert on it. Change a sentence only on purpose.
- The project names no estate, company or person that uses it. Anything site-specific (URLs, channel
  names, label names, icons) is configuration.

## Before opening a pull request

Run `mise run verify` and put its result in the pull request.

## Decisions

Decisions go in [docs/adrs/](https://forgejo.webgrip.dev/webgrip/fogbell/src/branch/main/docs/adrs/README.md) (MADR 4.0, append-only, each with a
`### Confirmation`). Evidence goes in `docs/research/YYYY-MM-DD-<topic>.md`.
