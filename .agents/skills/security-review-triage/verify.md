# Verify a fix or mitigation

Contents: [The rule](#the-rule) · [Proofs by control](#proofs-by-control) · [Regression signal](#regression-signal)

## The rule

- **Two-way, every time**: at least two cases that must be **blocked** and at least one legitimate case that must be **allowed**, run through the same harness. A check that only ever fails cannot tell a working control from a broken one.
- **Three arms where possible**: before the fix the bad case passes, after it is blocked, and reverting the fix lets it pass again. The allowed case passes in all three.
- **A timeout, missing marker or setup failure is a gap, never a pass.**
- **`fixed` only when the boundary is closed and legitimate behavior still works.** Follow moved code; a closed ticket, green CI or a silent scanner is not proof.
- **Prove it against the live object.** Committed is not delivered: a policy in git that never reconciled, or reconciled in Audit, protects nothing.
- **Probes prove a boundary, nothing more**: harmless test objects in a test namespace, dry runs where the API allows them, cleaned up afterwards. No exploit, no payload.

## Proofs by control

| Control | Blocked case | Allowed case | Live proof |
| --- | --- | --- | --- |
| Kyverno or ValidatingAdmissionPolicy | a test pod carrying the denied field or annotation (two variants: the exact key and a prefix sibling) | the same pod without it | `kubectl apply --dry-run=server -f pod.yaml` returns the policy's message, and the live policy shows Enforce or `Deny`; the CLI test (`kyverno test`) as the second, offline proof |
| Runtime allowlist (containerd, Kata, gVisor) | a pod requesting a non-allowlisted annotation | a pod requesting none | read the installed config on a node read-only; the admission proof above as the second layer |
| NetworkPolicy | a probe from an unlisted pod to the target port | the same probe from an allowed pod | a short-lived pod running a TCP connect with a timeout; a timeout on the allowed case is a gap |
| RBAC | `kubectl auth can-i VERB RESOURCE --as=SUBJECT` for what must be denied | the same for what the subject needs | both answers from the live API server |
| CI workflow | the untrusted trigger cannot reach the secret or the write token | a normal PR still builds | a PR from a fork in a test repository, logs read for the expected skip |
| Code fix | the original trigger, and one variant of it | the legitimate input that exercises the same path | tests at the fixed commit, and the same tests failing at the parent |

## Regression signal

Name the signal that would catch the hole coming back: the policy report count for the rule, an alert on a policy switching to Audit, the `infra_scan.py --fail-on warn` step in CI, or a scheduled probe. "Monitored by nothing" is an acceptable answer only when it is written down with a reason.
