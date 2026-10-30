# Governance

fogbell has one maintainer today. This page says how decisions are made now; it changes, by its own
rules, as people join.

## Roles

| Role | Who | What they can do |
| --- | --- | --- |
| Contributor | Anyone who opens an issue, a pull request or a review | Propose and review anything |
| Maintainer | Listed in [MAINTAINERS.md](MAINTAINERS.md) | Merge, release, and hold the project's accounts |

Maintainers are also the security response team ([SECURITY.md](SECURITY.md)) and the code of conduct
responders ([CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)).

## How decisions are made

| Decision | Where | Who decides |
| --- | --- | --- |
| A code or documentation change | A pull request against `main` | A maintainer who did not write it. While there is one maintainer, that maintainer merges their own change once CI passes. |
| A change to the stored state schema or another architectural decision | A new ADR in [docs/adrs/](docs/adrs/README.md), proposed in a pull request | Maintainers by lazy consensus over seven days |
| A stable release | A `feat` or `fix` merged to `main` | A maintainer |
| Adding a maintainer | A public nomination issue, then a pull request to MAINTAINERS.md | The maintainers, once the nomination has been open seven days |
| Changing this page | A pull request open for at least seven days | The maintainers |

Lazy consensus: a proposal stands when no maintainer objects during its comment period. With two or
more maintainers, any maintainer may call a vote instead; a majority of maintainers decides.

## Companies and users

Anyone may use, sell, host or support fogbell under the [Apache-2.0 licence](LICENSE). fogbell names
no estate, company or person that uses it in its code, documentation or defaults, and adds no feature
only one user can use. A pull request made for an employer or client says so.
