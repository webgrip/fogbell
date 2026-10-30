---
paths:
  - "**/kubernetes/**"
---

# Kubernetes and Flux

## GitOps

- Change a manifest or values file instead of running `kubectl apply/patch/delete` or `helm install/upgrade`, because Flux reconciles Git and reverts or fights anything applied by hand; reading the cluster is fine.
- Call a rollout live only once the Flux revision it reconciled and the running image digest both match the commit, because a merged manifest or a green pipeline is not a running resource.
- Express order between applications with `dependsOn` on the Flux Kustomization or HelmRelease, because Flux applies independent Kustomizations in parallel.
- Annotate an object whose deletion takes the cluster or its data down (Flux itself, the CNI, a namespace holding volumes) with `kustomize.toolkit.fluxcd.io/prune: disabled`, because moving or deleting its file would otherwise uninstall it.
- Escape a runtime shell variable in a manifest under a Kustomization with `postBuild` substitution as `$${VAR}`, because Flux substitutes every `${...}` before the container sees it.
- Annotate a generated ConfigMap whose file payload contains `$` text with `kustomize.toolkit.fluxcd.io/substitute: disabled` instead of escaping it, because envsubst fails the whole build on an unbalanced brace.
- Template a private hostname or address through the substitution variables the repository defines, because the literal values are kept out of Git.

## Charts in HelmReleases

- Pin an `OCIRepository` `ref` to a tag and its digest, and a `HelmRepository` chart to an exact version, because Renovate moves both and a floating reference deploys a chart nobody reviewed.
- Give every HelmRelease install and upgrade remediation with retries, because a failed release otherwise stays failed until someone intervenes.
- Deploy a workload that has no chart of its own through the bjw-s `app-template` chart, because `application-template` and `common-charts` are archived ([homelab-cluster ADR-0078](https://forgejo.webgrip.dev/webgrip/homelab-cluster/src/branch/main/docs/techdocs/docs/adr/adr-0078-one-release-recipe-four-kinds.md)).

## Workloads

- Reference every container image as `tag@sha256:<digest>`, because the clusters' admission policies refuse or report a mutable reference.
- Run containers with `runAsNonRoot`, `allowPrivilegeEscalation: false`, all capabilities dropped and the `RuntimeDefault` seccomp profile, because the pod security policies refuse or flag anything less.
- Set CPU and memory requests and a memory limit on every container, because the clusters' admission policies refuse or report a container without them.
- Place a workload by the `node.webgrip.io/pool` label, never by hostname or the control-plane role, because pools are how nodes are grouped and nodes come and go.
- Give a workload in a default-deny namespace its own policy that allows only the flows it needs, because everything else is dropped silently.
