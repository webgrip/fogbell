# fogbell documentation

- [README](../README.md): what fogbell is and the quickstart.
- [Configuration](configuration.md): every field, its default, and the link templates.
- [Integrations](integrations.md): enrichers, tickets, Ploeg investigations, CloudEvents, and what
  else plugs in.
- [The Slack app](slack-app.md): the manifest, the scopes and what each one buys.
- [Design](design.md): the incident lifecycle, the state on the message, and what runs when.
- [Taking over existing threads](adopting.md): moving from another relay without orphaning its open
  incidents.
- [Metrics](metrics.md): what fogbell exposes and the alerts worth having on it.
- [Releasing](releasing.md): branches, tags, and how to verify a download or the image.
- [Decisions](adrs/README.md): the ADR ledger.
- [Research](research/2026-10-29-landscape.md): what else does this, and where fogbell fits.

## Layout

| Path | What |
| --- | --- |
| `cmd/fogbell` | Reads flags and calls `internal/app` |
| `internal/config` | Reads and checks the configuration, compiles the link templates |
| `internal/alert` | The Alertmanager webhook and the broken object each alert is about |
| `internal/state` | The incident state stored on a Slack message (schema version 1) |
| `internal/render` | The message: headline, lines, state and enrichment, and the row of buttons |
| `internal/app` | Builds every client, the engine and the server from the configuration |
| `internal/engine` | The lifecycle (`Decide`), folding, the action registry, maintenance tasks, digest and noise report |
| `internal/store` | The incidents: the index over the Slack messages, the scans and the per-incident locks |
| `internal/enrich` | Each lookup as an `Enricher`: spark, chart, history, related, Flux change, logs |
| `internal/investigate` | The `Investigator` port and the Ploeg adapter |
| `internal/events` | The `Sink` port and the CloudEvents webhook |
| `internal/slack` | The Slack Web API calls fogbell makes, and the request signature check |
| `internal/promql` | Reads a rule's expression with the MetricsQL parser |
| `internal/spark`, `internal/chart` | The sparkline text and the PNG chart |
| `internal/upstream` | Alertmanager, the query API, the ruler, VictoriaLogs, Kubernetes, ClickUp |
| `internal/server` | The HTTP routes |
| `internal/slacktest` | An in-memory Slack for tests |
| `deploy/charts/fogbell` | The Helm chart |
| `testdata/alerts` | Alertmanager webhook fixtures |
