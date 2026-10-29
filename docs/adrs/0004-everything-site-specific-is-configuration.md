---
status: "accepted"
date: "2026-10-29"
review-by: "2027-04-29"
---

# Everything site-specific is configuration, links are templates

## Context and Problem Statement

The relay fogbell was ported from had its estate built in: its Grafana host and datasource ids, its
Git host, its chat model, its channel names, the labels that pick an icon. A project anyone can run
must name none of that. How does a site say where a pod's logs are, or what icon an audit alert gets?

## Decision Drivers

- The project names no estate, company or person that uses it.
- A site's links are arbitrary URLs: a Grafana Explore pane with JSON in its query string, a Kibana
  search, a Git host.
- One file, checked before anything starts.

## Considered Options

- Go templates in the configuration, with a few escaping functions.
- Fixed integrations (a "Grafana" block with host and datasource id).
- Hooks: an external program or webhook that returns the links.

## Decision Outcome

Chosen option: "Go templates", because a template can build any URL a site uses, and fixed
integrations would each need code for one vendor's URL scheme.

- `objects` lists the kinds of broken object in order, each with the labels that name it, regexes
  that exclude it, a button label and a URL template. The defaults, `pod` and `node`, have no links.
- `links.allAlerts`, `links.ask`, `incidents.icons`, the channel names, the team label, the fold
  parent, the Flux commit and path URLs and the ClickUp list are configuration.
- Unknown keys are refused, and every problem is reported at once (`fogbell -check`).

### Consequences

- Good, because no URL scheme is baked in.
- Bad, because a template is code a site writes; a broken one fails `fogbell -check`, a wrong one
  shows as a wrong link.

### Confirmation

- `internal/config` tests refuse unknown keys and report every broken field at once.
- `scripts/verify.sh` checks [examples/config.yaml](../../examples/config.yaml) and the chart's
  rendered configuration with `fogbell -check`.
- The repository's agent instructions forbid naming a user of the project.

## More Information

- 2026-10-29: accepted with the v0.1 scope.
