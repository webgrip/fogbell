# Triage: is it real, and how far does it reach

Contents: [Procedure](#procedure) · [Dismissal tests](#dismissal-tests) · [Blast-radius card](#blast-radius-card) · [Verdicts](#verdicts)

## Procedure

1. **Restate the claim** in your own words: actor, capability needed, trigger, root cause, impact, threat model. Many false positives fall apart here.
2. **Label the evidence**: supplied (the report or scanner says so), inspected (you read the code or config at a named commit), observed (you ran a read-only check and saw the result), inferred. A verdict rests on inspected or observed facts.
3. **Run the dismissal tests** below.
4. **Find the asset's real guard.** Bypassing a sibling check while the real guard holds is not a vulnerability.
5. **Usability**: can the attacker act on what they end up holding?
6. **Trust boundary**: an already-privileged actor, an operator's own misconfiguration or the same user is not a vulnerability unless the privilege gain is the point. Labels like "local", "admin" or "config" are not proof of trust: check who can actually set the value.
7. **Argue the opposite case** as strongly as you can; the finding must survive it.
8. **Fill the blast-radius card** and set the priority.
9. **Verdict.** After a batch, check whether dismissed findings combine into a chain.

## Dismissal tests

Run in order; stop at the first that dismisses, unless a full evaluation is asked for:

1. **No threat model, no vulnerability.** Name who attacks and what they gain.
2. **The required capability already covers the impact.** If the attacker must already hold what the finding gives them, dismiss.
3. **Outside real use.** A configuration or call path nobody runs is not a vulnerability in this deployment; it may still be one upstream.
4. **Standard or documented behavior.** If it behaves as specified, the issue may belong to whoever chose to use it that way.
5. **The cure is worse than the disease.** Weigh the fix's own risk and blast radius.
6. **A CVE ID is neither necessary nor sufficient.** Judge the technical claim with the ID and score removed.

Two-sided caution: do not dismiss merely because something is "debug only" or "needs local access", and do not accept merely because it "has a CVE" or "a high CVSS score".

## Blast-radius card

| Field | Content |
| --- | --- |
| Entry point | where the trigger enters |
| Position needed | internet · LAN · inside the cluster · pod creator in a namespace · on the node · admin |
| Privileges and preconditions | what the actor must already have or arrange |
| Asset reached | data, credentials, workloads, nodes, tenants |
| What limits it | network policy, RBAC, Pod Security, admission policy, mTLS, quotas, sandbox |
| What it does **not** give | explicit non-capabilities, so severity does not inflate |
| Who is exposed | which deployments meet the preconditions, and which do not |

Use STRIDE per crossed boundary as a lens and attack trees at most two levels deep, goals only. Likelihood follows position (internet high, LAN or pod creator medium, node or admin low); impact follows the asset. Apply it mechanically and show the inputs.

## Verdicts

| Verdict | Means | Must include |
| --- | --- | --- |
| `confirmed` | the precondition holds here and the impact follows | the inspected or observed facts, the card, a priority |
| `not actionable` | a dismissal test or the real guard holds | which test, and the fact that settled it |
| `needs review` | one fact is missing | the smallest fact that would change the verdict, and how to get it read-only |

Hardening notes (a defense-in-depth layer missing while the primary control holds) go in their own list and are never counted as vulnerabilities.
