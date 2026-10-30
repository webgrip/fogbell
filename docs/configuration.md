# Configuration

fogbell reads one YAML file, `-config` or `$FOGBELL_CONFIG` (default `/etc/fogbell/config.yaml`). An
unknown key is an error, and every problem in the file is reported at once. Check a file without
starting anything:

```sh
fogbell -check -config config.yaml
```

Durations are Go durations with `d` for days: `90s`, `30m`, `1h`, `8d`. A full example is
[examples/config.yaml](../examples/config.yaml).

## Top level

| Field | Default | What |
| --- | --- | --- |
| `listen` | `:8080` | Address of the HTTP server |
| `alertmanager.url` | required | Where silences are made, and firing alerts read for the also-firing line |

## `slack`

| Field | Default | What |
| --- | --- | --- |
| `tokenFile` | required | File holding the bot token (`xoxb-…`) |
| `signingSecretFile` | | File holding the signing secret. Read on every request: without it the buttons that act are hidden and interactions refused, and they appear once the file does |
| `apiURL` | `https://slack.com/api/` | The Web API base |
| `events.incident` | `fogbell_incident` | Metadata event type of incident messages |
| `events.digest` | `fogbell_digest` | Metadata event type of digests |
| `events.noiseReport` | `fogbell_noise_report` | Metadata event type of noise reports |
| `adoptEvents` | `[]` | Further event types read as incidents; see [adopting](adopting.md) |

## `channels`

| Field | Default | What |
| --- | --- | --- |
| `alerts` | `[]` | Every channel a webhook posts to. Maintenance and the noise report look across these |
| `digest` | `[]` | Channels that get the daily digest |
| `noiseReport` | | Channel of the weekly noise report; empty turns it off |
| `silenceOverview` | | This channel's digest also lists silences made outside Slack (amtool, the Alertmanager UI) |
| `teams` | `{}` | `team: channel` for a receiver whose webhook URL carries `&by_team=1`. A team's channel the app is not in falls back to the receiver's channel |
| `teamLabel` | `team` | The label that names the team, also shown in the headline |

Team channels count as alert and digest channels.

## The webhook

Each Alertmanager receiver posts to `POST /alertmanager?channel=<name>`, with `send_resolved: true`.
Add `&by_team=1` to route by `teamLabel`. fogbell answers 200 with what it did (`posted`, `updated`,
`resolved`, `reopened`, `nested`, …) and 500 when Slack refused, so Alertmanager retries.

## `incidents`

| Field | Default | What |
| --- | --- | --- |
| `pageReceivers` | `[page]` | Receivers whose first post mentions `@channel` and that escalate to the person on call |
| `reopenWindow` | `1h` | A group that fires again within this reopens its message instead of posting a new one |
| `historyWindow` | `8d` | How far back open incidents are looked for after a restart |
| `shortLived` | `10m` | A firing that resolves by itself within this counts as noise in the report |
| `notObjectLabels` | rule and route labels | Labels never taken as the broken object when reading a `subject` annotation |
| `quietAlerts` | `[Watchdog, InfoInhibitor]` | Alerts that fire by design, never named as neighbours |
| `icons` | `[]` | `{match: {label: regex}, icon: "🔐"}`; the first rule whose anchored regexes all match the common labels wins, else the severity's icon |
| `fold.parentAlertname` | `KubeNodeNotReady` | Pod alerts on a node with this incident open go into its thread. Empty turns folding off |
| `fold.label` | `node` | The label that names the node |
| `fold.window` | `30m` | How long after the parent was shown it adopts new groups |

## `objects`

How an alert names the object that is broken, tried in order. The first kind whose labels are all
present, and whose `exclude` regexes match none, is the object. It gives the alert's subject (the
label values joined by `/`), the labels of a silence narrowed to it, and the link on its button.

| Field | What |
| --- | --- |
| `name` | The kind. `pod` is special: log lines and folding apply to it |
| `labels` | Labels that must all be present |
| `exclude` | `label: regex`; a match means the alert is not about this kind (an exporter's own pod, for example) |
| `button` | Button label, default `Open` |
| `url` | A [Go template](https://pkg.go.dev/text/template) for the link |

The default is a `pod` (`namespace`, `pod`) and a `node` (`node`), without links. A `source_url`
annotation on the alert always wins over a template, and a `subject` annotation over the derived
subject.

A template reads `.Labels`, `.Annotations`, `.Kind`, `.Value` (the label values joined by `/`),
`.Start` (Unix seconds), `.StartMs` and `.FromMs` (an hour before the start, in milliseconds), and
can call `queryescape`, `pathescape` and `quote`.

## `links`

| Field | What |
| --- | --- |
| `allAlerts` | Link shown when an incident has no object link |
| `ask` | Template of a link that opens a chat with a question about the alert prefilled; reads `.Prompt` and `.Labels`. Shown while the incident fires |
| `askLabel` | Its button label, default `Ask in chat` |

## `schedule`

| Field | Default | What |
| --- | --- | --- |
| `timezone` | `UTC` | IANA zone of the schedule and the roster; the zone database is built in |
| `digestAt` | `09:00` | When the digest is posted, within ten minutes |
| `noiseReportDay` | `Monday` | The noise report's weekday, at `digestAt` |

## On call

`onCall.rosterFile` names a YAML (or JSON) file, read on every check so it can change without a
restart; `onCall.handoverAt` (default `09:00`) is when the week changes hands, in the schedule's zone.

```yaml
people: [U012ABCDEF, U034GHIJKL]   # Slack user ids, a week each, in this order
rotation_start: "2026-11-02"        # the first person's first day
escalate_after_minutes: 15          # a page nobody picked up or silenced this long is escalated
```

## `enrich`

Every lookup is optional, read-only and never fails an incident.

| Field | What |
| --- | --- |
| `metrics.url` | A Prometheus-compatible query API. Turns on the sparkline, the firing timeline and folding by `kube_pod_info` |
| `ruler.url` | A vmalert API, to read a rule's expression from its generator URL. A Prometheus generator URL carries the expression itself and needs no ruler |
| `ruler.generatorHost` | Regex the generator URL's host must match before the ruler is asked, when several rulers share Alertmanager |
| `logs.url` | A VictoriaLogs API, for the last log lines of a pod incident |
| `logs.namespaceField`, `podField`, `containerField` | Its stream fields, default `k8s_namespace`, `k8s_pod`, `k8s_container` |
| `related` | Name other alerts firing in the same namespace or on the same node |
| `charts` | Upload a PNG chart into the thread (needs `files:write`) |
| `flux.enabled` | Name the Flux change applied into the alert's namespace shortly before it started (needs the chart's `flux.rbac`) |
| `flux.window` | How long before the alert a change counts, default `1h` |
| `flux.commitURL` | Prefix a commit sha is appended to, for a change from `flux.ownSource` |
| `flux.pathURL` | Prefix a Kustomization path is appended to, when the commit cannot be known |
| `flux.ownSource` | The GitRepository whose commits the links point at, default `flux-system` |
| `refresh` | How often a firing incident's sparkline and also-firing line are redrawn, default `5m` |

## `tickets.clickup`

| Field | What |
| --- | --- |
| `tokenFile` | File holding a ClickUp API token; the button is hidden while it is empty or missing |
| `list` | The list id tickets are created in |

The ticket is assigned to the ClickUp member whose address matches the Slack user who pressed (needs
`users:read.email`).

## `investigate.ploeg`

| Field | What |
| --- | --- |
| `url` | Ploeg's API base; with it, the investigate button appears while the token file holds a token |
| `tokenFile` | File holding an operator-consumer token with execute permission for the team |
| `team` | The Ploeg team that investigates |
| `target` | The registered Work Target the team works on |
| `budgetUSD` | Each investigation's budget; default the consumer's maximum |
| `model` | A model alias every Run uses, when the team's policy lists it |
| `console` | Base of a person-facing link to a Work Item, such as an Unfold page; the id is appended |

See [integrations](integrations.md#investigations-with-ploeg).

## `events.webhooks`

A list; each entry receives every step as a CloudEvent.

| Field | What |
| --- | --- |
| `url` | Where events are posted |
| `secretFile` | File holding a secret; with it, `X-Fogbell-Signature` signs the body |
| `types` | Steps to send, default all: `opened`, `updated`, `acknowledged`, `silenced`, `unsilenced`, `resolved`, `reopened`, `ticketed`, `investigated` |
