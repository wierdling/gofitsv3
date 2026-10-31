---
name: commit
description: Create a git commit for the current GoFitsV3 changes the way this repo does it: run /check first, confirm docs/feature-list.md covers any user-visible change, write a concise message in the repo's style, commit, and never push unless the user asked for a push in the same message. Use when the user asks for a commit, check-in, or a commit message.
---

# /commit

1. Run `git status --short` and `git diff --stat`. Refuse to continue if the tree is clean or if generated or data files are staged (`*.exe`, `TestImages/`, `working/`, `output/`, `tmp/`, anything over 1 MB). Report those instead.
2. Run `/check` (`python -I scripts/check.py`). Stop and report if any step fails; do not commit red.
3. Feature inventory rule (AGENTS.md): if the diff changes a user-visible capability, limitation, supported format or instrument, or workspace behavior, `docs/feature-list.md` must change too. If it does not, say which bullet needs updating and update it before committing. Pure refactors and internal fixes skip this.
4. Write the message in the existing style (`git log --oneline -15` shows it): an imperative title under 72 characters, optionally prefixed by area (`mosaic:`, `align:`, `ui/compose:`), then a short body with one bullet per behavior change and the validation run. End with the attribution line the session provides.
5. `git add -A` only after step 1 confirmed nothing unwanted is untracked; otherwise add paths explicitly. Commit on the current branch. If the current branch is `develop` and the user did not ask for a commit on develop, say so before committing.
6. Do not push. If the user's request included "push", push after the commit and report the remote result.
7. Report: commit hash, title, files changed, checks run, and anything left uncommitted.
