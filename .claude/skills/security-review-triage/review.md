# Review: code diffs and infrastructure config

Contents: [Code review](#code-review) · [Exclusions](#exclusions) · [Infrastructure review](#infrastructure-review) · [Output](#output)

## Code review

1. **Report on the diff, research the whole repository.** Find existing sanitizers, authorization helpers, framework escaping and any threat model before judging new code.
2. **Rank changed files by risk, not size**: authentication, authorization, parsing of untrusted input, crypto, file and process access, serialization, CI and deploy config first. Run `git blame` on security code the diff removes.
3. **Trace source → controls → sink.** Decide per value whether an attacker or the server controls it. Trust follows who wrote a value, not the channel that delivered it: model output, shared volumes, queue messages and webhook bodies are input.
4. **Confidence gate**: report at 0.8 or above; 0.7–0.8 goes under "needs verification" with the condition it depends on; below that, drop it. The number orders findings; it is not a probability ([evidence.md](evidence.md#models-as-reviewers)).
5. **Check each finding on its own** against the exclusions below and the dismissal tests in [triage.md](triage.md#dismissal-tests) before it is reported. Models flag patched code as vulnerable; read the code path, do not pattern-match it.

## Exclusions

Drop these from a code review unless the change itself introduces the risk:

- Denial of service, missing rate limits, resource exhaustion.
- Outdated dependencies: handle them under [advisories.md](advisories.md).
- Test files, fixtures, documentation.
- Log spoofing; SSRF that controls only the path; regular-expression denial of service.
- Framework-escaped output (template autoescaping, ORM parameters) unless the code uses the framework's explicit bypass.
- Values the server controls: environment variables and CLI flags in application code, unguessable identifiers, server-side config stores.

These exclusions are for application code. In infrastructure review, config and environment **are** the attack surface.

## Infrastructure review

1. **Render first.** Judge what the cluster receives: `helm template`, `kubectl kustomize`, `flux build kustomization`. A HelmRelease's values are only part of the pod; chart defaults fill the rest.
2. **Scan**: `python3 scripts/infra_scan.py PATH` or pipe rendered output to `infra_scan.py -`. Every rule's meaning, severity guidance and fix is in [checks.md](checks.md). The scanner covers the axes below except supply-chain signatures and secret placement; check those by hand.
3. **Rate each finding exposure × blast radius** ([checks.md](checks.md#reading-a-finding)). The same pattern runs from P0 to P3. If the blast radius cannot be determined, rate conservatively and state the assumption.
4. **Missing hardening is reportable as hardening (P2/P3), not as a vulnerability.** A privileged workload by design is a true finding with an owner and a review date, not a false positive.
5. **Monitoring is not enforcement.** An Audit-mode policy, an alert or a dashboard does not block anything.
6. **List what a file read cannot see** (the scanner prints it): live drift, admission mutations, aggregated RBAC, CNI enforcement, runner isolation. Confirm read-only against the live system before calling anything closed.

| Area | Check by hand as well |
| --- | --- |
| Pods | Pod Security labels on the live namespace; user namespaces; who can create pods there |
| Runtime sandboxes | RuntimeClass handlers; the runtime's own annotation allowlist; debug consoles |
| RBAC | `kubectl auth can-i --list --as=system:serviceaccount:NS:NAME`; aggregated ClusterRoles |
| Network | default deny both ways plus DNS; egress by name; host-network pods bypass policy |
| Images | allowed registries; signature verification at admission |
| Admission | failure policy; namespace exclusions in the engine's config; that the webhook is actually called |
| CI | trigger × permissions × untrusted code; secrets scoped per environment; runner reuse between jobs |
| Secrets | where each lives, who reads it, whether untrusted workloads mount it, whether it is logged |
| Terraform and cloud | public exposure, wildcard IAM, state file access; no scanner here, read the plan |

## Output

A findings table first (rule or class, `file:line` at a named commit, priority with the exposure and blast radius that set it, fix), then dropped findings with the test that dropped each, then what was not checked. A clean review still lists what a file read cannot see.
