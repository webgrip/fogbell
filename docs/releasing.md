# Releasing

fogbell is versioned by [semantic-release](https://semantic-release.gitbook.io/) from Conventional
Commits ([ADR-0005](adrs/0005-semantic-release-cuts-tags-and-goreleaser-publishes-them.md)).

| Branch | Cuts | Example |
| --- | --- | --- |
| `development` (trunk; pull requests land here) | a release candidate on every releasable merge | `v0.1.0-rc.1` |
| `main` | a stable release when `development` is promoted to it | `v0.1.0` |

`feat` cuts a minor, `fix`, `perf`, `refactor`, `revert` and dependency bumps cut a patch; `docs`,
`test`, `ci` and `chore` cut nothing. Releases are tags only: nothing is committed back.

## Before the first release: the seed tag

semantic-release makes its first release `1.0.0` whatever the commits say. To start at `v0.1.0`, a
maintainer pushes a `v0.0.0` tag on the root commit once, before the first merge to `development`:

```sh
git tag v0.0.0 "$(git rev-list --max-parents=0 HEAD)"
git push origin v0.0.0
```

## What a release publishes

The `Release` workflow runs semantic-release and hands the new tag to `Publish`, which:

1. runs `mise run verify` on the tag;
2. builds `fogbell` for linux and darwin on amd64 and arm64 with GoReleaser, writes `checksums.txt`
   and an SPDX SBOM per archive, signs `checksums.txt` keylessly, and attaches everything to the
   GitHub release;
3. builds the image with ko on `cgr.dev/chainguard/static` for linux/amd64 and linux/arm64, with an
   SPDX SBOM, pushes it to `ghcr.io/fogbell-dev/fogbell:<version>` and signs it;
4. packages the chart at the tag's version, pushes it to `oci://ghcr.io/fogbell-dev/charts/fogbell`
   and signs it by digest.

A failed publish is rerun with the `Publish` workflow's manual trigger and the tag.

## Verify a download or the image

```sh
cosign verify-blob checksums.txt \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github.com/fogbell-dev/fogbell/\.github/workflows/publish\.yml@' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum --ignore-missing -c checksums.txt

cosign verify ghcr.io/fogbell-dev/fogbell:0.1.0 \
  --certificate-identity-regexp '^https://github.com/fogbell-dev/fogbell/\.github/workflows/publish\.yml@' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```
