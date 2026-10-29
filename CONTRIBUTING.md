# Contributing to fogbell

## Licence of what you send

fogbell's code is [Apache-2.0](LICENSE) and its documentation is
[CC-BY-4.0](LICENSES/CC-BY-4.0.txt); [REUSE.toml](REUSE.toml) says which covers each file. A
contribution comes in under the licence of the file it changes (inbound equals outbound, section 5 of
the Apache licence). There is no CLA and no copyright assignment.

Do not contribute code you do not have the right to license. In particular, do not port code from
AGPL or other copyleft incident tools; write it from the Slack and Alertmanager API documentation.

## Developer Certificate of Origin

Sign off every commit with `git commit -s` to certify the
[Developer Certificate of Origin](https://developercertificate.org/). Do not sign off on another
contributor's behalf. The `dco` CI job checks every commit in a pull request; fix a missing sign-off
with `git rebase --signoff development`.

## Workflow

1. Install [mise](https://mise.jdx.dev/), then run `mise run setup` and `mise run verify`. They need no
   credentials.
2. Open pull requests against `development`, with Conventional Commit messages. `feat` and `fix`
   cut a release candidate when merged.
3. A change to the state stored on Slack messages, or another decision that outlives the change,
   needs an ADR in `docs/adrs/`. A change to what a message says changes its tests on purpose.
4. Read [AGENTS.md](AGENTS.md) for the repository's conventions.

Report bugs and ideas in [GitHub Issues](https://github.com/webgrip/fogbell/issues) and
vulnerabilities as [SECURITY.md](SECURITY.md) says. Everyone taking part follows the
[code of conduct](CODE_OF_CONDUCT.md).
