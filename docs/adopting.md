# Taking over existing threads

fogbell finds its open incidents by the metadata event type on its messages. A relay that ran
before it, with the same state fields, wrote its own event type. If fogbell looks only for
`fogbell_incident`, it does not see those threads: their incidents get a second message on the next
notification, and the old messages never close.

Two ways to take them over without that:

1. **Keep the old event type.** Set `slack.events.incident` to the type the old relay wrote. Nothing
   on the messages changes.
2. **Adopt it.** Keep fogbell's default and list the old type under `slack.adoptEvents`. fogbell reads
   both; the first edit of an old message rewrites its metadata as `fogbell_incident`. Remove the
   entry after `incidents.historyWindow` has passed, by which time no open thread carries it.

```yaml
slack:
  adoptEvents: [previous_relay_alert]
```

The payload must be [schema version 1](design.md#the-state-lives-on-the-message). Fields fogbell does
not know are dropped on the next edit; fields it knows but the old relay did not write read as zero.

## The cutover

1. Freeze the old relay: fixes only, no new features.
2. Deploy fogbell beside it, with no Alertmanager receiver pointing at it yet, and check its
   configuration with `fogbell -check`.
3. Point the receivers at fogbell and scale the old relay to zero in the same change. Two relays
   posting the same group race on its message.
4. Move the Slack app's interactivity request URL to fogbell. A button pressed between steps 3 and 4 reaches
   nothing; buttons work again once the URL points at fogbell.
5. Replace alerts on the old relay's metrics with alerts on fogbell's ([metrics](metrics.md)).
