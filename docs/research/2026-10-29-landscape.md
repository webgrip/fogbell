# What else does this, 2026-10-29

Collected before the first commit, to place fogbell. Verdict: no CNCF project turns Alertmanager
notifications into one interactive Slack thread per incident. Alertmanager is adding the threading;
the projects that act on incidents are platforms with their own UI and database.

## Alertmanager itself

- `update_message` edits the first message of a group in place instead of posting a new one
  (introduced in [prometheus/alertmanager#5115](https://github.com/prometheus/alertmanager/pull/5115);
  the release that shipped it is not checked here).
- `post_updates_to_thread` was merged on 2026-09-29
  ([#5540](https://github.com/prometheus/alertmanager/pull/5540)) and is not in a release yet; v0.34.1
  rejects the field. With both set, the root message is updated and each change is a thread reply. The
  root's timestamp lives in Alertmanager's notification log, so it survives restarts and works in a
  cluster. It needs the bot-token flow, not an incoming webhook.
- An open follow-up copies the first message into the thread
  ([#5622](https://github.com/prometheus/alertmanager/pull/5622)).
- None of it acts: no acknowledgement, no silence from Slack, no escalation, no enrichment, no digest.

So the part of fogbell that is one message with a thread becomes free in Alertmanager. What fogbell
adds is everything that acts on the incident or explains it.

## Platforms

| Project | What it is | Status |
| --- | --- | --- |
| [Grafana OnCall OSS](https://grafana.com/blog/grafana-oncall-maintenance-mode/) | On-call, escalation, Slack acknowledgements | Maintenance mode from 2025-03-11, archived 2026-03-24; development continues in Grafana Cloud IRM only |
| [Keep](https://github.com/keephq/keep) | AIOps alert management: deduplication, correlation, workflows, its own UI | Open source with an enterprise tier; joined forces with Elastic in May 2025; current activity not checked |
| [Robusta](https://docs.robusta.dev/master/configuration/alertmanager-integration/outofcluster-prometheus.html) | Kubernetes troubleshooting automations; proxies Alertmanager alerts to Slack with enrichment and AI investigation | Open source, with a SaaS UI |

Each of these is a system to run: a database, a UI, workers. fogbell is one binary whose state is
the Slack messages.

## Where fogbell fits

- Next to Alertmanager, never instead of it: routing, grouping, inhibition and silences stay there.
- For a team that lives in Slack and wants the incident there: who is on it, what is silenced and
  why, what changed before it, what else is firing, and which rules are noise.
- Not an on-call product: the roster is a list in a file, and paging a phone stays with a paging
  service.

## Re-evaluate when

- Alertmanager ships interactive Slack actions (acknowledge or silence from a button).
- Slack message metadata changes its limits or retention.
