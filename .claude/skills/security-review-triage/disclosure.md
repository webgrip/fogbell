# Disclosure: telling the right people, privately

Contents: [Channel](#channel) · [Before publication](#before-publication) · [Report template](#report-template) · [After the fix](#after-the-fix)

## Channel

In order:

1. The project's `SECURITY.md` (GitHub reads it from the root, `.github/` or `docs/`).
2. The forge's private vulnerability report (GitHub: Security tab → Report a vulnerability; on other forges, check whether private reporting is enabled).
3. A security email named by the project or its foundation.
4. An issue that asks **only** for a security contact, with no detail.

If the flaw is in an upstream component and also in your own deployment, mitigate your deployment first (an admission policy, a config change) and report upstream in parallel.

## Before publication

- **Nothing in public until the advisory is published**: no issue, PR title, commit message, branch name, chat message, ticket, commit trailer or mirrored push that names the flaw. A mirrored repository publishes your branch names too.
- Internal tickets for the mitigation describe the control being added ("deny sandbox override annotations at admission"), not the flaw it closes.
- Keep the evidence (commands, outputs, commit SHAs) in a private place you can hand to the maintainer.

## Report template

Fields match GitHub's private vulnerability report form.

- **Title:** component: what the flaw allows, in one line.
- **Summary:** affected component and versions, the class of flaw, who can trigger it, the impact, and any upstream advisory it matches.
- **Details:** each finding with `file:line` at a named commit or tag, and the suggested fixes.
- **Proof of concept:** read-only checks that show the precondition holds (a config line, a rendered value, a dry-run result). Say plainly what was not run or not tested; never include a working exploit.
- **Impact:** who is exposed and who is not, from the blast-radius card ([triage.md](triage.md#blast-radius-card)).
- **Affected products:** ecosystem, package, affected range, patched version if known.
- **Severity:** a proposed level or vector, marked "proposed; the maintainer decides".
- **Workaround:** what users can do now, such as an admission policy or a config change, and how it was verified.
- **AI assistance:** tick the form's box when a model helped find or write the report.

## After the fix

- Public advisory text names the class, not the instance: no function names, paths or payloads beyond what the maintainer publishes.
- Never derive severity from the reporter's claim; maintainers routinely score lower, and theirs is the one that counts.
- Keep your own mitigation until the fixed version is deployed and verified; then decide whether the mitigation stays as defense in depth.
