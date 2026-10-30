# Security

fogbell is alpha software. Only the latest release is supported.

## Report a vulnerability

Report it privately through GitHub:
[open a draft security advisory](https://forgejo.webgrip.dev/webgrip/fogbell/issues/new).
Do not put credentials, exploit details or private deployment data in a public issue or pull request.

A [maintainer](MAINTAINERS.md) aims to acknowledge a report within seven days, agree on severity with
you, fix it, and publish an advisory with credit once a fixed release is out. These are targets, not
guarantees, while the project has one maintainer.

## What fogbell handles

fogbell holds a Slack bot token, a Slack signing secret and, optionally, a ClickUp token, each read
from a file the configuration names. It never logs them or puts them in a URL.

- `POST /slack/interactions` is the only path meant to be public. fogbell acts on a request only when
  its Slack signature (HMAC-SHA256 with the signing secret, at most five minutes old) verifies.
  Without a signing secret the buttons that act are hidden and every interaction is refused.
- `POST /alertmanager` posts to every channel the app is in. Keep it reachable from Alertmanager
  only, for example with a NetworkPolicy.
- Log lines posted into a thread pass a redaction of obvious secrets first. That is not a guarantee:
  a pod that logs a credential in an unknown shape still leaks it, which is why fogbell posts only a
  few lines of one pod.
- With `flux.rbac`, the ServiceAccount can get and list Flux Kustomizations and HelmReleases, and
  nothing else.

Releases are signed keylessly; [releasing](docs/releasing.md) shows how to verify a download or the
image.
