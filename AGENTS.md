# GoFitsV3 repository guidance

## Scope and workflow
- Inspect only the smallest relevant area; use targeted searches and avoid rereading unchanged files.
- For non-trivial work, state the relevant files, intended change, and validation plan before editing.
- Make the smallest practical change. Preserve existing architecture, naming, style, and unrelated user changes; do not expand a fix into cleanup or a broad refactor unless asked.
- Keep responses concise. Summarize long output and report only relevant failures or key lines.

## Implementation
- Prefer the standard library and existing utilities; add dependencies only when they provide clear value.
- Write clear, idiomatic Go with explicit error handling and simple abstractions. Do not use placeholder implementations unless requested.
- Preserve existing frontend patterns and localize state and styling changes.
- Run expensive image processing in a background goroutine with a progress dialog, applying UI updates on the UI thread.
- For any user-visible capability, limitation, supported format or instrument, or workspace behavior change, update `docs/feature-list.md`. Purely internal refactors do not require an inventory update.

## Testing
- Follow `docs/unit-test-standards.md`.
- Add or update tests when behavior changes.
- Run the narrowest relevant test, build, or lint command first. Run the full repository suite only when warranted or requested.
- Report validation commands and results briefly, explaining any broader validation.

## Safety and handoff
- Do not create commits, branches, or PR text unless requested.
- Do not modify secrets, credentials, CI, deployment, or infrastructure unless required by the task.
- Obtain explicit approval before destructive commands and flag risky assumptions before acting.
- On completion, report files changed, behavior changed, validation performed, and remaining risks or follow-up.
