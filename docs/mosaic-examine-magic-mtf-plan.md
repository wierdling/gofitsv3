# Plan: Add Magic and MTF Controls to Mosaic and Examine

## Goal

Bring the Mosaic and Examine stretch controls to feature parity with Compose for:

- **Magic** level estimation
- Magic presets: **Balanced**, **Nebula**, and **Galaxy**
- **MTF** as a stretch mode
- **MTF midtone** editing
- **Auto MTF**

Use the existing `processing.ApplyMagicLevels` and `processing.AutoMTFMidtone`
helpers. Do not duplicate their image-analysis logic in the UI.

## Existing behavior to preserve

- Compose's Magic button applies `processing.ApplyMagicLevels` to a
  `models.LoadedImage`. It updates `Background`, `Peak`, `Black`, and `White`
  without changing the selected stretch mode.
- Compose's Auto MTF button calls `processing.AutoMTFMidtone`. That helper
  calculates usable levels if needed, sets `MTFMidtone`, and selects MTF mode.
- Mosaic previews are rendered from temporary `models.LoadedImage` values in
  `buildMosaicPreviewImageWithLevels`.
- Examine renders its `state.img` directly through `processing.ApplyStretchParallel`.
- Existing saved Mosaic filter preferences must continue to load if they do not
  contain an MTF-midtone field.

## Relevant files

| File | Responsibility |
| --- | --- |
| `internal/ui/workspace_mosaic.go` | Mosaic stretch controls and button wiring |
| `internal/ui/mosaic_workspace.go` | Mosaic workspace state and widget references |
| `internal/ui/mosaic_levels.go` | Mosaic level application, Auto Scaling, and per-filter preferences |
| `internal/ui/mosaic_helpers.go` | Turns Mosaic levels/mode into a temporary `LoadedImage` for preview rendering |
| `internal/ui/mosaic_build.go` | Initial Mosaic preview rendering |
| `internal/ui/mosaic_fileload.go` | Mosaic blink preview rendering |
| `internal/ui/mosaic_stars.go` | Star-selection preview rendering |
| `internal/ui/mosaic_measure.go` | Measurement preview rendering |
| `internal/ui/artifact_mask_editor.go` | Artifact-mask preview rendering |
| `internal/ui/workspace_examine.go` | Examine stretch UI, reload state, and Compose handoff |
| `internal/ui/workspace_compose.go` | Reference implementation for UI behavior only |
| `internal/processing/processing.go` | Existing `AutoMTFMidtone` implementation |
| `internal/processing/magic_levels.go` | Existing Magic estimation implementation |

## Implementation steps

### 1. Add Mosaic MTF state and controls

In `internal/ui/mosaic_workspace.go`:

- Add a `mtfMidtoneEntry *NumberEntry` field alongside the other level-entry
  widget references.
- Add a `mtfMidtone float64` field to hold the current value used by all Mosaic
  preview paths. Initialize it to `stretch.DefaultMTFMidtone` when constructing
  the workspace.

In `internal/ui/workspace_mosaic.go`:

- Add `MTF` to `modeSelect` after `HistEq`.
- Map the `MTF` label to `stretch.MTF` in the selection callback. Keep all
  existing mappings unchanged.
- Create `mtfMidtoneEntry := NewNumberEntry(0.01, 3)` and set it to
  `stretch.DefaultMTFMidtone`.
- Put the entry in a labelled `MTF midtone` form row.
- Implement a small local `updateStretchParams` function which shows the MTF
  row only when `ws.stretchMode == stretch.MTF`; call it after every mode
  selection and after setting the initial selection.
- Store the entry in `ws.mtfMidtoneEntry` and update `ws.mtfMidtone` whenever
  Apply Values is pressed.

This should be a localized extension of the existing Preview Levels section;
do not reorganize Mosaic/Drizzle or baseline-reference controls.

### 2. Add Mosaic Magic and Auto MTF actions

Add the following controls next to the existing `Auto Scaling` and
`Apply Values` buttons in `internal/ui/workspace_mosaic.go`:

- `Auto MTF`
- `Magic`
- A Magic preset select containing `Balanced`, `Nebula`, and `Galaxy`, defaulted
  to `Balanced`

Use the same preset labels as Compose and pass the selected label through
`processing.ParseMagicPreset`.

Implement reusable Mosaic helpers in `internal/ui/mosaic_levels.go` rather
than embedding the image construction in multiple button handlers:

- A helper that returns the currently displayable source result: use
  `ws.state.result`, otherwise use `ws.starModeRefResult` while in star mode.
- A helper that constructs a `models.LoadedImage` from a `mosaic.Result`,
  including its pixels, width, height, current black/white/background/peak/
  scaled-peak values, stretch mode, and MTF midtone.
- `ws.autoMTFLevels(result *mosaic.Result)`: call
  `processing.AutoMTFMidtone` on that temporary image; copy the resulting
  background, peak, scaled peak, black, white, and MTF midtone back into the
  controls/workspace state; select `MTF`; refresh the preview.
- `ws.magicLevels(result *mosaic.Result, preset processing.MagicPreset)`: call
  `processing.ApplyMagicLevels`; copy background, peak, black, and white back
  into the controls/workspace state; refresh the preview. Leave MTF mode and
  MTF midtone unchanged, as Compose does.

The existing `autoLevels` helper may remain for FITS-Liberator Auto Scaling.
Keep `levelsSet` behavior consistent with the current Auto Scaling path.

### 3. Carry MTF midtone through every Mosaic preview path

Change `buildMosaicPreviewImageWithLevels` in `internal/ui/mosaic_helpers.go`
to accept an MTF midtone argument and assign it to the temporary
`models.LoadedImage.MTFMidtone`.

Update every caller to pass `ws.mtfMidtone`:

- normal build preview in `mosaic_build.go`
- normal and reference preview updates in `mosaic_levels.go`
- blink images in `mosaic_fileload.go`
- star-selection previews in `mosaic_stars.go`
- measurement previews in `mosaic_measure.go`
- artifact-mask editor preview in `artifact_mask_editor.go`

This is required because adding MTF to the selector alone would not affect
Mosaic renders: the render pipeline currently receives no MTF-midtone value.

### 4. Persist Mosaic MTF settings per filter

In `internal/ui/mosaic_levels.go`:

- Add `MTFMidtone string \`json:"mtfMidtone,omitempty"\`` to
  `mosaicSavedLevels`.
- In `saveLevelPrefs`, persist `ws.mtfMidtone` using the same stable numeric
  formatting used for other saved levels.
- In `loadLevelPrefsAndMode`, parse `MTFMidtone` only when present and valid.
  If it is absent, invalid, non-positive, or outside the valid `(0,1)` range,
  use `stretch.DefaultMTFMidtone`.
- Synchronize the MTF entry after load, then set the saved mode. This preserves
  compatibility with old preference JSON that has no MTF field.

When setting a new active filter or clearing the workspace, reset the MTF
midtone to the default and synchronize its entry unless saved preferences are
loaded for that filter. This prevents an unrelated filter's midtone from
silently carrying over.

### 5. Add Examine MTF controls

In `internal/ui/workspace_examine.go`:

- Add `MTF` to `modeSelect`.
- Create `mtfMidtoneEntry := NewNumberEntry(0.01, 3)`, initialized to
  `stretch.DefaultMTFMidtone`.
- Add a labelled `MTF midtone` row to Stretch Controls.
- Add an `updateStretchParams` function that shows the midtone row only when
  the selected mode is MTF.
- In the mode selector callback, update `state.img.Mode`, update row visibility,
  and refresh as the current code does.
- In `syncControlsFromImage`, load `state.img.MTFMidtone`; if it is invalid,
  display the default. Also ensure row visibility matches the synchronized mode.
- In Apply Values, write `mtfMidtoneEntry.Value()` to `state.img.MTFMidtone`
  before refreshing.

Use the existing `labelToMode` and `modeToLabel` helpers. They already support
MTF and should not be duplicated.

### 6. Add Examine Magic and Auto MTF actions

Add `Auto MTF`, `Magic`, and the same Magic preset selector to the Examine
Stretch Controls section.

- `Auto MTF` should return immediately if `state.img` is nil. Otherwise call
  `processing.AutoMTFMidtone(state.img)`, synchronize Background/Peak/Scaled
  Peak/Black/White and MTF midtone controls from the image, select `MTF`, and
  refresh.
- `Magic` should return immediately if `state.img` is nil. Otherwise call
  `processing.ApplyMagicLevels(state.img,
  processing.ParseMagicPreset(magicPreset.Selected))`, synchronize
  Background/Peak/Black/White controls, and refresh.
- Magic must not change mode or MTF midtone.

Use tooltip wording consistent with the existing Examine buttons where useful;
for example, describe Auto MTF as calculating a PixInsight-style starting
midtone and Magic as estimating levels for the selected target preset.

### 7. Preserve Examine MTF state across reload and Compose handoff

`workspace_examine.go` already saves `models.ChannelState` before reloading a
FITS file. Extend its preserve-stretch restoration code to assign:

```go
state.img.MTFMidtone = savedState.MTFMidtone
```

`ChannelState` already contains this field, so no model/schema change is
needed.

For Send to Compose, the image copy already retains `MTFMidtone`; ensure the
copy takes all current UI values before sending, including:

```go
imgCopy.MTFMidtone = mtfMidtoneEntry.Value()
```

This ensures that an MTF stretch edited in Examine looks the same after being
sent to a Compose channel.

### 8. Tests

Keep tests narrow and behavior-focused, following `docs/unit-test-standards.md`.

Add or extend tests in `internal/ui` for:

- Mosaic saved-level JSON with MTF midtone round-tripping correctly.
- Older Mosaic saved-level JSON without `mtfMidtone` loading with
  `stretch.DefaultMTFMidtone`.
- Invalid saved MTF midtones falling back to the default.
- Examine/Compose state restoration retaining a valid `MTFMidtone` where the
  existing state helper tests can cover it without testing widget layout.

No new processing tests are needed unless UI work exposes a defect in
`AutoMTFMidtone` or `ApplyMagicLevels`: both helpers already have focused
processing coverage.

## Validation sequence

Run the narrowest checks first:

```powershell
go test ./internal/ui
go test ./internal/processing -run "Test(Magic|AutoMTF)"
```

Then perform a manual UI smoke test:

1. In Mosaic, build or load a result, choose MTF, edit MTF midtone, and apply.
   Confirm the preview changes.
2. Click Auto MTF and verify it chooses MTF mode, populates the midtone field,
   and refreshes the preview.
3. Run Magic with each preset. Confirm Background/Peak and Black/White update,
   the preview refreshes, and the selected stretch mode is unchanged.
4. Switch Mosaic filters/reload the application and confirm the saved MTF mode
   and midtone restore for the filter.
5. In Examine, repeat the MTF, Auto MTF, and Magic checks for both a FITS file
   and a Mosaic result sent from Mosaic.
6. Reload an examined FITS with “Reload Current FITS” and confirm MTF midtone
   is preserved.
7. Send an MTF-stretched Examine image to Compose and confirm its selected mode
   and midtone match.

## Scope boundaries

- Do not alter the algorithms in `internal/processing`.
- Do not add project-level Mosaic persistence for preview levels; preserve the
  existing per-filter preferences design.
- Do not add unrelated stretch controls (for example GHS) to Mosaic or Examine
  as part of this change.
- Do not refactor the existing Compose controls; treat them as the behavior and
  label reference.
