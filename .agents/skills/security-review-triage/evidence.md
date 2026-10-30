# Evidence: what the rules in this skill rest on

Strength labels: **Strong** (a peer-reviewed or controlled study, a large sample with a published method, a specification, or the vendor's own documentation of its system) · **Consistent** (several independent sources or practitioners agree) · **Contested** · **Anecdotal** (one case, one lookup, a design default with no published calibration).

Contents: [Models as reviewers](#models-as-reviewers) · [Advisory data](#advisory-data) · [Severity](#severity) · [Infrastructure controls](#infrastructure-controls) · [Verification](#verification) · [Claim table](#claim-table)

## Models as reviewers

- **Models over-report.** Ullah et al. tested eight LLMs on vulnerability detection: every model had a high false-positive rate, flagged already-patched code as still vulnerable, and changed its answer under renaming variables or adding library calls (26 % and 17 % of cases for two models). Reasoning was often wrong even when the verdict was right ([IEEE S&P 2024, arXiv 2312.12575](https://arxiv.org/abs/2312.12575), Strong). → "not proven" is the default, every finding is checked on its own, and a verdict cites inspected or observed evidence.
- **Invalid AI-written reports cost maintainers real time.** curl's maintainer described a stream of plausible-looking, wrong security reports written with LLMs ([The I in LLM stands for intelligence, 2024](https://daniel.haxx.se/blog/2024/01/02/the-i-in-llm-stands-for-intelligence/)); the Python Software Foundation's security developer reported the same pattern across open source ([New era of slop security reports, 2024](https://sethmlarson.dev/slop-security-reports)). Consistent. → restate the claim and run the dismissal tests before anything leaves your hands; say plainly what was not tested.
- **The 0.8 confidence gate is a design default**, taken from Anthropic's `/security-review` prompt (report above 0.8, 0.7–0.8 needs specific conditions, below 0.7 drop). No calibration of model confidence against outcomes is published (Anecdotal). → use it as an ordering device, never as a probability in a report.

## Advisory data

- **Databases disagree on affected versions.** Dong et al. compared NVD entries with external reports: only 59.8 % listed exactly the same vulnerable versions, and NVD over-claimed and under-claimed about equally often ([USENIX Security 2019](https://www.usenix.org/conference/usenixsecurity19/presentation/dong), Strong). → query more than one source and print which answered.
- **OSV and the GitHub Advisory Database differ in practice.** Running `advisory_lookup.py package npm lodash 4.17.20` on 2026-10-24: both sources answered; GitHub also returned an advisory it had since withdrawn, which OSV omitted (Anecdotal, one lookup). → the script marks withdrawn advisories and names which source listed each row.
- **The version key matters as much as the version.** On the same day, `go github.com/kata-containers/kata-containers 3.30.0` returned no advisories from either source, while the pseudo-version of a commit from before the fixes returned three, including the annotation advisory in the worked example. The Go module is published under pseudo-versions, not release tags (Anecdotal, one lookup; the mechanism is documented by the Go module proxy). → an empty answer is checked against the identifier the database uses before it is reported.
- **NVD enrichment has lagged since early 2024**, when NIST announced a slowdown and a growing backlog of unanalysed CVEs ([NVD program announcement](https://nvd.nist.gov/general/news/nvd-program-transition-announcement), Strong). → NVD is listed as not queried, never as "no result".

## Severity

- **The Base score belongs to the maintainer; Threat and Environmental belong to the consumer.** CVSS v4.0 defines Base as the intrinsic characteristics of the flaw and the other groups as time- and deployment-specific; Supplemental metrics never change the score ([FIRST CVSS v4.0 specification](https://www.first.org/cvss/v4.0/specification-document), Strong).
- **A CVE number and score prove neither reality nor impact.** curl's maintainer documented a CVE scored 9.8 by NVD for a bug the project rated low and disputed ([CVE-2020-19909 is everything that is wrong with CVEs, 2023](https://daniel.haxx.se/blog/2023/08/26/cve-2020-19909-is-everything-that-is-wrong-with-cves/), Anecdotal as one case, Consistent with the database study above). → judge the technical claim with the ID and score removed.

## Infrastructure controls

- **Audit mode does not block.** Kyverno documents Audit as reporting violations in policy reports while admitting the resource; ValidatingAdmissionPolicy only rejects with `Deny` among its `validationActions` ([Kyverno policy settings](https://kyverno.io/docs/policy-types/cluster-policy/policy-settings/); [Kubernetes ValidatingAdmissionPolicy](https://kubernetes.io/docs/reference/access-authn-authz/validating-admission-policy/), Strong). → rule A01.
- **Pod hardening has a published baseline.** The Pod Security Standards' restricted profile names the fields rule K05 checks ([Kubernetes Pod Security Standards](https://kubernetes.io/docs/concepts/security/pod-security-standards/), Strong).
- **Privileged CI triggers run with the base repository's secrets.** GitHub Security Lab's "pwn request" write-up shows why `pull_request_target` plus a checkout of the PR head runs untrusted code with privileges ([Preventing pwn requests](https://securitylab.github.com/resources/github-actions-preventing-pwn-requests/), Strong), and GitHub's hardening guide documents script injection through `${{ }}` expressions in `run:` ([Security hardening for GitHub Actions](https://docs.github.com/en/actions/security-for-github-actions/security-guides/security-hardening-for-github-actions), Strong). → rules C01 and C06.
- **Tags move.** In March 2025 the `tj-actions/changed-files` tags were repointed to a malicious commit that printed CI secrets into logs (CVE-2025-30066, [GHSA-mrrh-fwg8-r2c3](https://github.com/advisories/GHSA-mrrh-fwg8-r2c3), Strong). → rules C04, K08, D01.
- **Sandbox runtimes accept configuration from pods.** Kata Containers reads `io.katacontainers.*` annotations, filtered by `enable_annotations` in its configuration, and containerd forwards only the annotations listed in `pod_annotations`; the documentation's own containerd example forwards `io.katacontainers.*` and warns against enabling `virtio_fs_extra_args` unless every annotation source is trusted ([Kata annotations](https://github.com/kata-containers/kata-containers/blob/main/docs/how-to/how-to-set-sandbox-config-kata.md), Strong). A published advisory shows the path reaching the host: [GHSA-rr59-xxvx-96qr](https://github.com/advisories/GHSA-rr59-xxvx-96qr) (CVE-2026-44210), a VM escape through argument injection in a default-enabled pod annotation, fixed in 3.31.0 (Strong, vendor advisory). → rules K09, K10, and the [worked example](case-sandbox-annotations.md).

## Verification

- **Two-sided and three-arm fix proofs** (bad case blocked, legitimate case allowed; before, after and reverted) are recommended by OpenAI's codex-security and Trail of Bits' published skills; no controlled study compares them with one-sided checks (Consistent).
- **A gate that only fails proves nothing.** A crashing checker also exits non-zero, so a gate is proven only when it also passes a case that must pass. This skill's own `test.sh` was checked that way: five deliberate breakages each failed it, and one exposed a crash in the scanner that a failure-only test would have read as a working rule (Anecdotal, this repository).

## Claim table

| Claim | Verdict | Strength | What the skill does |
| --- | --- | --- | --- |
| LLM reviewers produce many false positives | true | Strong | default "not proven"; check each finding |
| Confidence above 0.8 means likely real | unknown | Anecdotal (uncalibrated default) | use to order, not to report |
| One advisory database is enough | false | Strong (version disagreement) | coverage table, at least two sources |
| "No advisories" means not affected | false | Strong | wording bounded by sources that answered |
| A high CVSS Base score means high risk here | false | Strong (spec) | consumer-side Threat and Environmental, labelled |
| An Audit-mode policy protects | false | Strong | rule A01; prove Enforce on the live object |
| Pinning actions by tag is safe | false | Strong (2025 incident) | rule C04 |
| Missing hardening is a vulnerability | no | Consistent | report as P2/P3 hardening |
| A fix is proven by green CI | false | Consistent | two-sided proof on the live object |
| Sandbox annotations are a config-level exposure | true where forwarded | Strong (advisories) | rules K09, K10; worked example |
