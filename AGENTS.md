# GoFitsV3 repository guidance

GoFitsV3 is a Go/Fyne desktop app that drizzles HST and JWST calibrated exposures into mosaics and composes RGB images. These rules apply to every agent working here; `CLAUDE.md` imports this file.

## Scope and workflow
- Inspect only the smallest relevant area; use targeted searches and avoid rereading unchanged files.
- For non-trivial work, state the relevant files, intended change, and validation plan before editing.
- Make the smallest practical change. Preserve existing architecture, naming, style, and unrelated user changes; do not expand a fix into cleanup or a broad refactor unless asked.
- Keep responses concise. Summarize long output and report only relevant failures or key lines.
- Ask before expanding into refactors, unrelated cleanup, or architecture changes.
- Plans live in `PLAN.md` and `docs/*-plan.md` as numbered checkbox steps. Implement one step per request with `/stage`; never start the next step unprompted. Current in-progress work and open TODO lists are in `docs/STATUS.md`; update it when you pause or finish such work.

## Implementation
- Prefer the standard library and existing utilities; add dependencies only when they provide clear value.
- Write clear, idiomatic Go with explicit error handling and simple abstractions. Do not use placeholder implementations unless requested.
- Preserve existing frontend patterns and localize state and styling changes.
- Run expensive image processing in a background goroutine with a progress dialog, applying UI updates on the UI thread (`fyne.Do`).
- For any user-visible capability, limitation, supported format or instrument, or workspace behavior change, update `docs/feature-list.md`. Purely internal refactors do not require an inventory update.

## Testing
- Follow `docs/unit-test-standards.md`.
- Add or update tests when behavior changes.
- Run `/check` (`python -I scripts/check.py`) before reporting a change complete. It runs gofmt on changed files, `go build ./...`, and vet/test on the changed packages; `--all` runs every package. Run the full suite only when shared code changed or the user asks.
- A PostToolUse hook runs gofmt (goimports when installed) on every edited `.go` file, so do not spend a build round trip on formatting or unused-import errors; if the hook did not run (edits through shell scripts), run `gofmt -w` yourself.
- Report validation commands and results briefly, explaining any broader validation. Checks do not cover what the user must see in the running app; say so.

## Tooling
- `/check`: closing checks. `/commit`: checks + feature-list rule + commit, no push unless asked. `/stage`: one plan step.
- `go run ./cmd/fitsinfo <file> [-all] [-hdu N -stats]`: HDU layout, key headers, WCS and pixel statistics for any FITS file. Use it instead of writing a throwaway header-dump program.
- `fits-triage` agent (read-only, Sonnet): first look at a real-data report (banding, misalignment, a file that will not combine). Use it before fixing anything that involves the user's HST/JWST data so header dumps stay out of the main context.
- Performance or memory investigations ("why is X slow / using 4 GB") start in an `Explore` subagent that returns the suspect functions and call sites, not file contents.

## Large files (read by range only)
- `internal/mosaic/drizzle.go` (~3.5k lines): never Read whole. `Grep -n` for the func/symbol first, then Read with `offset`/`limit` (<=200 lines).
- Compose tab = `composeWorkspace` struct (`internal/ui/compose_workspace.go`) built by `newComposeWorkspace` (`workspace_compose.go`, state + callback wiring only). Methods by area in `internal/ui/compose_*.go`: `_project` (save/load), `_export` (headers, RGB export), `_channels` (load channel/L, send to edit), `_large_mode`, `_overlay_ui` (layer windows), `_blink_ui`, `_picker`, `_stretch_ui` (match/normalize/levels), `_weights_ui`, `_align_ui`, `_cross_channel_ui` / `_cross_channel_large`, `_render` (refresh, composeRGB), `_reset`, `_menus` (updateMenus), `_layout` (buildMenus/buildLayout), `_filter_set`, `_magic_ui`.
- Pure helpers: `_load`, `_channel_state`, `_channel_controls`, `_preview`, `_blink`, `_overlay`, `_offset`, `_stretch_match`, `_large_stretch`, `_cross_channel_replay`, `_widgets`. History: `docs/compose-workspace-refactor-plan.md`.
- Mosaic tab = `mosaicWorkspace` struct (`internal/ui/mosaic_workspace.go`), constructor in `workspace_mosaic.go`, methods in `internal/ui/mosaic_*.go`.

## Safety and handoff
- Do not create commits, branches, or PR text unless requested.
- Do not modify secrets, credentials, CI, deployment, or infrastructure unless required by the task.
- Obtain explicit approval before destructive commands and flag risky assumptions before acting.
- On completion, report files changed, behavior changed, validation performed, and remaining risks or follow-up.
