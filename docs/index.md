# fogbell documentation

- [README](../README.md): what fogbell is and the quickstart.
- [Configuration](configuration.md): every field, its default, and the link templates.
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
| `cmd/fogbell` | File-to-config wiring only |
| `internal/config` | Reads and checks the configuration, compiles the link templates |
| `internal/alert` | The Alertmanager webhook and the broken object each alert is about |
| `internal/state` | The incident state stored on a Slack message (schema version 1) |
| `internal/render` | The message: headline, lines, state and enrichment, and the row of buttons |
| `internal/engine` | The lifecycle, folding, enrichment, buttons, maintenance, digest and noise report |
| `internal/slack` | The Slack Web API calls fogbell makes, and the request signature check |
| `internal/promql` | Reads a rule's expression with the MetricsQL parser |
| `internal/spark`, `internal/chart` | The sparkline text and the PNG chart |
| `internal/upstream` | Alertmanager, the query API, the ruler, VictoriaLogs, Kubernetes, ClickUp |
| `internal/server` | The HTTP routes |
| `internal/slacktest` | An in-memory Slack for tests |
| `deploy/charts/fogbell` | The Helm chart |
| `testdata/alerts` | Alertmanager webhook fixtures |
