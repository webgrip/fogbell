---
name: coderabbit-review
description: Review your own change with the CodeRabbit CLI before opening or updating a pull request, and handle its findings - run cr review in agent mode against the target branch, fix or reject each finding with a reason, turn a recurring one into a rule. Use before you open a pull request, when asked to review a branch or a diff with CodeRabbit, when a CodeRabbit CLI review comment appears on a Forgejo pull request, or when cr reports a rate limit or an incomplete review.
---

# CodeRabbit review

CodeRabbit reviews no Forgejo pull request directly. Its CLI reviews a local diff with the same rules
it uses on pull requests: `.coderabbit.yaml` (rendered by `agentstd install`), `.claude/rules` with
their `paths:`, `AGENTS.md` and the skills. Run it on your own change before a human sees it.

## Run it

1. Commit your work, then review it against the branch the pull request targets:

   ```bash
   cr review --agent --base origin/main      # or origin/development
   ```

   `cr` is `coderabbit`. Not installed: `curl -fsSL https://cli.coderabbit.ai/install.sh | sh`. Not
   signed in: ask the person to run `cr auth login --region eu`; never ask for or paste an API key.
2. Read the NDJSON on stdout line by line. Act on `finding` events (`severity`, `fileName`,
   `startLine`/`endLine`, `comment`, `codegenInstructions`). Ignore `heartbeat`.
3. The run is incomplete when the exit code is 1, or `complete` has `outcome: "failed"` or a positive
   `unreviewedFileCount`. Say so; do not report a partial review as clean.
4. A review takes minutes; allow 15. Run it in the background and keep working on something else.

## Handle each finding

- **critical, major:** fix it, or explain in one sentence why it is wrong. Never leave one silent.
- **minor:** fix it when the fix is small and inside the change; otherwise mention it.
- **trivial, info:** fix in passing or drop.
- About half of AI review findings are wrong or do not fit. A rejected finding with a reason is a
  good outcome, not a failure.
- After fixing, run the review again only on what changed (`cr review --agent --uncommitted`), not on
  the whole branch: every run counts against an hourly allowance (5 per hour per seat).

## When the same point keeps coming back

A finding that recurs, or one that a person also raises, becomes a rule in `.apm/instructions/`
(scoped with `applyTo`) or a check in CI. Do not leave it to a CodeRabbit learning: learnings are
invisible to the other agents and to people.

## Rate limits and errors

- A rate-limit error reports the wait. Wait it out or stop; never retry in a loop.
- "Too many files": the error suggests narrower scopes (`--dir`, `--committed`). Pick one and rerun.
- Review comments from the CI job on a pull request carry the marker
  `<!-- agentstd:coderabbit -->` and come from the same CLI.
