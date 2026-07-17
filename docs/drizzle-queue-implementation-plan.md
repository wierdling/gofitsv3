# Drizzle Queue Implementation Plan

## Goal

Add a transient, sequential drizzle queue to the Mosaic workspace so a user can select several saved mosaic projects, choose whether each project should run automatic star alignment, and leave the application to process them unattended.

Each successful job must write exactly one final FITS mosaic, release its image data, and move to the next job. A failed job must not stop later jobs. The queue does not survive an application restart.

This document is intended as an implementation handoff. Follow the repository's `AGENTS.md` and `docs/unit-test-standards.md`; keep the UI wiring thin and put branching behavior in small testable helpers.

## Confirmed Product Decisions

- A queue job is based on a saved Mosaic project JSON file.
- Automatic alignment is enabled or disabled independently for each job.
- When alignment is disabled, use the offsets and affine transforms stored in the project unchanged.
- When alignment is enabled, use that project's saved `AlignmentSettings`.
- Output is saved beside the Mosaic project file.
- Output name is `<filter>_<dateTime>_drizzle.fits`.
- Use local time and the filesystem-safe timestamp format `20060102_150405`, for example `F606W_20260716_214530_drizzle.fits`.
- Existing output should not normally be possible because of the timestamp. Never intentionally overwrite an existing file; if the computed path exists, fail that job and continue.
- Jobs run strictly one after another. Do not parallelize jobs.
- A failed job does not stop the queue. Record the error, continue, and prominently notify the user after the queue finishes.
- The queue is session-only. Do not persist it and do not add restart/resume behavior.
- Do not render a preview, calculate a histogram, update the Mosaic result, or retain the result after saving.
- Do not keep completed job image data in memory.

## Recommended Safety Semantics

The unattended path must favor a visible failure over silently writing a questionable or incomplete mosaic.

- Project JSON parse failure: fail the job.
- Missing/unreadable project input or reference file: fail the job. Do not drizzle a partial subset.
- Alignment API error: fail the job.
- Alignment enabled but any active, unlocked, non-reference input has no applied alignment result: fail the job before drizzle. Include the affected filenames and alignment errors in the job message.
- Inputs whose saved offsets are locked remain unchanged and do not count as alignment failures.
- Drizzle build or FITS save failure: fail the job.
- Cancellation of the current job is not a failure; mark it `Cancelled`. See cancellation behavior below.

Do not add an "allow partial alignment" mode in the first implementation. It is too easy for an unattended run to produce an apparently successful but misregistered FITS file.

## Important Existing-Code Findings

The current code already supplies most processing primitives:

- `internal/ui/mosaic_project.go` serializes and interactively loads `models.MosaicProject`.
- `internal/ui/mosaic_build.go` converts saved settings into `mosaic.Options`, calls `mosaic.Build`, and saves an optional preview.
- `internal/mosaic/drizzle.go` contains cancellable `Build`, `AlignInputsByStarsWithMode`, `AlignmentStreamsPixels`, and `SaveResultFITS`.
- `internal/mosaic/exposure_combine.go` contains cancellable `LoadInputsForPipeline` and the combined-exposure cache behavior.
- `internal/ui/progress_dialog.go` shows the current cancellable single-operation progress dialog.
- `internal/ui/workspace_mosaic.go` contains the Mosaic menu and current interactive alignment workflow.
- Fyne 2.7.3 supports OS notifications through `fyne.App.SendNotification(fyne.NewNotification(...))`.

Do not implement the queue by repeatedly calling `loadMosaicProject` and `buildDrizzlePreview`. Both methods are UI-oriented and mutate the visible workspace. Extract reusable non-UI helpers, then have the existing interactive code and the new queue call those helpers.

### Saved-project fidelity gap to fix

`MosaicInputState` currently saves offsets/transforms but not the input's `Excluded`, `NormalizeExposure`, or `ExposureScale` fields. `MosaicProject` also does not save the selected exposure-normalization mode. Therefore a saved project can currently reload differently from the workspace that produced it.

Fix this as part of the queue feature so newly saved projects are reproducible:

```go
type MosaicInputState struct {
    // existing fields...
    Excluded          bool    `json:"excluded,omitempty"`
    NormalizeExposure bool    `json:"normalizeExposure,omitempty"`
    ExposureScale     float64 `json:"exposureScale,omitempty"`
}

type MosaicProject struct {
    // existing fields...
    ExposureNormMode int `json:"exposureNormMode,omitempty"`
}
```

Update project save, `applyInputState`, and interactive load to round-trip those values. Zero values remain backward-compatible with older project files: all inputs included, normalization off, scale zero/default. Add model round-trip tests and a focused apply-state test. Do not otherwise redesign the project format.

## User Experience

Add `Drizzle Queue...` to the existing Mosaic menu in `internal/ui/workspace_mosaic.go`, separated from the settings/actions as appropriate. It opens a non-modal queue window so the user can continue viewing the main window, but Mosaic actions that could start another build/alignment should be disabled while the queue is running.

The queue window should contain:

- A table/list with columns or clearly aligned fields:
  - order
  - project filename
  - filter
  - `Align` checkbox
  - status
  - output filename after it is known
  - concise error text for failed jobs
- `Add Project...` button using the existing JSON file dialog conventions. Fyne's existing project picker is single-file; repeated adds are acceptable for the expected 3-6 projects. Avoid building a custom multi-select file browser for this feature.
- `Remove` button for the selected pending job.
- `Move Up` and `Move Down` buttons for pending jobs.
- `Start Queue` button.
- `Cancel Current` button while a job is running.
- `Stop After Current` button while a job is running.
- A current-stage label and progress bar.
- An overall label such as `Job 2 of 5: F606W_project.json`.

Adding a project should parse only its JSON immediately. This provides early validation, obtains `ActiveFilter`, and does not load FITS pixels. Reject duplicate project paths in the same queue with a clear information dialog.

### Filter determination

Use `project.ActiveFilter` when it is non-empty. Normalize it only for filename safety (trim whitespace and replace characters invalid in Windows filenames with `_`); preserve ordinary astronomical names such as `F606W`, `Ha`, or `OIII`.

If `ActiveFilter` is empty, inspect the project's input metadata during job loading and require one unambiguous non-empty filter across active inputs. If it cannot be determined, fail the job with a message asking the user to reload/save the project with a selected filter. Do not silently use `mosaic` as the filter name because that breaks the requested naming convention.

### Completion reporting

When all jobs finish, leave their final statuses visible in the queue window and show one summary dialog:

- all successful: `Completed N of N drizzle jobs.`
- any failures: `Completed S of N drizzle jobs. F failed.` followed by a compact list of failed project names and errors
- stopped/cancelled: include cancelled and unrun counts separately

Also send an operating-system notification with `ws.app.SendNotification(fyne.NewNotification(...))`. The title must explicitly say `Drizzle Queue Completed with Failures` when failures occurred so an unattended user notices it. The dialog remains the authoritative detailed report in case OS notifications are disabled.

## Data Model

Keep queue state in `internal/ui`; it is transient UI/application workflow state, not a durable domain model.

Suggested types in a new `internal/ui/mosaic_queue.go`:

```go
type drizzleQueueStatus int

const (
    queuePending drizzleQueueStatus = iota
    queueLoading
    queueAligning
    queueDrizzling
    queueSaving
    queueSucceeded
    queueFailed
    queueCancelled
)

type drizzleQueueJob struct {
    ProjectPath string
    Project     models.MosaicProject // JSON only; no pixels
    Filter      string
    RunAlign    bool
    Status      drizzleQueueStatus
    OutputPath  string
    Err         string
}

type drizzleQueueRunner struct {
    // transient jobs and callbacks; no package-global state
}
```

Do not put `mosaic.Input`, `mosaic.Result`, pixel slices, or a cancellation context in a completed `drizzleQueueJob`.

The runner should expose callbacks (or a small event type) for status/progress changes. The runner must not directly manipulate Fyne widgets. The queue window receives events and uses `fyne.Do` for UI changes. This separation makes sequential/failure/cancellation behavior unit-testable.

Inject the expensive operations into the runner for tests rather than requiring real FITS files:

```go
type drizzleQueueExecutor interface {
    Execute(ctx context.Context, job drizzleQueueJob, progress func(string, int, int)) (string, error)
}
```

A concrete executor performs project load, optional alignment, drizzle, and save. Unit tests use a fake executor.

## Reusable Headless Project Loading

Create `internal/ui/mosaic_project_loader.go` and extract the non-visual portion of `loadMosaicProject` into helpers. Suggested result:

```go
type loadedMosaicProject struct {
    Project        models.MosaicProject
    ProjectPath    string
    Inputs         []mosaic.Input
    Statuses       []mosaic.InputStatus
    ReferenceInput *mosaic.Input
    MigratedCount  int
}

func readMosaicProject(path string) (models.MosaicProject, string, error)

func loadMosaicProjectData(
    ctx context.Context,
    projectPath string,
    progress func(stage string, done, total int),
) (*loadedMosaicProject, error)
```

Requirements:

- Resolve and store an absolute project path.
- Preserve the current grouping by source path and legacy multi-chip migration behavior.
- Call `mosaic.LoadInputsForPipeline` with the provided context and mapped progress callback.
- Apply all persisted input state, including the new excluded/normalization fields.
- Treat any failed required input as an error for the queue executor.
- The interactive loader may still display per-input statuses, but should use the same core loader so behavior does not drift.
- Load the optional reference baseline with the current SCI-extension selection behavior and mark it `ReferenceOnly`.
- Resolve project-relative sky-mask directories with `resolveSkysubSettingsForProject` at execution time.
- Avoid Fyne calls, dialogs, preferences, and workspace mutation in the helper.

If preserving the interactive loader's current partial-load behavior makes a shared helper awkward, return both loaded data and structured load errors. The interactive caller can display partial results; the queue caller must reject any error. Do not duplicate the entire loader.

## Optional Alignment Execution

Create a non-UI helper that receives loaded inputs, optional reference, saved alignment settings, context, and progress callback.

Behavior must mirror the existing `Align / Register Frames` action:

1. Build the active input slice, prepending the reference baseline when present.
2. Read mode, search radius, and reference count from `project.AlignmentSettings`.
3. Clamp `NumRefs` to at least 1.
4. If a reference baseline exists, force `NumRefs = 1`, matching current behavior.
5. For modes where `mosaic.AlignmentStreamsPixels(mode)` is false, ensure needed pixels are resident. Extract the current pixel-restoration code in `mosaic_methods.go` into a slice-based helper rather than requiring a `mosaicWorkspace`.
6. Call `mosaic.AlignInputsByStarsWithMode` with `mosaic.AlignProgress{Ctx: ctx, ...}`.
7. Map results back to the original active input indices using the same reference offset logic as the current UI.
8. Never change locked inputs.
9. Automatically apply every successful result: `OffsetX`, `OffsetY`, `ManualTransform`, and `HasManualTransform`.
10. If any required unlocked input result is not applied, return a descriptive aggregate error and do not build.
11. Do not show the current result-review dialog.
12. Ignore `AlignmentSettings.DebugAlignment` in queue mode. Debug alignment is interactive and would block unattended processing.

Alignment changes are job-local. Do not rewrite the project JSON.

After alignment, clear resident SCI/ERR/weight slices before `mosaic.Build` so the existing on-demand frame loader keeps peak memory low.

## Headless Drizzle and Save

Extract the settings-to-options conversion currently embedded in `buildDrizzlePreview` so interactive and queued builds use the same mapping. A helper should cover every current field:

- `Scale`
- `FinalScale`
- `LockToReferenceFrame`
- `PixFrac`
- `CRMethod`
- `SepKernel`
- `FinalKernel`
- weighting mode, including the legacy `UseERRWeighting` fallback
- `SurfaceBrightnessNorm`
- `CRSeedSNR`
- `CRDerivScale`
- resolved `Skysub` options
- progress callback and context

For queue execution force `DebugOutputDir` to empty. The requirement is one final FITS per successful job; a debug directory saved in a project must not cause many extra files during unattended queue processing.

Execution order for one job:

1. Load and validate the project and all required inputs.
2. Determine/validate the filter.
3. Optionally align and apply results in memory.
4. Clear loaded pixel arrays that are no longer required.
5. Call `mosaic.Build` with cancellable options.
6. Generate the timestamp immediately before saving.
7. Build the output path in the project directory.
8. Check that the output path does not exist. If it exists, return an error; never call the current writer on that path because it uses `os.Create` and would truncate it.
9. Call `mosaic.SaveResultFITS`.
10. Only after a successful save, set the job's `OutputPath` and `Succeeded` status.
11. Drop all references to the `mosaic.Result`, loaded inputs, reference pixels, and project-local pixel data before returning.

Do not assign the result to `ws.state.result`. Do not call auto-level, stretch, histogram, preview, status, or Send-to-Examine code.

If a save fails after creating a partial output file, remove only that exact newly-created path after verifying it is the intended path for the current job. This cleanup is part of the save attempt and should be logged. Never remove a file that existed before the attempt.

## Queue Control and Cancellation

Use one background goroutine for the queue loop. There must never be more than one executor call active.

- `Cancel Current` cancels the current job context. Mark that job cancelled and continue with the next pending job.
- `Stop After Current` does not cancel the current job. It allows it to finish, then leaves all remaining jobs pending/unrun and ends the queue.
- If the user closes the queue window while running, hide the window but keep the queue running; completion notification still fires. Alternatively intercept close and ask whether to hide or cancel, but do not silently kill the worker.
- Disable add/remove/reorder/start controls while running. Re-enable them after completion or stop.
- Disable the main Mosaic `Create Mosaic`, interactive alignment, project load, and input mutation actions while the queue is active, or guard their handlers with an `isQueueRunning` check. This prevents concurrent high-memory mosaic operations and state races.
- A cancellation returned as `mosaic.ErrCancelled` must not be displayed as a failure dialog.

Use a fresh child context per job. Cancelling one job must not pre-cancel later jobs.

## Timestamp and Filename Helper

Put filename logic in a small pure helper, for example:

```go
func drizzleQueueOutputPath(projectPath, filter string, when time.Time) (string, error)
```

It should:

- reject an empty project path or filter
- sanitize invalid Windows filename characters: `< > : " / \\ | ? *` and control characters
- trim trailing dots/spaces from the filter component
- reject a filter that becomes empty after sanitization
- use the project directory
- use local time passed by the caller and `when.Format("20060102_150405")`
- return `<safeFilter>_<timestamp>_drizzle.fits`

Pass time into the helper; do not call `time.Now` inside it. The production executor calls `time.Now()`, while tests use fixed times.

## Expected File Changes

Keep the exact split flexible, but aim for these narrow responsibilities:

- `internal/models/models.go`
  - persist excluded/normalization project fields
- `internal/models/models_test.go`
  - round-trip and backward-compatible zero-value coverage
- `internal/ui/mosaic_project.go`
  - use extracted JSON/load helpers; preserve existing dialogs and workspace updates
- `internal/ui/mosaic_project_loader.go` (new)
  - headless project parse/load and input-state restoration
- `internal/ui/mosaic_project_loader_test.go` (new, only for pure/state logic; avoid large FITS integration fixtures)
- `internal/ui/mosaic_build.go`
  - use shared settings-to-options helper; preserve interactive preview behavior
- `internal/ui/mosaic_methods.go`
  - extract reusable input/ref and pixel-release/restoration helpers
- `internal/ui/mosaic_queue.go` (new)
  - job/status types, sequential runner, concrete executor
- `internal/ui/mosaic_queue_test.go` (new)
  - pure filename tests and fake-executor runner tests
- `internal/ui/mosaic_queue_window.go` (new)
  - Fyne widgets, callbacks, status refresh, summary, notification
- `internal/ui/mosaic_workspace.go`
  - queue-window reference/running state if needed
- `internal/ui/workspace_mosaic.go`
  - Mosaic menu item and guards/disable wiring

Do not move mosaic algorithms out of `internal/mosaic`, introduce a new dependency, or perform a broad workspace refactor.

## Unit Test Requirements

Follow `docs/unit-test-standards.md`. Prefer deterministic, fast tests around pure branching logic.

### Model/state tests

- New input-state fields and exposure normalization mode survive JSON round-trip.
- Older JSON with the new fields omitted decodes to backward-compatible zero values.
- Applying saved state restores exclude, normalize, scale, offsets, lock, and affine fields.

### Filename tests

- Fixed local time produces exactly `F606W_20260716_214530_drizzle.fits` beside the project.
- Filter names with invalid filename characters are sanitized deterministically.
- Empty/invalid-only filters are rejected.
- Project names and directories containing spaces work.

### Sequential runner tests using a fake executor

- Jobs execute in insertion order.
- Maximum concurrent executor calls is exactly one.
- A failed job is marked failed and the next job still executes.
- A successful job records its returned output path.
- Cancel-current marks only the current job cancelled and the next job runs with a fresh context.
- Stop-after-current leaves later jobs unrun.
- Final counts correctly separate succeeded, failed, cancelled, and pending/unrun.
- Queue completion callback fires once.

Avoid sleep-based timing tests. Coordinate fake jobs with channels so cancellation and ordering tests are deterministic.

### Executor/helper tests

- Alignment-disabled execution does not invoke the alignment function and preserves saved transforms.
- Alignment-enabled execution uses saved alignment mode/search radius/reference count.
- A reference baseline forces one alignment reference.
- Locked inputs are not overwritten.
- An unapplied required result prevents the drizzle call.
- A project input load error prevents drizzle.
- Existing output path prevents the FITS save call.
- Queue options force `DebugOutputDir` empty.

Use injected functions/interfaces for these tests. Do not require an hour-scale real drizzle or large FITS fixtures.

## Validation Sequence

Run the narrowest checks first:

1. `gofmt` only the modified Go files.
2. `go test ./internal/models`
3. `go test ./internal/ui`
4. `go test ./internal/mosaic` only if mosaic package code was changed.
5. `go test ./...` because this feature touches shared project loading and workspace wiring; run it after narrow tests pass.
6. `go build ./...`

Manual validation with two or three tiny projects:

1. Add projects, toggle alignment independently, reorder, and remove a pending job.
2. Run two successful jobs and confirm strict sequential execution.
3. Confirm filenames, project-directory placement, and readable FITS output.
4. Confirm no preview/histogram/result is created and memory drops between jobs.
5. Include a deliberately broken project between valid projects; verify later jobs run and the final dialog/OS notification reports the failure.
6. Cancel the current job and verify the next job starts.
7. Stop after current and verify remaining jobs do not start.
8. Close/hide the queue window during processing and verify work and notification continue.
9. Save and reload a project containing an excluded input and exposure normalization choices; verify the state is preserved.
10. Confirm ordinary interactive project load, alignment, Create Mosaic, preview, and manual Save Drizzle FITS still work.

## Implementation Order

Implement in this order to keep each step reviewable:

1. Extend saved-project fidelity fields and tests.
2. Extract project parsing/loading and input-state helpers without changing visible behavior.
3. Extract shared settings-to-options and alignment-application helpers; keep existing interactive behavior passing.
4. Add pure queue job/status/summary/filename types and tests.
5. Add the sequential runner with fake-executor tests.
6. Add the concrete headless executor and focused tests.
7. Add the queue window, menu entry, controls, action guards, summary dialog, and OS notification.
8. Run full validation and the manual scenarios above.

## Definition of Done

The feature is complete when a user can queue 3-6 saved filter projects, independently enable alignment for each, start the queue, and return later to timestamped final FITS files. Processing is sequential, results/previews are not retained, broken jobs do not block later jobs, and failures are unmistakably reported. Existing interactive Mosaic workflows and old project JSON files continue to work.

## Directory-Based Project Generation Extension

The queue also supports creating jobs from a directory of calibrated FITS files. The `Create Projects from Directory...` action first selects a saved project template, then scans a source directory with `mosaic.DiscoverFilterFiles`. The dialog offers product type selection (`.flc`, `.flt`, `.cal`, or `All`) and filter-group checkboxes with file counts.

For each selected filter, create `<filter>_project.json` beside the source files. Copy the template's drizzle, sky-subtraction, alignment, exposure-normalization, and optional reference settings; replace the input list with that filter's absolute file paths; set `ActiveFilter`; and clear template artifact masks and input-specific offsets/transforms/exclusions. Do not overwrite an existing generated project: report it as a per-filter error. Add each successfully written project directly to the existing queue, with alignment initially disabled so it can be enabled independently per generated job.
