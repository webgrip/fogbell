# infra_scan.py and advisory_lookup.py

Contents: [Running the scanner](#running-the-scanner) · [Reading a finding](#reading-a-finding) · [Pod rules](#pod-rules) · [Runtime sandbox rules](#runtime-sandbox-rules) · [RBAC rules](#rbac-rules) · [Network and admission rules](#network-and-admission-rules) · [Secret and image rules](#secret-and-image-rules) · [CI rules](#ci-rules) · [Exemptions](#exemptions-and-why) · [Advisory lookup](#advisory-lookup)

## Running the scanner

Stdlib Python 3.10+, no network, no cluster access.

```bash
python3 scripts/infra_scan.py path/to/repo                  # walk a tree
kubectl kustomize apps/shop | python3 scripts/infra_scan.py -   # merged Kustomize output
helm template shop ./chart | python3 scripts/infra_scan.py -    # rendered chart
python3 scripts/infra_scan.py --rules                       # the rule list
```

`--json` for machine output, `--fail-on fail|warn|note|never` for CI (default `fail`; exit 1 at or above it). It reads YAML (Kubernetes manifests, Helm values, HelmRelease and Flux objects, CI workflows), TOML and `.conf` (containerd, Kata, gVisor config) and Dockerfiles. A file that will not parse is listed as skipped, never silently passed.

## Reading a finding

Each finding has a rule number, a **default** severity, `file:line` and a message. The default only orders the list. The priority you report is **exposure × blast radius**, set by hand:

| Exposure (who can reach the trigger) | Blast radius (what it reaches) | Priority |
| --- | --- | --- |
| Internet or any tenant | node, cluster or credentials of other workloads | P0 |
| Anyone who can create pods or open PRs | the namespace, its secrets, the CI token | P1 |
| Cluster admins or the owning team only | the workload itself | P2 |
| Nobody without already holding the impact | nothing new | P3 hardening |

A finding on a workload that is meant to be privileged (a CI daemon, a CNI agent, a node exporter) is still true. Record why it is accepted, who owns that decision and when it is reviewed, and check that the namespace is fenced off; do not call it a false positive.

## Pod rules

| # | Rule | Default | Fires when | Why it matters | Fix |
| --- | --- | --- | --- | --- | --- |
| K01 | `privileged-container` | fail | `privileged: true` anywhere in a manifest or values file | every capability and every host device; a container escape is no longer needed | drop it; grant the one capability or device needed |
| K02 | `host-namespace` | warn | `hostNetwork`, `hostPID` or `hostIPC` is true | the pod sees node traffic, node processes or shared memory, and NetworkPolicy no longer applies to host-network pods | remove; use a Service or a node agent with a narrow role |
| K03 | `hostpath-volume` | warn | a `hostPath` volume | node files in the pod; a writable mount of `/`, `/var/run`, `/var/lib/kubelet` or a runtime socket is node takeover | `emptyDir`, a CSI volume, or a read-only mount of one file |
| K04 | `privilege-escalation-allowed` | warn | `allowPrivilegeEscalation: true` | setuid binaries can gain root inside the container | `allowPrivilegeEscalation: false` |
| K05 | `security-context-incomplete` | note | a workload container lacks any of runAsNonRoot (or a non-zero runAsUser), `allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]`, `readOnlyRootFilesystem: true`, a seccomp profile | the Pod Security restricted profile, minus what admission may add | set them per container or at pod level; the message lists which are missing |
| K06 | `dangerous-capability-added` | warn | `capabilities.add` has ALL, SYS_ADMIN, SYS_PTRACE, SYS_MODULE, SYS_RAWIO, DAC_READ_SEARCH, NET_ADMIN, BPF or PERFMON | each is close to root on the node in at least one known escape class | the narrowest capability, or a node agent outside tenant reach |
| K07 | `default-token-automounted` | note | a pod with no service account mounts the default token | an API credential nobody asked for; any RBAC later bound to `default` reaches every such pod | `automountServiceAccountToken: false`, or a named account when the pod calls the API |

## Runtime sandbox rules

Sandboxed runtimes (Kata Containers, gVisor) read some of their configuration from pod annotations. Whoever can create a pod then controls part of the sandbox's own configuration, and published advisories show that this can reach the host. → [case-sandbox-annotations.md](case-sandbox-annotations.md)

| # | Rule | Default | Fires when | Why it matters | Fix |
| --- | --- | --- | --- | --- | --- |
| K09 | `sandbox-override-annotation` | warn | a pod template sets `io.katacontainers.*` or `dev.gvisor.*` annotations | a workload asks the runtime to change its own sandbox | remove; if one is truly needed, name it in the runtime's allowlist |
| K10 | `sandbox-annotation-passthrough` | warn | containerd `pod_annotations` or `container_annotations` forwards `*`, `io.katacontainers.*` or `dev.gvisor.*`; Kata `enable_annotations` is non-empty; gVisor allows flag overrides | the passthrough is what turns "can create pods" into "can configure the sandbox" | an empty or exact allowlist at the runtime, **and** an admission policy that denies the prefix on pods, proven two ways ([verify.md](verify.md)) |

## RBAC rules

| # | Rule | Default | Fires when | Why it matters | Fix |
| --- | --- | --- | --- | --- | --- |
| R01 | `rbac-wildcard` | warn (fail for `*` verbs on `*` resources in a ClusterRole) | `*` in verbs or resources | grants everything that exists, including resources added later | list verbs and resources |
| R02 | `rbac-secrets-read` | warn for ClusterRole, note for Role | get, list or watch on `secrets` without `resourceNames` | list returns every value in scope; it is credential access, not metadata | `resourceNames`, or let the controller own the one secret it needs |
| R03 | `rbac-dangerous-verb` | warn | `escalate`, `bind`, `impersonate`, or `pods/exec`, `pods/attach`, `pods/portforward`, `pods/ephemeralcontainers`, `nodes/proxy`, `serviceaccounts/token` | each lets the holder act as someone else or run code in someone else's pod | scope to named roles or namespaces; separate break-glass role |
| R04 | `rbac-broad-binding` | warn (fail for anonymous or all-authenticated) | a binding to `cluster-admin`, or a subject `system:anonymous`, `system:unauthenticated`, `system:authenticated` | full cluster control, or a grant to everyone | bind a narrow role; never bind the broad groups |

## Network and admission rules

| # | Rule | Default | Fires when | Why it matters | Fix |
| --- | --- | --- | --- | --- | --- |
| N01 | `namespace-without-networkpolicy` | note | a namespace with workloads (or a HelmRelease) and no NetworkPolicy or CiliumNetworkPolicy for it in the scanned set; suppressed when a cluster-wide policy is present | without a default deny, any pod can reach it and it can reach anything | default-deny ingress and egress plus a DNS allow, then named allows |
| A01 | `enforcement-policy-in-audit` | warn | a Kyverno policy, ValidatingPolicy or ValidatingAdmissionPolicyBinding whose name or title says block, deny, disallow, restrict, require, enforce, prevent, refuse, forbid or reject runs in Audit, or without `Deny` | monitoring that reads as enforcement: reviewers stop looking because they believe it blocks | Enforce (or `Deny`) after the policy report is clean, or rename it so it says it audits |
| A02 | `admission-fail-open` | note | a webhook with `failurePolicy: Ignore`, or an enforcement-named policy that fails open | while the engine is down, every request passes | `Fail` for security policies, with the engine highly available and its own namespace excluded |

Namespaces are taken from `metadata.namespace`, else the nearest directory holding a Namespace object or a `kustomization.yaml` with `namespace:`. Flux `targetNamespace` is not followed; scan rendered output when it matters.

## Secret and image rules

| # | Rule | Default | Fires when | Why it matters | Fix |
| --- | --- | --- | --- | --- | --- |
| S01 | `plaintext-secret` | warn (note for a git-ignored local copy) | a Secret with `data` or `stringData` values in a file without SOPS metadata; `ENC[` values and `${VAR}` placeholders are ignored, and values are never printed | anyone with read access to the repository holds the credential, and history keeps it after deletion | External Secrets or SOPS; rotate the value if it was ever committed |
| K08 | `image-not-digest-pinned` | warn (note when the chart supplies the tag) | a container image, a values `image:` string, or an `image: {repository, tag}` mapping without `@sha256` | a tag can be moved to different content after review | `tag@sha256:...` or a `digest:` field, updated by Renovate |
| D01 | `dockerfile-base-not-digest-pinned` | warn | `FROM` without a digest, including a build argument whose default has none | the base image can change between two builds of the same commit | `FROM image:tag@sha256:...` |
| D02 | `dockerfile-runs-as-root` | warn | the final stage has no `USER`, or `USER root`/`0` (images named `nonroot` are exempt) | a container escape starts as root | `USER 10001` or a nonroot base |
| D03 | `dockerfile-remote-add` | warn | `ADD https://...` without `--checksum` | the build trusts whatever the server returns | `ADD --checksum=sha256:...`, or download and verify in `RUN` |
| D04 | `dockerfile-secret-in-build-arg` | warn | `ARG` or `ENV` with a secret-like name (`_FILE`, `_PATH`, `_URL` suffixes exempt) | build arguments and env stay in the image history and metadata | `RUN --mount=type=secret` |
| D05 | `dockerfile-pipe-to-shell` | warn | `curl` or `wget` piped into a shell in `RUN` | unverified code runs at build time with the build's credentials | download, verify a checksum or signature, then run |

## CI rules

Workflows are files under `.github/workflows/`, `.forgejo/workflows/` or `.gitea/workflows/`.

| # | Rule | Default | Fires when | Why it matters | Fix |
| --- | --- | --- | --- | --- | --- |
| C01 | `ci-untrusted-checkout` | fail | `pull_request_target` or `workflow_run` with a checkout of the pull request head | code from a fork runs with the base repository's secrets and write token | `pull_request` for untrusted code; if the privileged trigger is needed, never check out or run the head |
| C02 | `ci-permissions-missing` | warn (note on Forgejo or Gitea) | neither the workflow nor the job sets `permissions:` | the token gets the repository default, often write | `permissions: {contents: read}` at the top, widen per job; on Forgejo, check that your version applies the block before relying on it |
| C03 | `ci-permissions-write-all` | warn | `permissions: write-all` | every scope writable for every step | list the scopes the job needs |
| C04 | `ci-action-not-sha-pinned` | warn | `uses:` with a tag or branch (local `./` actions exempt; `docker://` needs a digest) | a tag can be repointed: the 2025 tj-actions/changed-files compromise did exactly that | the full 40-character commit SHA, with the version in a trailing note Renovate maintains |
| C05 | `ci-secret-echoed` | fail | a `run:` line prints a `secrets.` expression with echo, printf, tee or a heredoc (`::add-mask::` exempt) | masking is best effort; encoded or split output leaks | pass it through `env:` and never print it |
| C06 | `ci-untrusted-expression-in-run` | fail | a `run:` script interpolates attacker-written event text: issue, PR, comment or review titles and bodies, head branch names, commit messages | the expression is pasted into the script before the shell runs, so the text becomes code | move it to `env:` and quote the variable |

## Exemptions and why

Each comes from a false positive found on a real repository, with a matching case in `fixtures/careful/`:

- **Policy test fixtures**: directories with `kyverno-test.yaml` or `chainsaw-test.yaml` hold deliberately bad resources; they are skipped and listed.
- **Kustomize patch files** named in a `kustomization.yaml` are partial objects; absence rules (K05, K07, N01) skip them. Findings on a patched base describe it unpatched, and the report says so: scan `kubectl kustomize` output for the merged object.
- **Git-ignored files** in a walked tree (generated machine configs, decrypted local copies) are skipped and counted; pass one explicitly to judge it.
- **Go templates in block scalars** (alert text such as `{{ $labels.instance }}`) parse normally; only files that fail to parse and contain template lines are reported as unrendered.
- **Policies and policy exceptions** describe pods without being pods, so host and privilege keys inside them are not judged.

## Advisory lookup

```bash
python3 scripts/advisory_lookup.py package npm lodash 4.17.20
python3 scripts/advisory_lookup.py package go github.com/kata-containers/kata-containers 3.30.0 --json
python3 scripts/advisory_lookup.py git --repo path/to/clone --fix-commit SHA --shipped v3.30.0
```

- `package` queries OSV and the GitHub Advisory Database (network, only when run) and prints a **coverage table**: answered, failed (with the HTTP status) or skipped, plus the sources never queried. Advisories are merged by shared ids and aliases, each row naming which source listed it; withdrawn advisories stay visible but are not counted. Ecosystem names and endpoints live in [scripts/advisory-sources.json](scripts/advisory-sources.json), dated; re-check it when a lookup fails with an unknown ecosystem. `GITHUB_TOKEN` lifts GitHub's 60-requests-an-hour limit.
- `git` reads a local clone: shallow or not, whether it holds the fix commit, the tags that contain it, and whether `--shipped` (the deployed tag or commit) contains it.
- Exit status: 0 clean, 1 found (or the shipped ref lacks the fix), 3 inconclusive, 2 usage error. A clean result is worded as "no advisory in the sources that answered", never as "not affected".
