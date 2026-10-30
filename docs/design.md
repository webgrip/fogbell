# Design

fogbell is one process, one replica, no database. Alertmanager is the source of truth for what
fires; the Slack message is the source of truth for the incident around it.

## One message per notification group

An incident is keyed by Alertmanager's `groupKey`. A notification for a key:

| The key's latest message | The notification | fogbell |
| --- | --- | --- |
| none | firing | posts a message (`@channel` for a page receiver), then enriches it |
| open | firing | edits it in place and replies with what changed (`➕ 2 more firing`) |
| open | resolved | closes it with ✅ and replies with the duration |
| resolved within `reopenWindow` | firing | reopens it, counts the re-fire, clears the acknowledgement and silence |
| resolved longer ago | firing | posts a new message |
| none or resolved | resolved | ignores it |

Before posting, a pod alert on a node whose parent incident (by default `KubeNodeNotReady`) is open
and was shown within `fold.window` is folded into that thread as a sub-line. Its later notifications
stay there until the parent resolves.

Work that waits on upstreams (logs, sparkline, history, neighbours, the Flux change, the chart) runs
after the first post, beside the webhook Alertmanager is waiting on, and edits the message once. Each
lookup is an enricher; one runner counts its outcome and never lets it fail the incident
([ADR-0008](adrs/0008-integrations-are-ports-with-adapters.md)).

`engine.Decide` is the table above as a pure function; one function applies its step.

## The state lives on the message

Every incident message carries Slack message metadata: an event type (`slack.events.incident`) and a
flat payload, schema version 1 ([ADR-0001](adrs/0001-the-slack-message-is-the-only-store.md)):

| Field | What |
| --- | --- |
| `group_key`, `state`, `count` | The key, `firing` or `resolved`, the active alerts |
| `alertname`, `severity`, `receiver`, `team` | From the common labels |
| `group_labels`, `objects`, `fields`, `children` | JSON strings: the group labels, the objects a silence can narrow to, the contract annotations, folded child groups and their counts |
| `started_at`, `posted_at`, `fired_at`, `resolved_at` | Unix seconds; `fired_at` is the latest showing, which time to acknowledge and to resolve count from |
| `flaps`, `week_count` | Firings within the reopen window; incidents of this key in the channel this week |
| `acked_by`, `noise_by`, `noise_votes` | Slack user ids |
| `silence_id`, `silence_until`, `silence_by`, `silence_reason`, `silence_at`, `expiry_warned` | The silence made from the message |
| `escalated`, `refreshed_at`, `ticket_url`, `ticket_id` | Maintenance bookkeeping and the ticket |
| `spark`, `history`, `related`, `change` | The enrichment lines, kept so a re-render does not drop them |

After a restart fogbell reads each alert channel's history of `historyWindow` once and rebuilds its
index from these payloads, the newest message per key winning. The group itself (the alerts) is in
memory only; Alertmanager repeats a firing group, which brings it back.

## Buttons

Slack shows five elements of an attachment's action row and folds the rest into "+N more", where the
buttons that act would hide. The row is built to five: the object link, the runbook, **I'm on it**,
**Silence** first, the rest in one ⋯ menu.

A press is answered within Slack's three seconds and handled after. **Silence** first opens a "why?"
modal (a trigger lives three seconds, so this is the first call); the reason becomes the silence's
comment and a thread line. When Slack refuses the modal, fogbell silences without a reason. A
silence names its matchers: the group's labels, narrowed to one object when the person picks it.

Every press re-reads the message under the incident's lock, so two people pressing at once make one
acknowledgement, one silence and one ticket.

## Time-driven work

Every two minutes, for every open incident:

- a silence made from the message that ends within the hour (ten minutes for a one-hour silence)
  while the alert still fires gets a warning line, once;
- a page nobody picked up or silenced within the roster's minutes mentions the person on call, once
  per firing;
- a firing incident's sparkline and also-firing line are redrawn every `enrich.refresh`, and the
  message is edited only when one changed;
- an investigation started from the message is read until it finishes, and its findings are posted
  in the thread once ([integrations](integrations.md#investigations-with-ploeg)).

At `schedule.digestAt` each digest channel gets the last 24 hours; on `noiseReportDay` the noise
report ranks the rules (three per noise vote, one per re-fire, one per short-lived firing). Both are
deduplicated by their own metadata, so a restart in the window does not post twice.

## Failure

| What fails | fogbell |
| --- | --- |
| Slack refuses a post or edit | 500 to Alertmanager, which retries |
| A team channel the app is not in | posts to the receiver's channel, counted |
| Any enrichment upstream | skips that line, counted in `fogbell_enrichment_total` |
| Alertmanager while making a silence | says so in the thread |
| History unreadable | keeps incidents in memory, counted |
| The investigator refuses or fails | says so in the thread |
| An event webhook | logs it; the incident goes on |

## Why one replica

The per-incident lock is in memory ([ADR-0003](adrs/0003-one-replica-with-in-memory-locks.md)). Two
replicas would race on the same message. A restart costs a delayed message, never a lost one.
