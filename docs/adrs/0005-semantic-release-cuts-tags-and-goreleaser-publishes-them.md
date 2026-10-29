---
status: "accepted"
date: "2026-10-29"
review-by: "2027-04-29"
---

# semantic-release cuts tags; GoReleaser, ko and Helm publish them, signed keylessly

## Context and Problem Statement

fogbell ships a container image, binaries and a Helm chart. Who decides the version, and how are the artifacts built,
signed and published?

## Decision Drivers

- Conventional Commits already say what kind of change each commit is.
- Release candidates come from trunk; stable releases are a deliberate step.
- Users verify what they download: checksums, SBOMs and signatures without a long-lived key.

## Considered Options

- semantic-release decides and tags; GoReleaser builds binaries and, with ko, the image on the tag;
  Helm packages the chart at the tag's version; cosign signs keylessly through GitHub OIDC.
- GoReleaser alone, with hand-pushed tags.
- release-please.

## Decision Outcome

Chosen option: "semantic-release decides and tags; GoReleaser and Helm publish", because the version
follows from the commits and the build follows from the tag.

- Branches: `development` is trunk and cuts `vX.Y.Z-rc.N` candidates; `main` cuts stable `vX.Y.Z`.
  `.releaserc.cjs` uses the shared `@webgrip/semantic-release-config` preset with `forge: 'github'`.
  Releases are tag-only: no version is committed back into `Chart.yaml` or `go.mod`.
- The first release starts from a `v0.0.0` seed tag on the root commit, so the first `feat` cuts
  `v0.1.0` rather than semantic-release's default `1.0.0` (see [releasing](../releasing.md)).
- `.github/workflows/release.yml` runs semantic-release on a push to `main` or `development` and calls
  `publish.yml` with the new tag: GoReleaser builds linux and darwin on amd64 and arm64, writes
  `checksums.txt` and an SPDX SBOM per archive, signs the checksums with `cosign sign-blob --bundle`,
  and attaches them to the GitHub release semantic-release created. ko builds the image on
  `cgr.dev/chainguard/static` for linux/amd64 and linux/arm64 with an SPDX SBOM, pushes it to
  `ghcr.io/fogbell-dev/fogbell` and cosign signs it. The chart is packaged at the tag's version,
  pushed to `oci://ghcr.io/fogbell-dev/charts/fogbell` and signed by digest. A Dockerfile is not
  needed: ko builds a static Go binary onto a distroless base.

### Consequences

- Good, because nobody types a version.
- Good, because a user verifies `checksums.txt` with its bundle, then each archive with the checksums.
- Bad, because a tag pushed with the workflow token does not trigger other workflows, so the publish
  job is called directly and a failed publish is retried by hand (`publish.yml`, workflow_dispatch).
- Bad, because a breaking change before v1 would cut v1.0.0; keep breaking changes out of commit
  headers until v1 is intended.

### Confirmation

- CI runs `goreleaser release --snapshot --skip=sign,publish,ko` on every change, so the release
  configuration builds before a tag needs it.
- `actionlint` passes on every workflow.

## More Information

- 2026-10-29: accepted with the v0.1 scope, following the sibling projects' release design.
