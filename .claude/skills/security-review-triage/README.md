# security-review-triage

A defensive security skill for what comes after a scanner, an agent or a person says "this looks like a vulnerability": is it real, does it affect us, how far does it reach, who do we tell, and did the fix work. It never produces payloads or exploits; attacks are described by their shape.

What it does that a generic security prompt doesn't:

- **Evidence before verdicts.** Every claim is labelled supplied, inspected, observed or inferred, and "not proven" is the default, because models reviewing code over-report (IEEE S&P 2024: high false-positive rates, verdicts flipped by renaming). The rules themselves carry Strong, Consistent, Contested or Anecdotal labels with their sources in `evidence.md`.
- **Triage with dismissal tests.** Restate the claim, run six tests in order, find the asset's real guard, argue the opposite case, and end on confirmed, not actionable or needs review, naming the smallest missing fact.
- **Severity from exposure × blast radius.** A blast-radius card (entry point, position, privileges, asset, limits, explicit non-capabilities, who is exposed) instead of a pattern's default score or a CVSS Base score.
- **`scripts/infra_scan.py`.** A stdlib-only scanner with 29 numbered rules for Kubernetes manifests, Helm values, Kustomize and Helm output (via stdin), sandbox runtime config, Dockerfiles and GitHub, Forgejo or Gitea workflows: privileged and host-namespace pods, hostPath, capabilities, default tokens, digest pinning, Kata and gVisor annotation passthrough, RBAC wildcards and escalation verbs, namespaces without NetworkPolicy, enforcement policies left in Audit, fail-open admission, plaintext Secrets (values never printed), `pull_request_target` head checkouts, missing or write-all permissions, unpinned actions, echoed secrets and script injection. Every report ends with what a file read cannot see.
- **`scripts/advisory_lookup.py`.** Queries OSV and the GitHub Advisory Database, merges by alias, marks withdrawn advisories and prints a coverage table, so "no advisories" is always bounded by the sources that answered. Its `git` mode checks whether the deployed tag contains a fix commit and refuses to conclude on a shallow clone.
- **Disclosure and proof.** The private channel in order, nothing public before the advisory, a report draft matching GitHub's form, and fixes proven two ways (blocked and allowed cases) on the live object, with a named regression signal.

## Install

```text
/plugin install security-review-triage@ai-skills
```

or `npx skills add https://forgejo.webgrip.dev/webgrip/ai-skills.git -s security-review-triage`.

## Example prompts

- "Trivy says our ingress image has CVE-2026-XXXX. Are we actually affected?"
- "Review this Helm values file and the release workflow before I merge."
- "Wat is de blast radius hiervan, wie kan dit triggeren?"
- "I added a Kyverno policy to block those annotations. Did it work?"
- "How do I report this upstream without making it public?"

## Scripts

```bash
python3 skills/security-review-triage/scripts/infra_scan.py path/to/repo
kubectl kustomize apps/shop | python3 skills/security-review-triage/scripts/infra_scan.py -
python3 skills/security-review-triage/scripts/advisory_lookup.py package npm lodash 4.17.20
python3 skills/security-review-triage/scripts/advisory_lookup.py git --repo clone --fix-commit SHA --shipped v1.2.3
```

`infra_scan.py` reads files only and exits 1 at or above `--fail-on` (default `fail`). `advisory_lookup.py package` is the only network access, and only when run; `--offline` contacts nothing. `test.sh` asserts that every rule fires on `fixtures/careless`, that none fires on `fixtures/careful`, that each rule is documented in `checks.md`, and runs unit tests for the YAML reader and for the advisory lookup against a local server and throwaway git repositories. The gate itself was mutation-tested: disabling a rule, polluting the careful fixture, un-documenting a rule, undating the sources file and adding an unparseable fixture each fail it.

Before release the scanner ran read-only over two GitOps repositories (about 1,600 files) and a sample of real Dockerfiles. The false positives it found became exemptions with a careful-fixture case each: policy-test fixture directories, Kustomize patch files judged as whole objects, git-ignored generated configs, alert-template text read as unrendered Helm, and a chart-supplied image tag.

## Sources

Each reference file links its sources: the Kubernetes, Kyverno, Kata Containers and GitHub documentation, the FIRST CVSS v4.0 specification, OSV and GitHub advisory APIs, peer-reviewed studies on LLM vulnerability detection and on advisory database accuracy, published incident advisories, and the security skills credited in [NOTICE.md](NOTICE.md).
