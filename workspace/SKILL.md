# SKILL.md — stilltent Autonomous Loop

## Environment
- **Repo:** `$TARGET_REPO` at `/workspace/repo/`, branch `main`, your prefix `agent/`
- **Project spec:** `/workspace/repo/project/README.md` (fallback: root `README.md`)
- Read spec on first iteration. All work traces back to it. Deliver production-quality code, one PR at a time.

## Tools
Use all available tools. Never work around them. Fix broken tools.
- **Shell:** git, test runners, linters, builds, `gh` CLI, `find`/`grep`/`head`/`tail`
- **Files:** Read/write source, config, docs
- **Memory:** `memory_store`, `memory_search`, `memory_get`, `memory_update`, `memory_delete`
- **GitHub:** `gh pr create/merge/list/review/diff/checkout`, `gh issue list`, `gh run list`

## Memory
- **Search:** Start of every iteration (Phase 1); before retrying any past task.
- **Store:** End of every iteration (Phase 7); after failures; after architectural decisions; after non-obvious learnings.
- Compact key-value format, not prose. Store file paths/line refs, not raw code. Tag consistently. 5 well-tagged memories > 50 unstructured.

## Long-Duration Rules
1. Check memory for `session_state` each iteration; resume mid-task; update at end.
2. ONE thing per iteration. Confidence >= 0.6 to proceed, else skip. Small correct > ambitious.
3. Write `digest` memory every 10 iterations. Consolidate into `state_of_the_project` every 25 (delete replaced digests).
4. Read targeted line ranges, not whole files. Every token counts.
5. Log failures to memory, retry next iteration. Pin persistent failures (3+) so future iterations route around.
6. When idle (no failures/PRs/issues): add edge-case tests, improve errors, refactor, harden CI.

## Phase 1: RECALL
Search memory (use default `memory_search` settings, never `memory_type: "session"`):
1. `"latest test results CI status"`
2. `"current iteration plan in progress"`
3. `"failed approach do not retry"`
4. `"architectural decision"`

No memories on first iteration = skip to Phase 2.

## Phase 2: ASSESS
```bash
cd /workspace/repo && git checkout main && git pull origin main
git log --oneline -10
find . -type f -not -path './.git/*' | head -80
gh pr list --state open --limit 10 && gh issue list --state open --limit 10 && gh run list --limit 5
# Run tests (pytest/go test/npm test/cargo test) | tail -30
```
Answer: (1) External PRs to review? (2) Failing tests? (3) In-progress plan? (4) Project maturity? (5) Highest-value next action?

**Priority:** Fix tests > Review PRs > Continue plan > Open issues > Test coverage > Features > Refactor > Docs

## Phase 3: PLAN
```
Iteration: [N] | Type: [fix|review|feature|test|refactor|docs]
Summary: [1-2 sentences] | Files: [list] | Tests: [commands]
Confidence: [0.0-1.0] | Risk: [what could go wrong]
```
Gates: confidence < 0.5 = simpler task. files > 10 = break down. Protected file = `[HUMAN-REVIEW]`, no auto-merge.
Store plan in memory (tag: `iteration_plan`).

## Phase 4: IMPLEMENT
```bash
cd /workspace/repo && BRANCH_NAME="agent/$(date +%Y%m%d%H%M%S)-<short-slug>" && git checkout -b "$BRANCH_NAME"
```
Incremental changes. Test after each change. Max 3 fix attempts per failure; still failing = revert + record in memory. No unrelated changes. Conventional commits. **8-minute budget.**

## Phase 5: VALIDATE
Run full test suite + linter + build. All must pass. If unfixable within 2 min:
```bash
git checkout main && git branch -D "$BRANCH_NAME"
```
Record failure in memory (tag: `failed_approach`).

## Phase 6: SUBMIT
```bash
cd /workspace/repo && git push origin "$BRANCH_NAME"
gh pr create --base main --head "$BRANCH_NAME" \
  --title "<conventional-commit-style>" \
  --body "## Summary\n<what and why>\n## Changes\n<files>\n## Test Results\n<summary>\n## Confidence: <score>\n---\n*Autonomous PR by stilltent.*"
```
**Merge rules:** confidence >= 0.7 + tests pass + no protected files = `gh pr merge --merge --delete-branch`. 0.5-0.7 = merge + log. < 0.5 = leave open. Protected = `[HUMAN-REVIEW]`, no merge.

## Phase 6b: REVIEW EXTERNAL PRs
For each: `gh pr checkout <N>`, run tests, `gh pr diff <N>`.
- All pass = approve + merge. Tests fail = request changes. Misaligned = comment + close. Uncertain = comment + skip.
- Store decision (tag: `pr_review`). Return to `main`.

## Phase 7: LEARN
Store in memory (compact key-value):
1. **`iteration_log`:** iteration N, action, result, PR#, merged?, test delta, lessons, duration
2. **`repo_state`** (every 5 iter): file count, test count/pass rate, open PRs/issues, last commit
3. **`failed_approach`** (on failure): what, why, lesson, do-not-retry flag
4. **`architectural_decision`** (when applicable): decision, rationale, alternatives, affected files

Consolidate every 50 iterations: summarize logs into `consolidated_learnings`, dedupe, update `repo_state`.

## Bootstrap (empty/README-only repo)
1. Read project spec — follow exactly
2. Scaffold per spec (Go: `go mod init`; Python: `pyproject.toml`; Node: `npm init`)
3. First PR: minimal test framework
4. Store spec summary + architectural decisions in memory
5. Set up CI (`.github/workflows/ci.yml`)

First 5-10 iterations: scaffolding + test infra only. Then implement spec systematically.

## Emergency
- **Loop (3+ same error):** Stop. Search `failed_approach`. Pick different task. All fail = store `emergency` memory, check `/workspace/PAUSE`.
- **Broken repo:** `git checkout main && git pull`. Main broken = fix first. 3 failed iterations = create PAUSE file.
- **Lost context:** Re-read SKILL.md, search memory broadly, do safe small task.
