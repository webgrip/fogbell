---
status: "accepted"
date: "2026-10-30"
supersedes: "0005"
review-by: "2027-04-30"
---

# Forgejo leads, releases go to Harbor, and GitHub is a mirror

## Context and Problem Statement

[ADR-0005](0005-semantic-release-cuts-tags-and-goreleaser-publishes-them.md) released from GitHub
Actions to GHCR. fogbell is a webgrip project, and webgrip projects live on the webgrip Forgejo, are
built by its runners with the shared `webgrip/workflows`, publish to Harbor, and are push-mirrored to
GitHub. Where does fogbell live and release?

## Decision Drivers

- One way of building and releasing across webgrip repositories.
- A public copy for people who find it on GitHub.
- Signed images with an SBOM, as every webgrip image has.

## Considered Options

- Forgejo-leading, push-mirrored to `github.com/webgrip/fogbell`, released by Forgejo Actions to
  Harbor.
- GitHub-leading as in ADR-0005, with a pull mirror into Forgejo.

## Decision Outcome

Chosen option: "Forgejo-leading", because it is how the estate is built and the CI bot can push tags.

- `main` is the trunk. `.forgejo/workflows/on_source_change.yml` runs the shared Go static-analysis
  and test workflows, `mise run verify` and the image build, and on `main` the shared
  semantic-release composite in the semantic-release toolchain image; `.releaserc.cjs` uses the
  `@webgrip/semantic-release-config` preset, which publishes through the Gitea plugin on Forgejo.
- `.forgejo/workflows/on_release_published.yml` builds the image from the `Dockerfile` for amd64 and
  arm64, pushes `harbor.webgrip.dev/webgrip/fogbell:<version>`, signs and attests it with the shared
  cosign composite, and pushes the chart to `oci://harbor.webgrip.dev/webgrip/charts/fogbell`.
- The Go module path is `github.com/webgrip/fogbell`, the mirror, so `go install` works for anyone.
- GitHub carries no workflows: it is a copy.

### Consequences

- Good, because CI, release and signing match the other webgrip repositories.
- Bad, because the GoReleaser binaries of ADR-0005 are gone; fogbell is consumed as an image.
- Bad, because GitHub release objects do not mirror, only tags.

### Confirmation

The Forgejo workflows reference `webgrip/workflows` at one pinned release; the agents-check workflow
and `agentstd check` keep the agent files current.

## More Information

- 2026-10-30: accepted when the repository moved to Forgejo.
