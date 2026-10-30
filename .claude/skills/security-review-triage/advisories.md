# Advisories and versions: are we affected

Contents: [Look up](#look-up) · [Map to the shipped artifact](#map-to-the-shipped-artifact) · [Word it](#word-it) · [CVSS](#cvss)

## Look up

```bash
python3 scripts/advisory_lookup.py package ECOSYSTEM NAME VERSION
python3 scripts/advisory_lookup.py git --repo CLONE --fix-commit SHA --shipped DEPLOYED_TAG
```

The script queries OSV and the GitHub Advisory Database (network access, only when run), merges rows by shared ids and aliases, and prints a coverage table: which source answered, which failed and why, and which were never queried. Details and exit codes: [checks.md](checks.md#advisory-lookup).

- **Query the identifier the database uses.** Go modules without semver tags are listed under pseudo-versions; distribution packages under the distribution's version; container images by the packages inside them. An empty answer for the release number is not an empty answer for the code ([evidence.md](evidence.md#advisory-data)).
- **Check the project's own advisories too** (`gh api repos/OWNER/REPO/security-advisories`, or the forge's Security tab) and the distribution tracker when the package came from one. The script lists both as not queried.
- **Withdrawn advisories** stay visible in the table and are not counted.
- **Offline or rate limited**: the result is inconclusive, and says so. Set `GITHUB_TOKEN` or rerun later; never report a failed source as "no advisories".

## Map to the shipped artifact

- **Affected means what runs**: the deployed tag or digest, the image's package list, the config actually loaded. Read it from the live object (`kubectl get ... -o jsonpath` on the image digest, the running binary's version) rather than from the repository's default branch.
- **Fix commits**: `advisory_lookup.py git` tells whether the shipped ref contains the fix and which tags do. A shallow clone or a missing commit makes the answer inconclusive.
- **Reachability beats presence**: is the vulnerable function, feature, annotation or setting used here? A library present but never called is "present, not reachable": say both halves.
- **Backports**: a distribution or vendor build may carry the fix under an older version number; check its changelog or tracker before calling it affected.

## Word it

- "X affects Y when Z is configured", with the evidence label of each part.
- Never upgrade "possible" to "affected", and never read "no advisories" beyond the sources that answered.
- Name the sources and the date: advisory data changes daily.

## CVSS

- **v3.1** metrics: AV, AC, PR, UI, S, C, I, A. Bands: 0.1–3.9 Low, 4.0–6.9 Medium (GitHub says Moderate), 7.0–8.9 High, 9.0–10 Critical.
- **v4.0** groups: Base, Threat, Environmental, Supplemental (Supplemental never changes the score). New: Attack Requirements (AT); User Interaction None, Passive or Active; Scope replaced by impacts on the vulnerable system (VC, VI, VA) and on subsequent systems (SC, SI, SA).
- **The maintainer owns the Base score.** A reporter proposes a vector with a one-line reason per non-obvious metric. Your own environment's score is consumer-side (Threat and Environmental) and is labelled as such.
- Sources disagree on scores for the same CVE; quote whose score it is.
