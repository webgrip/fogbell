# fogbell

fogbell turns [Alertmanager](https://prometheus.io/docs/alerting/latest/alertmanager/) notifications
into one Slack message per incident. The message is posted once, edited in place as alerts come and
go, and closed with a ✅ when the group resolves. Every change, acknowledgement and silence is a reply
in its thread. Buttons on the message act on the incident: **I'm on it**, **Silence** for an hour, a
day or a week (with a reason), **Noise**, **Ticket**, and a postmortem draft once a critical incident
resolves.

**Status: v0.1, alpha.** The state schema on the messages is version 1 and stays readable by every
later release ([ADR-0001](docs/adrs/0001-the-slack-message-is-the-only-store.md)).

## Why

Alertmanager's own Slack notifier posts a new message for every notification. A busy group turns
into a wall of near-identical messages, and nothing in Slack says who is on it or whether it is
silenced. Alertmanager is learning to edit one message and thread its updates (`update_message`,
and `post_updates_to_thread` once it ships), which covers the threading. It does not act: no
acknowledgement, no silence from Slack, no escalation, no context.

The open-source tools that do act are platforms: Grafana OnCall OSS (archived in March 2026), Keep
and Robusta each bring their own UI and database. fogbell is one small binary with no database: the
incident's state lives on the Slack message itself, as message metadata, so a restart reads its open
incidents back from the channel. See [the landscape note](docs/research/2026-10-29-landscape.md).

## What a message shows

- The headline, the broken object linked to where you look at it (logs for a pod, a dashboard for a
  node, any URL template you configure), what is wrong and since when, and the first move.
- Who is on it, the silence and its end, re-fires within the hour, and noise votes.
- Looked up after the first post, each optional: a sparkline of what the rule measures, with its
  threshold, trend and the value a week ago; the rule's last week in this channel; what else fires
  in the same namespace or on the same node; the Flux change applied shortly before the alert; the
  pod's last log lines, with obvious secrets redacted; and a PNG chart in the thread.
- Pod alerts on a node whose not-ready incident is open fold into that incident's thread.

On a schedule: a daily digest per channel (open, resolved, flapping, silenced now) and a weekly noise
report that ranks the rules by noise votes, re-fires and short-lived firings. A page nobody picked up
escalates to the person on call from a roster.

## Quickstart

1. Create the Slack app from [the manifest](docs/slack-app.md), install it, invite it to your alert
   channels, and put its bot token and signing secret in a Secret:

   ```sh
   kubectl -n monitoring create secret generic fogbell-slack \
     --from-file=bot-token=./bot-token --from-file=signing-secret=./signing-secret
   ```

2. Install the chart with your Alertmanager and channels:

   ```sh
   helm install fogbell oci://ghcr.io/webgrip/charts/fogbell -n monitoring \
     --set config.alertmanager.url=http://alertmanager-operated:9093 \
     --set 'config.channels.alerts={alerts}'
   ```

3. Point an Alertmanager receiver at it:

   ```yaml
   receivers:
     - name: warnings
       webhook_configs:
         - url: http://fogbell.monitoring.svc:8080/alertmanager?channel=alerts
           send_resolved: true
   ```

4. Expose `POST /slack/interactions` (and only that path) publicly and set it as the app's
   interactivity request URL. fogbell checks Slack's signature on every request.

Every option is in [configuration](docs/configuration.md); a full example is
[examples/config.yaml](examples/config.yaml). Check a file with `fogbell -check -config <file>`.

## Develop

```sh
mise run setup    # pinned toolchain
mise run verify   # gofmt, vet, build, race tests, golangci-lint, helm lint, the chart's config, actionlint
```

Start at [docs/index.md](docs/index.md). Contributions: [CONTRIBUTING.md](CONTRIBUTING.md).

## License

Code: [Apache-2.0](LICENSE). Documentation: [CC-BY-4.0](LICENSES/CC-BY-4.0.txt).
