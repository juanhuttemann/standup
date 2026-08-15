# TODO

Roadmap items, highest leverage first.

## Bugs — v0.4.0 end-user evaluation

Misuse handling (bad/ambiguous IDs, invalid status/date/days, empty add)
is good — clear, actionable errors throughout. Flaws found:

- [x] `commits` destroys day attribution (core feature broken): imported
      commits are timestamped now, not commit time — all Friday commits
      landed "today" (`list --date` finds nothing, `generate` has no
      Yesterday section). → Store the commit's author date as the task
      timestamp.
- [x] `list` demands model credentials: `list` (also `--date`, etc.)
      fails with missing `OPENAI_BASE_URL`/`OPENAI_MODEL` though listing
      never calls a model — breaks the zero-config claim on a read-only
      command. → Require the vars only in add/generate (online mode).
- [x] `--tag` is a substring search, not a tag match: `--tag fix` returns
      "Fixed login bug #auth"; `--tag api` matches the word "API" with no
      tag present. → Match the literal `#token`.
- [x] `edit` fallback `vi` doesn't exist on Windows: "editor vi:
      executable file not found" on the platform we ship a native
      installer for. → Fall back to notepad on Windows.
- [x] `commits` misleads outside a repo and on email mismatch: outside a
      repo it errors "user.email is not configured" (config check
      precedes repo check — wrong diagnosis); the `--author=<user.email>`
      filter silently yields zero tasks for a different/secondary git
      identity. → Check repo first; warn when the author filter matches
      nothing.
- [x] `.env` lookup is working-directory only: running from a project
      subdir loses the endpoint config. → Walk up to the git root like
      git does.
- [x] Blocked tasks duplicated in reports: they appear both in the day
      section and under `## Blockers`; README implies they're moved
      there. → Exclude blocked tasks from day sections.
- [x] Non-deterministic report formatting: bare `generate` sometimes
      drops `[status]`/times; `generate N` and offline mode always keep
      them — the reporter prompt doesn't pin the format. → Render
      deterministically; use the model only for phrasing.
- [x] Re-running `commits` duplicates every task — no dedupe of
      already-imported commits.
- [x] Minor: multi-line tasks break the one-row list layout (continuation
      lines lack columns); endpoint-down errors are raw Go dumps
      (`connectex: …`) rather than friendly hints.

## Commit ingestion

- [x] Multi-repo support: `commits` accepts multiple repo paths (or walks
      subdirectories of the cwd) and collects the user's commits from all of
      them into one add pipeline.
- [x] Co-authored commits: commits carrying the user's address in a
      `Co-authored-by:` trailer are collected too, not just commits authored
      directly.
- [x] Full commit bodies: `commits` ingests the whole commit message, not
      only the subject line — the editor agent already cleans and splits
      multi-task input.

## Reports

- [x] Arbitrary date window: `generate` accepts explicit dates
      (e.g. `--from`/`--to` or `--date`), not only trailing N days from today.
- [x] Weekend-aware window: on Monday (or after holidays) the report covers
      the last working day onward, matching the lookback `commits` already
      uses, instead of literal yesterday.
- [x] Share output: `generate` gains a copy-to-clipboard flag and/or a
      webhook target so the report can go straight where the team reads it.

## Onboarding / polish

- [x] Report language as a first-class `language` config key (prompt-level
      today); document and market it.
- [x] `standup doctor`: sanity-checks setup — endpoint reachable, model set,
      git identity present, data file writable.

## Agent skills (share standup with coding agents)

- [x] Add the two skill paths + README/CHANGELOG entries; `make verify`.

  Shipped: `.agents/skills/standup/SKILL.md` (real file, single home —
  Codex skips symlinked SKILL.md files) + `.claude/skills/standup` dir
  symlink (Claude Code reads only `.claude/skills/` but follows dir
  symlinks). Frontmatter carries `name: standup` (must equal the dir name
  for Cursor/Amp/Copilot/Hermes) and a trigger-first `description`.
  Windows checkouts without symlink support lose only the Claude Code
  path; the real file keeps every other harness working.
