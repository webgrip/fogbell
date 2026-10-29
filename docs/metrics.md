# Metrics

`GET /metrics` serves Prometheus text. Besides the Go and process collectors:

| Metric | Labels | What |
| --- | --- | --- |
| `fogbell_notifications_total` | `result` | Webhooks handled: `posted`, `updated`, `resolved`, `reopened`, `ignored-resolve`, `nested…`, `error` |
| `fogbell_last_success_timestamp_seconds` | | When a webhook was last handled without an error |
| `fogbell_slack_requests_total` | `method`, `result` | Slack API calls; `result` is `ok` or Slack's error code |
| `fogbell_interactions_total` | `action` | Button presses by action; `rejected` is a bad signature, `error` a failure |
| `fogbell_interactive` | | 1 while a signing secret is present |
| `fogbell_incidents_total` | `alertname`, `severity`, `team` | Incidents posted or reopened |
| `fogbell_ack_seconds` | `alertname`, `severity`, `team` | Histogram: firing shown to the first I'm on it |
| `fogbell_resolve_seconds` | `alertname`, `severity`, `team` | Histogram: firing shown to resolve |
| `fogbell_noise_votes_total` | `alertname` | Noise votes |
| `fogbell_folded_total` | `alertname` | Groups folded into a parent's thread |
| `fogbell_escalations_total` | `alertname` | Pages escalated to the person on call |
| `fogbell_refreshes_total` | `result` | Redraws: `same` or `edited` |
| `fogbell_enrichment_total` | `kind`, `result` | Lookups by kind (`spark`, `history`, `related`, `change`, `logs`, `chart`) and result (`ok`, `none`, `skipped`, `disabled`, `forbidden`, `error`, `timeline`) |
| `fogbell_degraded_total` | `feature` | Fallbacks: `team_channel`, `reason_modal`, `thread_read`, `silence_overview`, `roster` |
| `fogbell_history_unavailable_total` | | Channel history reads that failed |

`fogbell_interactions_total{action="rejected"}`, `{action="error"}` and
`fogbell_notifications_total{result="error"}` exist at zero from the start, so `increase()` sees their
first step.

## Alerts worth having

```yaml
- alert: FogbellNotificationsFailing
  expr: increase(fogbell_notifications_total{result="error"}[15m]) > 0
  for: 15m
- alert: FogbellInteractionsRejected
  expr: increase(fogbell_interactions_total{action="rejected"}[1h]) > 3
- alert: FogbellNotInteractive
  expr: fogbell_interactive == 0
  for: 1h
```

An alert on fogbell is delivered by fogbell: route the first one to a receiver that does not go
through it.
