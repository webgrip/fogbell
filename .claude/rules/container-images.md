---
paths:
  - "**/Dockerfile*"
---

# Container images

- Pin the `# syntax=` frontend and every base image by tag and digest (`name:tag@sha256:<digest>`), with a `# renovate:` line above an `ARG` that holds it, because the digest makes the build reproducible and the tag is what Renovate moves.
- Verify every file a build downloads against a checksum pinned next to its version (`ADD --checksum=` or `sha256sum -c`), because a release asset can change under a fixed URL.
- Give a runtime image a non-root `USER`, because the clusters' pod security policies refuse or report a container that runs as root.
- Keep build-only tools in an earlier stage and copy only the artefacts into the final one, because every extra package in the final image is attack surface the CVE gate counts.
