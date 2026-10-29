# Architecture Decision Records

Decisions that outlive the change that prompted them are recorded here, in
[MADR 4.0](https://adr.github.io/madr/) with two local rules:

1. **Supersession is append-only.** An accepted record's `status:` is never flipped. A new record
   carries `supersedes: NNNN` in its front matter, and the table below shows the current view. Files
   are never renamed and numbers are never reused. A record's own history is appended, dated, under
   *More Information*.
2. **Every record says how it is checked.** A `### Confirmation` section names the test, CI job or
   script that fails when the decision is not followed. A decision that can change carries
   `review-by:` and names what would reopen it.

Evidence stays out of the record: research goes in `docs/research/YYYY-MM-DD-<topic>.md` and is linked.

## Records

| No. | Decision | Status |
| --- | --- | --- |
| [0001](0001-the-slack-message-is-the-only-store.md) | The Slack message is the only store (state schema version 1) | accepted |
| [0002](0002-expressions-are-read-with-the-metricsql-parser.md) | A rule's expression is read with the MetricsQL parser, not by hand | accepted |
| [0003](0003-one-replica-with-in-memory-locks.md) | One replica, with the per-incident lock in memory | accepted |
| [0004](0004-everything-site-specific-is-configuration.md) | Everything site-specific is configuration, links are templates | accepted |
| [0005](0005-semantic-release-cuts-tags-and-goreleaser-publishes-them.md) | semantic-release cuts tags; GoReleaser, ko and Helm publish them, signed keylessly | accepted |
| [0006](0006-go.md) | fogbell is written in Go | accepted |
