# Case: a runtime extension that forwards sandbox annotations

An anonymized walk through the procedure on a real finding. It describes shape only: no exploit steps, and nothing about the vendor beyond what the public Kata Containers documentation and advisory say.

## The claim

A coding agent reviewing a cluster reported: "The Kata extension on the nodes forwards every `io.katacontainers.*` pod annotation to the runtime, which matches a critical Kata advisory. The cluster is exposed."

## Restated

| Field | Content |
| --- | --- |
| Actor | anyone who can create a pod that uses the Kata RuntimeClass |
| Capability needed | pod create in a namespace allowed to use that RuntimeClass |
| Trigger | annotations on the pod, passed by containerd to the Kata runtime |
| Root cause | the node OS's Kata extension ships a containerd config with `pod_annotations = ["io.katacontainers.*"]` |
| Impact | depends on which annotations Kata itself accepts (`enable_annotations`) and on the installed Kata version |

## Evidence

| Fact | Label |
| --- | --- |
| The extension's containerd config forwards the whole prefix | inspected (the extension's published config at the deployed version) |
| `infra_scan.py` on the node config flags it as K10 `sandbox-annotation-passthrough` | observed |
| Kata's own documentation shows the same forwarding and filters with `enable_annotations` | inspected (upstream docs) |
| The public advisory GHSA-rr59-xxvx-96qr describes a VM escape through a default-enabled annotation, fixed in 3.31.0 | supplied (advisory text) |
| `advisory_lookup.py` returned nothing for the Kata release number and three advisories for the pseudo-version of a pre-fix commit | observed; see [evidence.md](evidence.md#advisory-data) |
| Which annotations the installed Kata build enables | needs review until read from a node |

## Dismissal tests

- Threat model: yes, a tenant with pod create but no node access.
- Capability already covers the impact: **no**. Pod create in a sandboxed namespace is meant to give less than the node; that is the point of the sandbox.
- Outside real use: no, the RuntimeClass is used by agent workloads.
- Documented behavior: partly. Forwarding is documented upstream; shipping it by default, combined with annotations Kata enables by default, is the exposure.

## Blast-radius card

| Field | Content |
| --- | --- |
| Entry point | pod annotations on a Kata pod |
| Position needed | pod creator in a namespace allowed to use the Kata RuntimeClass |
| Asset reached | at worst the node, through the sandbox's own configuration (per the public advisory's class) |
| What limits it | who may create pods there (RBAC), which namespaces may use the RuntimeClass, the installed Kata version, admission policy |
| What it does not give | nothing to callers without pod create; nothing on nodes without the extension; no effect on runc pods |
| Who is exposed | clusters running the extension with a Kata version below the fix, where untrusted parties create Kata pods |

Verdict: **confirmed as a config-level exposure**, priority set by who creates pods. In this cluster only the agent platform's controllers create Kata pods: P1 for the platform, not P0 for the cluster.

## Mitigation, proven two ways

An admission policy denying `io.katacontainers.*` and `dev.gvisor.*` annotations on pods, in Enforce:

1. **Offline**: a policy CLI test with three blocked pods (an exact key, a different key under the same prefix, the gVisor prefix) and one allowed pod with ordinary annotations.
2. **Live**: `kubectl apply --dry-run=server` of the same blocked and allowed pods against the cluster, plus a read of the live policy showing Enforce.

Regression signal: the policy's report count, and an alert if the policy leaves Enforce. The runtime-side allowlist stays on the list as a second layer, owned by the node OS project.

## Disclosure

The extension's project publishes a security policy with private vulnerability reporting. The report followed [disclosure.md](disclosure.md#report-template): the config line at a named version, the upstream advisory it matches, the blast-radius card, the admission-policy workaround with its two proofs, a proposed severity marked as a proposal, and a plain statement that nothing was exploited or run against the sandbox. Public tickets and commits named the control ("deny sandbox override annotations at admission"), not the flaw.

## What to take from it

1. The finding was real, but its severity came from who can create pods, not from the advisory's score.
2. An empty advisory lookup was a version-key problem, not a clean result.
3. The mitigation counted only after a blocked and an allowed case passed both offline and against the live API server.
4. The runtime config forwarding and the admission policy are two layers; closing one does not retire the other.
