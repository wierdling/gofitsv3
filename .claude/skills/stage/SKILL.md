---
name: stage
description: Implement exactly one unchecked step of a staged plan document (PLAN.md or docs/*-plan.md) in GoFitsV3, run /check, tick the box, and stop. Use when the user says "run stage N", "do the next step of the plan", or names a plan file. Never implements more than one step per invocation.
---

# /stage [plan-file] [step-number]

GoFitsV3 work is planned in numbered, checkbox-tracked stages (`1. [ ] ...`). The user implements one stage per request and sometimes continues with a different model, so each stage must leave the repo green and the plan file truthful.

1. Resolve the plan file: the argument, else `PLAN.md`. Read it. Pick the step the user named, else the first `[ ]` step. Restate the step's files, change and "done when" criteria in two or three lines before editing.
2. Read `AGENTS.md`. Make the smallest change that satisfies the step's "done when" criteria and nothing from later steps. If a later step's work is unavoidable, stop and say so instead of doing it.
3. Add or update tests as the step lists, following `docs/unit-test-standards.md`. Prefer deterministic fixtures and the bounded-behavior assertions the plan describes over heap measurements.
4. Run `/check` (`python -I scripts/check.py`, with `--pkg` for the step's packages). Fix until green.
5. Tick the step (`[x]`) and append a one-line `Done <YYYY-MM-DD>:` note under it with any deviation from the plan text. Do not rewrite other steps. If the step revealed that a later step is wrong, add a `Note:` line under that step rather than changing its scope.
6. Stop. Report files changed, what changed, validation, and what the user should verify in the running app. Do not start the next step.
