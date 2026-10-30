# Attribution

This skill consolidates ideas from the following works, restated in our own words. Each is used under its own license; none of the upstream projects endorses this skill.

## Apache-2.0 and MIT

- anthropics/claude-code-security-review, the `/security-review` prompt (MIT): diff-only scope, confidence threshold, per-finding false-positive pass, exclusion list. <https://github.com/anthropics/claude-code-security-review>
- getsentry/skills, `security-review` and `security-model` (Apache-2.0): confidence tiers, server-controlled values, monitoring versus enforcement, keeping SECURITY.md for disclosure. `security-model` itself adapts openai/codex-security. <https://github.com/getsentry/skills>
- openai/codex-security (Apache-2.0): verdict vocabulary, the smallest deciding fact, trust not inferred from labels, impact × likelihood, two-sided fix verification, supplied versus inspected versus observed versus inferred. <https://github.com/openai/codex-security>
- openai/skills, `security-threat-model` (Apache-2.0): evidence-tied claims, explicit attacker non-capabilities. <https://github.com/openai/skills>
- superagent-ai/skills, `security-disclosure-triage` and `infra-security` (MIT): the asset's real guard, usability of what the attacker holds, arguing the opposite case, exposure × blast radius, listing what a file read cannot see. <https://github.com/superagent-ai/skills>
- addyosmani/agent-skills, `security-and-hardening` (MIT): STRIDE per boundary, trust follows who wrote a value. <https://github.com/addyosmani/agent-skills>
- paperclipai/paperclip, `deal-with-security-advisory` (MIT): nothing about the flaw in public branches, titles or commits before publication. <https://github.com/paperclipai/paperclip>

## Rule ideas for the scanner (no code copied)

- kubernetes/website, Pod Security Standards (CC-BY-4.0): the restricted-profile fields behind K05. <https://kubernetes.io/docs/concepts/security/pod-security-standards/>
- Kubescape, Polaris, kube-score and Checkov (Apache-2.0 or MIT): the kinds of workload, RBAC and image checks a config scanner is expected to cover. <https://github.com/kubescape/kubescape> <https://github.com/FairwindsOps/polaris> <https://github.com/zegl/kube-score> <https://github.com/bridgecrewio/checkov>
- zizmor (MIT): the CI rule classes (privileged triggers, template injection, unpinned uses, excessive permissions). <https://github.com/zizmorcore/zizmor>
- Kata Containers documentation (Apache-2.0): annotation forwarding and the `enable_annotations` allowlist behind K09 and K10. <https://github.com/kata-containers/kata-containers>
- OSV (Apache-2.0) and the GitHub Advisory Database (CC-BY-4.0): the advisory APIs `advisory_lookup.py` queries. <https://osv.dev> <https://github.com/github/advisory-database>

## Ideas only (share-alike or copyleft sources; no text or code copied)

- hadolint (GPL-3.0): Dockerfile rule classes (unpinned base images, root user, remote ADD, pipe to shell). <https://github.com/hadolint/hadolint>

- trailofbits/skills (CC-BY-SA-4.0): restating the claim first, the false-positive checklist, chains of dismissed findings, risk-ranked diffs, three-arm fix proofs, evidence gaps that are never passes. <https://github.com/trailofbits/skills>
- The vulnerability triage brocards by William Woodruff, as used in trailofbits/skills `vulnerability-triage-brocards`: the dismissal tests. <https://vulnbrocards.com>

This skill is MIT-licensed (see the repository `LICENSE`).
