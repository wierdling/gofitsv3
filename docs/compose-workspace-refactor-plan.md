# Compose workspace refactor plan

Goal: break up `newComposeWorkspace` in `internal/ui/workspace_compose.go` (~5,250 lines,
~95 closures, ~650 locals in one scope) into a `composeWorkspace` struct with methods spread
across `internal/ui/compose_*.go` files. Behavior must not change.

This document is self-contained so any assistant can pick it up. Line numbers are as of
2026-09-22 and will drift. Always locate code with `grep -n "<name> :\?= func" internal/ui/workspace_compose.go`.

## Already done
- [x] Stage 0: top-level helpers moved out of `workspace_compose.go` into `compose_load.go`,
  `compose_channel_state.go`, `compose_channel_controls.go`, `compose_preview.go`,
  `compose_blink.go`, `compose_overlay.go`, `compose_offset.go`, `compose_stretch_match.go`,
  `compose_large_stretch.go`, `compose_cross_channel_replay.go`, `compose_widgets.go`
  (pure moves, only imports touched).

## Pattern to follow
The Mosaic workspace already went through this: see `type mosaicWorkspace struct` in
`internal/ui/mosaic_workspace.go` and its methods in `workspace_mosaic.go`. Copy that style.

1. Create `type composeWorkspace struct` in a new file `internal/ui/compose_workspace.go`.
   At the top of `newComposeWorkspace` do `ws := &composeWorkspace{win: win}` (receiver `ws`, matching `mosaicWorkspace`).
2. Convert **one area per step** (below). For each closure in the area:
   - Turn `foo := func(args) {...}` into `func (ws *composeWorkspace) foo(args) {...}` in the
     area's file.
   - Every captured local it reads/writes that is shared with code still inside
     `newComposeWorkspace` becomes a field on the struct. Replace references with `ws.field`
     both in the method and in the remaining function body.
   - Forward-declared closures (`var refresh func()` then `refresh = func...`) become methods;
     delete the `var` and change callers to `ws.refresh()`.
   - Callers that pass the closure as a value (`widget.NewButton("X", foo)`) become
     `ws.foo` (method value).
3. Move variables into the struct only when a converted method needs them. Do not move
   everything at once.
4. Watch for loop variables captured by closures (`i := i`) and for closures that shadow
   an outer name. Keep goroutine / `fyne.Do` boundaries exactly as they are.
5. Package-level hooks (`globalSendToChannel`, `globalComposeLargeCleanup`,
   `composeLargeModeActive`, `globalSelectComposeTab`) stay as package vars but are assigned
   method values, e.g. `globalSendToChannel = ws.sendToChannel`.

## Validation for every step
- `gofmt -l internal/ui/` (no new entries), `go build ./...`, `go vet ./internal/ui/`
- `go test ./internal/ui/ -count=1`
- Launch the app, open Compose, and try the converted area by hand (UI wiring bugs are
  not caught by tests).
- Add a unit test for the new method(s) where practical: construct a `composeWorkspace`
  with fake `models.LoadedImage` values and call the method directly.
- Commit after each area so it can be reverted independently.

## Areas to convert (recommended order: least coupled first)

Each line: closures (approx. start line) -> target file.

- [x] **1. Shared state struct** — `internal/ui/compose_workspace.go` created;
  `ws := &composeWorkspace{win: win}` at top of `newComposeWorkspace`. Lifted every local
  captured by the step-2 closures (48 names) into fields and rewrote all references to
  `ws.<name>` (type-checked rewrite, no logic changes). Closures from other areas that
  step 2 calls are temporary func-typed fields under "closures not yet converted"; when an
  area is converted, replace its field with a method and delete the field.
  To find which outer locals a closure captures, check which non-`ws.` identifiers declared
  in `newComposeWorkspace` it references; lift those into the struct first.
- [x] **2. Project save/load** — `saveProject`, `loadProject`, `captureViewportStates`,
  `restoreViewportStates` and the `vpState` type are now methods/types in
  `internal/ui/compose_project.go`; callers use `ws.<name>`. Test:
  `compose_project_test.go` (viewport state round-trip). Manually verified by user
  (load/edit/save project) after step 1; re-verify after this step.
- [x] **3. Headers & per-channel save** -> `compose_export.go` (+ `compose_export_test.go`).
- [x] **4. Channel loading** -> `compose_channels.go`; `globalSendToChannel = ws.sendToChannel`
  (+ `compose_channels_test.go`).
- [x] **5. Large-file mode bookkeeping** -> `compose_large_mode.go`;
  `globalComposeLargeCleanup = ws.cleanupLargeMode`, `composeLargeModeActive = ws.isLargeModeActive`
  (+ `compose_large_mode_test.go`).
- [x] **6. Overlay layers** -> `compose_overlay_ui.go` (+ `compose_overlay_ui_test.go`).
- [x] **7. Blink** -> `compose_blink_ui.go` (+ `compose_blink_ui_test.go`).
- [x] **8. Picker & measurement** -> `compose_picker.go` (`composePicker` type moved to package
  level) (+ `compose_picker_test.go`).
- [x] **9. Stretch matching / normalize** -> `compose_stretch_ui.go` (+ `compose_stretch_ui_test.go`).
- [x] **10. Weights & star neutralize** -> `compose_weights_ui.go` (+ `compose_weights_ui_test.go`).
- [x] **11. Alignment** -> `compose_align_ui.go` (+ `compose_align_ui_test.go`).
- [x] **12. Cross-channel clean** -> `compose_cross_channel_ui.go` (entry + resident path) and
  `compose_cross_channel_large.go` (`crossChannelCleanLarge`, the former large-mode branch)
  (+ `compose_cross_channel_ui_test.go`).
- [x] **13. Render pipeline** -> `compose_render.go` (+ `compose_render_test.go`).
- [x] **14. Reset / clear** -> `compose_reset.go` (no unit test: every path sits behind a
  confirm dialog).
- [x] **15. Menus** -> `updateMenus` in `compose_menus.go` (+ `compose_menus_test.go`).
- [x] **16. Layout** -> `compose_layout.go`: `buildMenus()` (menu items + File/Compose/View
  menus) and `buildLayout()` (controls panel, viewport grid, maximize/restore). Two large
  inline handlers not listed above were also converted: "Load Filter Set..." ->
  `compose_filter_set.go` (`loadFilterSet`) and the Magic button -> `compose_magic_ui.go`
  (`runMagicAll`). `newComposeWorkspace` now only builds state and wires callbacks.

### Notes for reviewers
- All moves were mechanical (type-checked rename of captured locals to `ws.<field>`, closure ->
  method). Statement order inside `newComposeWorkspace` is unchanged; `buildMenus()` runs where
  the menu code was and `buildLayout()` runs last, as before.
- Former forward-declared closures had `if ws.x != nil` guards. As methods they are always
  non-nil, so `go vet` flagged the guards and they were removed. Each was checked: every
  construction-time call site runs after the widgets the method touches are created.
- `composeWorkspace` fields are grouped by area in `compose_workspace.go`.

## Done when (met 2026-09-22)
- `newComposeWorkspace` is under ~300 lines.
- No file in `internal/ui/compose_*.go` is over ~800 lines.
- The file map in `CLAUDE.md` ("Large files" section) is updated to the new files.
