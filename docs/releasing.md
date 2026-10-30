# Releasing

fogbell lives on [forgejo.webgrip.dev/webgrip/fogbell](https://forgejo.webgrip.dev/webgrip/fogbell),
is push-mirrored to [github.com/webgrip/fogbell](https://github.com/webgrip/fogbell), and is released
by Forgejo Actions ([ADR-0007](adrs/0007-forgejo-leads-and-releases-go-to-harbor.md)).

`main` is the trunk. semantic-release cuts a version from Conventional Commits on every push to it:
`feat` cuts a minor; `fix`, `perf`, `refactor`, `revert` and dependency bumps a patch; `docs`, `test`,
`ci` and `chore` nothing. Releases are tags only: nothing is committed back.

## Before the first release: the seed tag

semantic-release makes its first release `1.0.0` whatever the commits say. To start at `v0.1.0`, push a
`v0.0.0` tag on the root commit once, before the first push to `main` that should release:

```sh
git tag v0.0.0 "$(git rev-list --max-parents=0 HEAD)"
git push origin v0.0.0
```

## What a release publishes

`On Release Published` runs on the release semantic-release created:

1. builds the image from the `Dockerfile` for linux/amd64 and linux/arm64 and pushes
   `harbor.webgrip.dev/webgrip/fogbell:<version>` (and `:latest` for a stable version);
2. signs and attests it, with an SBOM, through the shared cosign composite;
3. pushes the chart at the same version to `oci://harbor.webgrip.dev/webgrip/charts/fogbell`.

A failed publish is rerun with the workflow's manual trigger and the tag.

## Verify the image

```sh
cosign verify harbor.webgrip.dev/webgrip/fogbell:0.1.0 \
  --certificate-identity-regexp '^https://forgejo\.webgrip\.dev/webgrip/fogbell/' \
  --certificate-oidc-issuer https://forgejo.webgrip.dev/api/actions
```

The identity and issuer above are the Forgejo OIDC defaults; the cosign composite's documentation in
`webgrip/workflows` is the authority if they differ.
