# Integrations

fogbell talks to other systems through four seams. Each is an interface in the code with one or more
implementations, so a new system is an adapter, not a change to the engine
([ADR-0008](adrs/0008-integrations-are-ports-with-adapters.md)).

| Seam | Interface | What ships | Direction |
| --- | --- | --- | --- |
| Context for a message | `enrich.Enricher` | sparkline and chart (Prometheus-compatible API, vmalert or Prometheus ruler), logs (VictoriaLogs), the change before the alert (Flux), what else fires (Alertmanager), the rule's last week (Slack history) | fogbell reads |
| Tickets | `engine.Tracker` | ClickUp | fogbell writes, on a press |
| Investigations | `investigate.Investigator` | Ploeg | fogbell writes, on a press, and reads the findings back |
| Everything that happens | `events.Sink` | CloudEvents webhooks | fogbell writes, on every step |

## Investigations with Ploeg

**🔎 Investigate** hands the incident to an agent team that looks and reports, and posts what it
found in the thread. With [Ploeg](https://github.com/ploeg-hq/ploeg):

1. The press admits a manual-origin Work Item with `POST /api/v1/operator/work-items` (Ploeg
   ADR-0102) to `investigate.ploeg.team` on `investigate.ploeg.target`, with a budget. The brief
   carries the alert, its labels and contract fields, the thread link and every line fogbell already
   looked up, and tells the agent not to change anything. The request id is derived from the group
   key and the firing, so a repeated press admits nothing twice.
2. Every maintenance round reads `GET /api/v1/operator/work-items/{id}/facts` until the Work Item is
   `done`, `needs_human`, `awaiting_review`, `stale` or `withdrawn`, then posts the last Run's
   summary, cause and fix in the thread, marked as written by an agent, and turns the button into a
   link (`investigate.ploeg.console`, for example an Unfold page, or the tracker URL Ploeg reports).
3. A refusal (402, a spent allowance) or an error is said in the thread.

What the agent may read is Ploeg's business, not fogbell's: the team's plan, its roles and their
read-only credentials decide that. fogbell needs an operator-consumer token with execute permission
for that one team, mounted from a Secret (`investigate.ploegSecret` in the chart).

Unfold sits on Ploeg and shows the same Work Item to people; fogbell links to it and needs nothing
from Unfold itself.

## Events

Every step is a [CloudEvent](https://cloudevents.io/) (specversion 1.0, structured mode) posted to each
`events.webhooks[].url`: `dev.fogbell.incident.opened`, `.updated`, `.acknowledged`, `.silenced`,
`.unsilenced`, `.resolved`, `.reopened`, `.ticketed` and `.investigated`. `subject` is the group key;
`data` is the thread (channel, ts, permalink), who pressed, and the full [incident
state](design.md#the-state-lives-on-the-message). With `secretFile`, the body is signed:
`X-Fogbell-Signature: sha256=<hex HMAC-SHA256 of the body>`. `types` limits a webhook to some steps.

Delivery is fire-and-forget: a slow or failing receiver is logged and never holds up an incident.

## What else plugs in

These are not built; each is a small adapter on a seam above.

| Idea | Seam | Notes |
| --- | --- | --- |
| A Vikunja, Forgejo or GitHub issue as the ticket | `Tracker` | The same `Create` and `Member` shape as ClickUp |
| Ploeg Asks instead of Work Items for a quick, cheaper look | `Investigator` | An Ask is client-carried: fogbell would run the agent itself with the key Ploeg returns (Ploeg ADR-0082) |
| An MCP server: list open incidents, acknowledge, silence | a new inbound seam | Lets an agent session act on incidents with the same rules as the buttons |
| Deploys from a pipeline as the change before an alert | `Enricher` | Ploeg's `POST /api/v1/deploys` already collects them |
| Grafana annotations for opened, acknowledged and resolved | `events.Sink` | One adapter instead of a webhook relay |
| Mattermost or Matrix instead of Slack | the chat client | The largest change: Block Kit and message metadata are Slack's |
