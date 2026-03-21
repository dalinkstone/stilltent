# SKILL.md — stilltent Autonomous Loop

## Guiding Documents
- **`/workspace/LEARNING.md`** — Read this on your first iteration. It defines HOW you get better at building software across iterations. It is as important as this file.
- **`/workspace/AGENTS.md`** — Your identity, constraints, and principles.

## Environment
- **Repo:** `$TARGET_REPO` at `/workspace/repo/`, branch `main`, your prefix `agent/`
- **Project spec:** `/workspace/repo/project/README.md` — this is your ONLY source of truth for what to build.
- **Working directory:** ALL implementation code, tests, and configs go inside `/workspace/repo/project/`. NEVER create or modify files outside this directory.
- **Off-limits:** `orchestrator/`, `workspace/`, `scripts/`, `config/`, `dockerfiles/`, `embed-service/`, `mnemo-server/`, `docs/`, `Makefile`, `docker-compose.yml`, `.env*`, root `README.md`. These are infrastructure that runs you — do NOT touch them.
- Read the project spec (`/workspace/repo/project/README.md`) on your FIRST iteration. All work traces back to it. Deliver production-quality code inside `/workspace/repo/project/`, one PR at a time.

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
- **Development memories:** Also store `iteration_plan`, `iteration_result`, `project_status`, `improvement_queue`, `self_reflection` — see LEARNING.md for full details.

## Long-Duration Rules
1. Check memory for `session_state` each iteration; resume mid-task; update at end.
2. ONE thing per iteration. Confidence >= 0.6 to proceed, else skip. Small correct > ambitious.
3. Write `digest` memory every 10 iterations. Consolidate into `state_of_the_project` every 25 (delete replaced digests).
4. Read targeted line ranges, not whole files. Every token counts.
5. Log failures to memory, retry next iteration. Pin persistent failures (3+) so future iterations route around.
6. When idle (no failures/PRs/issues): **build the next feature from the SOUL.md roadmap.** If the roadmap is complete, work the improvement queue. Do NOT write standalone tests, docs, or refactors when idle. Build features.
7. **Every 5th iteration:** Work one item from the improvement queue instead of new features. You are an engineer who revisits and improves past work — not a script that only moves forward.
8. **Every 10th iteration:** Perform a self-reflection (see LEARNING.md). Evaluate your recent development efficiency — are you shipping features faster? Store as `self_reflection`.
9. **Every 25th iteration:** Knowledge consolidation — synthesize what you've learned about the codebase, architecture, and development patterns. Review the improvement queue. Store as `consolidated_learnings`. See LEARNING.md.
10. **Every 50th iteration:** Deep review — re-read the spec entirely, compare to current state, assess roadmap progress, set feature priorities for next 50 iterations. See LEARNING.md.

## Phase 1: RECALL
Search memory (use default `memory_search` settings, never `memory_type: "session"`):
1. `"current iteration plan in progress"`
2. `"failed approach do not retry"`
3. `"architectural decision"`
4. `"improvement_queue"` — check for queued improvements to revisit
5. `"project_status"` — know the current roadmap position before deciding what to build
6. `"self_reflection"` — recall your most recent development efficiency insights (every 10th iteration or when stuck)

No memories on first iteration = read LEARNING.md first, then skip to Phase 2.

## Phase 2: ASSESS
```bash
cd /workspace/repo && git checkout main && git pull origin main
git log --oneline -10
# Check: what percentage of recent commits are feat: commits?
git log --oneline -10 | grep -c "^[a-f0-9]* feat:" || true
find project/ -type f -name "*.go" | head -80
# Check what directories exist vs what the roadmap requires:
ls -la project/internal/
# These directories MUST exist (from SOUL.md roadmap): hypervisor/, virtio/, boot/, image/, network/policy.go, compose/, sandbox/
# If they don't exist, that is your next feature.
gh pr list --state open --limit 10 && gh issue list --state open --limit 10
cd project && go build ./... 2>&1 | tail -20
```
Answer: (1) External PRs to review? (2) Build broken? (3) In-progress plan? (4) Project maturity? (5) What is the next feature on the roadmap in SOUL.md?

**Priority:** Fix broken build > Review PRs > Continue plan > **Build the next feature from the SOUL.md roadmap** > Open issues > Improve existing features

**BANNED priorities:** Do NOT spend iterations on: standalone tests, documentation, refactoring, or test coverage. These are not valid iteration goals. Tests accompany features in the same PR. Docs come after the tool works.

## Phase 3: PLAN
```
Iteration: [N] | Type: [feature|fix|improve]
Summary: [1-2 sentences — what FEATURE this builds] | Files: [list]
Confidence: [0.0-1.0] | Risk: [what could go wrong]
Goal: [What this code will do when done — e.g., "hypervisor.Backend interface defined, compiles on darwin and linux"]
Source: [roadmap-phase-N-item-X | improvement-queue IQ-XXX | failed-approach-retry]
```
Gates: confidence < 0.5 = simpler task. files > 10 = break down. Protected file = `[HUMAN-REVIEW]`, no auto-merge.
Store plan in memory (tag: `iteration_plan`).

**TYPE MUST BE `feature` IN 90%+ OF ITERATIONS.** If your planned type is not `feature`, ask: "What feature does this enable?" If the answer is none, change your plan to build the next feature from the SOUL.md roadmap.

**Choosing what to work on:**
- Is the build broken? → Fix it (this counts as `fix`, which is allowed).
- Otherwise → **Build the next feature from the SOUL.md roadmap.** Read the roadmap. Find the first incomplete item. Build it.
- Every 5th iteration → Revisit and improve a past feature (this counts as `improve`).
- Do NOT choose to write standalone tests, docs, or refactors. These are not valid iteration goals.

## Phase 4: IMPLEMENT
```bash
cd /workspace/repo && BRANCH_NAME="agent/$(date +%Y%m%d%H%M%S)-<short-slug>" && git checkout -b "$BRANCH_NAME"
```
Incremental changes. Build after each change to verify it compiles. Max 3 fix attempts per failure; still failing = revert + record in memory. No unrelated changes. Conventional commits. **8-minute budget.**

## Phase 5: VALIDATE
Run build + linter + **darwin cross-compile check** (`GOOS=darwin go vet ./...`). All must pass. If unfixable within 2 min:
```bash
git checkout main && git branch -D "$BRANCH_NAME"
```
Record failure in memory (tag: `failed_approach`).

## Phase 6: SUBMIT
```bash
cd /workspace/repo && git push origin "$BRANCH_NAME"
gh pr create --base main --head "$BRANCH_NAME" \
  --title "<conventional-commit-style>" \
  --body "## Summary\n<what feature this implements and why>\n## Changes\n<files>\n## Build Status\n<compiles clean on linux and darwin>\n## Confidence: <score>\n---\n*Autonomous PR by stilltent.*"
```
**Merge rules:** confidence >= 0.7 + build passes + no protected files = `gh pr merge --merge --delete-branch`. 0.5-0.7 = merge + log. < 0.5 = leave open. Protected = `[HUMAN-REVIEW]`, no merge.

## Phase 6b: REVIEW EXTERNAL PRs
For each: `gh pr checkout <N>`, build the code, `gh pr diff <N>`.
- Build passes = approve + merge. Build fails = request changes. Misaligned with spec = comment + close. Uncertain = comment + skip.
- Store decision (tag: `pr_review`). Return to `main`.

## Phase 7: LEARN
Store in memory (compact key-value):
1. **`iteration_result`:** iteration N, what you built, result, PR#, merged?, roadmap item advanced, lessons, duration
2. **`project_status`** (every 5 iter): file count, roadmap phase, features complete, open PRs/issues, last commit, feat_commit_ratio, darwin_build_clean
3. **`failed_approach`** (on failure): what, why, lesson, do-not-retry flag
4. **`architectural_decision`** (when applicable): decision, rationale, alternatives, affected files

**Review what you shipped (from LEARNING.md):**
5. **`insight`** (when an approach worked well): What worked, why, and how to reuse it.
6. **`improvement_queue`:** After every PR, ask: "What could be built better?" Add items with priority, area, and rationale.

**Forward ratchet:** Before storing `project_status`, compare to the previous entry. Roadmap progress must always advance. Build must stay clean. If anything regressed, add a high-priority item to the improvement queue to fix it.

**Self-reflection** (every 10th iteration): See LEARNING.md for the full protocol. Store as `self_reflection`.

Consolidate every 25 iterations: summarize what you've learned into `consolidated_learnings`, dedupe, update `project_status`, review improvement queue, re-prioritize.

## Bootstrap (empty/README-only project)
1. Read project spec at `/workspace/repo/project/README.md` — follow exactly
2. Read the SOUL.md roadmap — this is your feature priority list
3. Scaffold inside `/workspace/repo/project/` per spec (Go: `cd /workspace/repo/project && go mod init`)
4. **First PR: the first feature from the SOUL.md roadmap** (e.g., create `internal/hypervisor/backend.go` with the Backend interface)
5. Store spec summary + architectural decisions in memory

First 2-3 iterations: scaffold + start building features from the roadmap immediately. Do NOT create PRs for test frameworks, CI setup, or documentation. Build the tool. ALL code goes in `/workspace/repo/project/`.

## Emergency
- **Loop (3+ same error):** Stop. Search `failed_approach`. Follow creative escalation from LEARNING.md (reframe → decompose → invert → research → pivot). All approaches exhausted = store `emergency` memory, check `/workspace/PAUSE`.
- **Broken repo:** `git checkout main && git pull`. Main broken = fix first. 3 failed iterations = create PAUSE file.
- **Lost context:** Re-read SKILL.md and LEARNING.md, search memory broadly, do safe small task.
- **Quality regression:** If the build breaks for 3+ iterations, fix it before doing anything else. But NEVER pause feature work to write tests — that is not a quality improvement, it is a distraction.
- **Feature stall:** If you have gone 3+ iterations without a `feat:` commit, you are stalled. Re-read the SOUL.md roadmap, pick the next item, and build it. No excuses.
