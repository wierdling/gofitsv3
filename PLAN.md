# Disk-Backed Compose Completion Plan

## Scope and non-negotiable architecture

- Finish the existing `Compose Options > Large files` mode. It remains a persisted global preference (`compose.largeFiles`), creates a unique session below `<current working directory>/working/tmp`, and may be toggled only while all three standard channels and every colored overlay slot are empty.
- In large-file mode, a channel's `internal/fitsio.Float32Artifact` is the authoritative full-resolution raster. `models.LoadedImage` retains path, selected-HDU headers, dimensions, stretch state, rotation/alignment metadata, and other small state, but `HDU.Data.Pixels` must be nil between jobs. `largePreviews` may retain only bounded display images (currently at most 1600x1600).
- Never silently fall back to the normal in-memory Compose path. Every large-mode job must use an artifact-aware implementation or return an actionable error without changing the prior artifact/state.
- A channel-local job may materialize at most one full-resolution float32 plane, only for the duration of the background job, when an existing algorithm cannot yet be expressed as a row/tile pass. It must not clone that plane or retain it after producing the bounded preview. Multi-channel jobs must use sequential channel passes, small catalogs/statistics, or bounded tiles with halos; they must not materialize two or three full channels concurrently.
- All artifact mutations are transactional: write a sibling temporary artifact, close/sync it, atomically replace the slot only after success, then regenerate the bounded preview. Cancellation or failure preserves the old artifact, metadata, and preview. Artifact paths never enter `.gfprj` files.
- Rendering is a staged disk pipeline: prepare one aligned/stretched channel or overlay at a time into temporary artifacts, then combine bounded rows/tiles into a disk-backed composite. Do not retain full-resolution RGB byte buffers or three float planes. Manual offsets and fitted affines remain metadata applied during preparation, not destructive edits.
- Expensive work runs in a cancellable background goroutine with a progress dialog. Only commit state and update Fyne widgets through `fyne.Do`.
- Blink remains disabled in large-file mode, including after project load. Do not implement disk-backed blink.
- Preserve normal Compose behavior and existing project compatibility. Do not add dependencies unless an existing encoder cannot consume a bounded disk-backed image source.
- Tests follow `docs/unit-test-standards.md`. Assert bounded behavior with deterministic reader/writer instrumentation (maximum open artifacts/materialized planes/tile sizes), not flaky process heap measurements.

## Existing entry points that must be migrated

- Channel and overlay load/preview/store: `loadChannel`, `loadLayer`, `refresh`, `loadLargeComposeImage`, and `composeLargePreview` in `internal/ui/workspace_compose.go` and `internal/ui/compose_large_store.go`.
- Channel controls: the `newChannelControls` callbacks for mode/show-clip/Apply, `Auto scaling`, `Auto MTF`, `Magic`, `Apply Offset`, and `Rotate 90°`; the all-channel `magicAll`; `copySettings`, `matchChannelStretch`, `normalizeScale`, `resetData`, `alignChannels`, and `crossChannelClean` in `internal/ui/workspace_compose.go`.
- Composite/overlays/calibration: `composeRenderWithCalibration`, `composeRGB`, `renderImages`, overlay preview/save/load callbacks, and the Color Calibration `calculate.OnTapped` job in `internal/ui/workspace_compose.go`; current in-memory implementations are `processing.ComposeRender`, `AlignedPlanesForCalibration`, `ComposeRGB*`, `ApplyStretchParallel`, and overlay helpers.
- Project and output: `saveProject`, `loadProject`, `saveChannelGray`, `saveLayerGray`, `exportRGB`, `compositeImageForEdit`, and `sendToEdit` in `internal/ui/workspace_compose.go`; encoders are in `internal/export/exporter.go`; the current Edit handoff is `globalExportToEdit func(image.Image)` in `internal/ui/workspace_edit.go` and `internal/ui/app.go`.

## Ordered implementation steps

1. [ ] Establish the artifact transaction and bounded-access layer.
   - Files: `internal/fitsio/stream.go`, `internal/fitsio/stream_test.go`, `internal/ui/compose_large_store.go`, and `internal/ui/compose_large_store_test.go`.
   - Add reusable row-range/tile reads, sequential row writes, read-only open support, atomic temporary-output commit, artifact copy/replace, and helpers to materialize exactly one channel when required. Avoid allocating a fresh byte buffer on every `ReadRow`/`WriteRow`; reuse caller- or artifact-owned bounded buffers.
   - Add a small artifact descriptor (path, width, height, generation) and store APIs that replace/remove a runtime slot atomically. Stop passing uncoordinated raw paths through UI closures. Keep all descriptor paths contained under the session root.
   - Add deterministic instrumentation usable by tests to record concurrently open source artifacts, materialized full planes, and largest row/tile buffer without affecting production behavior.
   - Tests: truncated/invalid artifacts; row/tile edge clipping; clockwise/non-square access primitives needed by rotation; successful atomic replacement; injected read/write/cancel failures preserve the old artifact; cleanup remains idempotent and contained.
   - Done when: later operations can read bounded rows/tiles, can temporarily load one plane, and can transactionally commit a replacement through one API; failure never leaves a slot pointing at a partial artifact; focused `go test ./internal/fitsio` and `go test ./internal/ui -run "TestComposeLargeStore|TestComposeLargePreview"` pass.

2. [ ] Restore every channel-local stretch/control operation and bounded previews.
   - Files: `internal/ui/compose_large_store.go`, `internal/ui/workspace_compose.go`, focused tests in `internal/ui/compose_large_store_test.go` and `internal/ui/workspace_compose_test.go`, plus the smallest pure helpers in `internal/processing/processing.go` or `magic_levels.go` if streaming statistics are required.
   - Introduce one large-mode channel-job coordinator used by standard channels and overlays. It snapshots the artifact generation and metadata, runs off the UI thread, ignores stale completion, commits metadata/artifact/preview together, and invalidates calibration exactly as normal Compose does.
   - Mode, show-clip, Apply, black/white/background/peak/scaled-peak, Asinh/MTF/GHS parameters, manual offsets, and `copySettings` are metadata-only; apply them without rewriting the source artifact and rebuild the small preview directly from sampled artifact values with the selected stretch.
   - Implement `Auto scaling`, `Auto MTF`, per-channel `Magic`, and `magicAll` as sequential artifact passes. Prefer streaming histograms/quantile summaries; if exact current logic requires materialization, load one channel only, call the existing algorithm without cloning, derive the preview, clear pixels, then advance to the next channel. Preserve cancellation and partial-commit policy explicitly: `magicAll` prepares all results first and commits all-or-none.
   - Update pixel/region pickers to read only the requested artifact row/window in large mode. Overlay preview and grayscale-save controls must use the same channel-job/artifact abstraction.
   - Tests compare small fixtures against normal-mode stretch/auto/magic results within defined numeric tolerances and assert no retained pixels, no more than one materialized plane, bounded preview dimensions, stale-generation rejection, all-or-none Magic cancellation, and picker parity.
   - Done when: no channel control is disabled merely because large mode is active; each edit regenerates an accurate bounded channel/overlay preview; `imgs[i].HDU.Data.Pixels` is nil after every success/failure; Blink alone remains unavailable.

3. [ ] Make project save/load and reset artifact-aware.
   - Files: `internal/ui/workspace_compose.go`, `internal/ui/compose_project_test.go` (or the existing focused project test file), and `internal/models/models.go` only if an explicit project-version/mode field is necessary.
   - Keep save output path-based and backward compatible; persist channel/overlay state, offsets, quarter-turn rotation, fitted affine, RGB levels, and calibration exactly as normal mode does, but never persist session artifact paths. Save must work in either mode.
   - Remove the current large-mode load rejection. Parse and normalize the project first, create overlay slots, then stage each source FITS into a new session artifact sequentially with the saved selected-HDU behavior. Apply saved state and generate previews per slot; swap the complete staged project into the live workspace only after all required loads succeed or present the same deliberate partial-load policy as normal mode without leaking artifacts.
   - Force saved Blink state off while large mode is active. `resetData` must restage the original selected HDU sequentially, preserve current user stretch/offset/rotation metadata as its current normal behavior specifies, and commit each channel transactionally.
   - Tests: current and legacy overlay projects, missing source failure, selected SCI fallback, saved rotations/affines, no temp paths in JSON, bounded sequential loading, cleanup of abandoned staged artifacts, and Blink disabled after load.
   - Done when: `saveProject`, `loadProject`, and `resetData` have behavioral parity in large mode without full pixel retention or orphaned session files.

4. [ ] Restore copy/match and star alignment using sequential summaries/catalogs.
   - Files: `internal/ui/workspace_compose.go`, `internal/ui/compose_large_store.go`, `internal/processing/tweakreg_align.go`, their focused test files, and existing Compose alignment coordinator tests.
   - `copySettings` is metadata-only and should already be enabled after step 2.
   - Refactor `matchComposeChannelStretch` around a compact channel summary containing finite percentiles and optional star-core anchors. Build the reference summary once and each target summary sequentially from its artifact; apply only `ChannelState` results. Preserve `SmartLevels` fallback behavior.
   - For `alignChannels`, extract/limit stars from one artifact/channel at a time (one temporary full plane only if a bounded detector cannot preserve results), retain only catalogs, and add the smallest processing API that fits existing `general` transforms from catalogs. Continue using `coordinateComposeAlignmentWithEligibility`, Channel 2 as reference, direct/fallback eligibility, current stats, and metadata-only fitted affines; do not warp source artifacts.
   - Tests compare summary-based stretch matching and catalog-based alignment with current small-array behavior, including star-core fallback, missing/insufficient stars, indirect alignment, cancellation, no artifact mutation, and maximum one materialized channel.
   - Done when: Match Channel Stretch and Align to Channel 2 are enabled and produce the same persisted settings/transforms without two full channels resident.

5. [ ] Add transactional artifact transforms for normalize-scale and quarter-turn rotation.
   - Files: `internal/fitsio/stream.go`, `internal/ui/compose_large_store.go`, `internal/ui/workspace_compose.go`, and focused FITS/UI tests; reuse processing interpolation math from `processing.ResizeChannel` rather than changing normal callers.
   - Implement bounded tile/scanline resampling for `normalizeScale`, processing Channels 1 and 3 sequentially against Channel 2 metadata. Preserve the 1% skip threshold, header-derived scale, dimensions, state invalidation, and progress/result count.
   - Implement `Rotate 90°` as a transactional artifact rewrite that supports rectangular images, updates dimensions and `Rotation90`, clears alignment/manual offsets exactly as today, then regenerates the preview. `Apply Offset` remains metadata-only and is consumed by the compositor.
   - Tests compare small raster output with `ResizeChannel` and `rotateComposeChannel90CW`, cover rectangular rotations and four-turn identity, injected failures/cancellation, dimension metadata, calibration invalidation, and bounded tile use.
   - Done when: normalize, rotate, reset, and manual-offset controls never use a full multi-channel snapshot and never corrupt the prior artifact on failure.

6. [ ] Build the sequential disk compositor and restore RGB/overlay preview.
   - Files: new focused `internal/processing/disk_compose.go` and tests (or a narrowly named equivalent), `internal/ui/compose_large_store.go`, and `internal/ui/workspace_compose.go`.
   - Define an artifact-based immutable render request mirroring `ComposeRenderRequest`: B/G/R descriptors and metadata, ordered overlay descriptors/settings, calibration snapshot, RGB levels, context, and destination artifacts. Keep UI/session ownership out of `processing`.
   - In separate sequential phases, map each source to Channel 2's output grid with its fitted affine/manual offset/rotation and current fallback resize semantics, apply the appropriate per-channel or linked stretch, and write prepared float artifacts. Use a bounded tile cache for affine sampling; never open/materialize multiple full source channels.
   - Blend calibrated-linear and artistic overlays in their current order/settings by streaming each overlay into the on-disk R/G/B outputs. Preserve validity masks, opacity, highlight protection, tint, calibration status/diagnostics, and render fingerprint without hashing full pixels in memory.
   - Generate the composite viewport from a downsampled disk composite and streaming histograms. Wire `composeRenderWithCalibration`, `composeRGB`, `refresh`, measurement coordinates, and RGB Levels to this result; remove “Composite disabled in disk-backed mode.”
   - Tests use small fixtures to compare normal `ComposeRender` and disk output for unequal dimensions, manual offsets, rotations, fitted affines, invalid fill, all stretch modes, overlay ordering/modes, cancellation, and RGB-level preview. Assert sequential source opens and bounded tiles.
   - Done when: large mode renders the same composite/overlay semantics to disk and shows only a bounded composite preview; no full RGB byte buffer or three full float planes exist.

7. [ ] Make all Color Calibration paths disk-backed.
   - Files: `internal/processing/color_calibration.go`, `background_estimation.go`, `gaia_calibration.go` and focused tests as needed; `internal/ui/workspace_compose.go`; reuse the disk alignment/preparation layer from step 6.
   - Replace the large-mode `calculate.OnTapped` construction of cloned `CalibrationInput.Pixels`, `AlignedPlanes`, and Gaia planes with streaming/background-summary inputs and sparse artifact samplers. Instrument photometry remains metadata-only.
   - Neutral-background automatic/ROI estimates must stream valid aligned samples one channel at a time. Gaia must extract the reference catalog once, query/cache as today, and perform aperture/annulus measurements through bounded windows for each channel/overlay without retaining aligned planes.
   - Preserve cancellation/generation checks, status transitions, unsupported reasons, provenance/fingerprints, overlay calibrated/artistic fallback, Before/After preview, and save-calibration gating.
   - Tests compare small fixtures with current calibration results/tolerances for off/instrument/Gaia, automatic and ROI backgrounds, invalid footprints, overlays, cancellation/supersession, cache-only behavior, and maximum bounded readers.
   - Done when: Calculate and calibrated rendering work in large mode without constructing any full `CalibrationInput`, `AlignedPlane`, or `GaiaPlane` pixel slice.

8. [ ] Reimplement Cross-Channel Clean as a bounded tiled job.
   - Files: new narrow helpers beside `internal/processing/mask.go` and `clean.go`, their tests, plus `internal/ui/workspace_compose.go`.
   - Process the shared top-left region in tiles with an overlap/halo large enough for star-mask morphology and both cosmic-ray passes. A tile may hold bounded pieces from all three channels because the operation is inherently cross-channel, but never full planes. Write three replacement artifacts transactionally and preserve pixels outside the shared region.
   - Specify seam ownership so halo pixels are computed but only each tile's interior is committed. Prepare all three outputs and swap them as one transaction; cancellation/failure keeps every original channel.
   - Tests compare tiled and current whole-array `BuildLayerStarMasks`/`RemoveCosmicRays` output on small and seam-focused fixtures, unequal sizes, short/truncated data errors, cancellation, all-or-none commit, and a fixed maximum tile footprint.
   - Done when: Cross-Channel Clean preserves current results within an explicit tolerance, has no tile seams, and never materializes a full channel.

9. [ ] Add bounded grayscale and composite export for 8-bit and 16-bit output.
   - Files: `internal/export/exporter.go`, a focused disk/stream exporter file and tests, `internal/ui/workspace_compose.go`, and the disk compositor from step 6.
   - Add encoder-facing disk-backed image/row sources so `saveChannelGray`, `saveLayerGray`, and `exportRGB` consume artifacts without full RGBA or float-channel allocations. PNG 16-bit must quantize rows from disk; PNG/JPEG/TIFF/WebP 8-bit must use a bounded adapter or a format-specific streaming path. Preserve options, extensions, RGB Levels, calibration save gate, overlay blend, and atomic destination behavior.
   - Do not call current `FromRGBABytes`, `FromFloat32Channels`, or `FromImage` with a materialized full-size image in large mode. Normal mode keeps those APIs unchanged.
   - Tests decode exported small fixtures and compare pixels/bit depth with normal exports, cover each format, invalid destination/cancellation, atomic failure cleanup, RGB Levels, calibrated and artistic overlays, and bounded row/tile use.
   - Done when: all Compose grayscale/composite exports work in large mode at 8 bit, PNG at 16 bit, and export memory is independent of image height.

10. [ ] Preserve the Send Composite to Edit operation without crossing an in-memory boundary.
   - Files: `internal/ui/compose_large_store.go`, `internal/ui/workspace_compose.go`, `internal/ui/workspace_edit.go`, `internal/ui/app.go`, focused additions to `internal/ui/compose_large_store_test.go`, and a new narrow `internal/ui/workspace_edit_handoff_test.go`. Reuse `export.FromFloat32ArtifactsWithLevels` from step 9; do not add another encoder or artifact format.
   - Introduce a package-local typed union such as `editImageHandoff`: exactly one of the existing `image.Image` payload or an `editDiskSource` payload is present. The disk payload owns three R/G/B float artifacts, full dimensions, the RGB-level snapshot, and an idempotent cleanup function/root. Replace `globalExportToEdit func(image.Image)` and the setter returned by `newEditWorkspace` with this typed boundary. The normal in-memory call path must still call the existing `setImage` behavior.
   - Add a generation-checked Compose-store snapshot operation. Given the current `composeCompositeDescriptor`, it holds the store lifetime/read lock so publish/removal/session close cannot delete a plane mid-copy, validates generation and all dimensions, and copies each plane with one reusable bounded buffer into a newly created `<cwd>/working/tmp/edit-*` staging directory. Close/sync all three files before accepting the snapshot. Cancellation, read/write error, stale generation, or partial copy removes only the staging directory and leaves both the current Compose composite and current Edit source unchanged. Do not transfer raw Compose paths, persist them, or make Edit cleanup depend on the Compose session.
   - Split Send-to-Edit by mode. Normal mode continues to use `compositeImageForEdit`. Large mode starts one cancellable background job with a progress dialog, renders/publishes the save-gated calibrated composite, snapshots its current descriptor and RGB Levels, and stages the Edit-owned copy. It must never construct a full RGBA buffer. Reject or cancel concurrent sends through one small coordinator/generation token; a stale UI completion cleans its staged Edit source. Only a successful `fyne.Do` commit replaces the Edit source and selects the Edit tab; cancellation/failure keeps the tab and prior Edit source and shows an actionable error except for deliberate cancellation.
   - In `editWorkspaceState`, keep `source *image.RGBA` for normal mode and add one mutually exclusive owned disk source. Installing either kind first validates/prepares the new source, then swaps it and cleans the old disk root. Installing a disk source builds a maximum-1600x1600 RGBA preview row-by-row from its owned R/G/B artifacts with the captured RGB Levels, records the full original dimensions, and computes only fixed-size histogram bins; it must not use `toRGBA`, `image.NewRGBA(fullBounds)`, or expose the preview as the full-resolution export source.
   - Disk-backed Edit is intentionally read-only in this step. Zoom/fit and Save remain available; Save calls `export.FromFloat32ArtifactsWithLevels` against the Edit-owned planes. Levels, Curves, Sharpen, Clean, Heal, Crop, Apply, and Reset are disabled both in the widgets/tabs and by method-level guards, with a visible message that the displayed image is a bounded disk-backed preview. Loading or receiving a normal image restores all tools. Do not silently materialize a disk source if a disabled callback is reached.
   - Add `globalEditCleanup` (or an equivalent returned cleanup hook) and invoke it from the application close handler as well as on Edit-source replacement. Close order must be safe in either direction because Edit owns its copied root. If the window closes while staging, cancel the send, discard its staging root, then clean the installed Edit root; repeated cleanup is harmless.
   - Tests: store snapshot success; stale descriptor; source close/publish cannot race the copy; injected read/write/cancel failure removes the partial Edit root and preserves the Compose descriptor. Handoff tests cover typed-union validation, normal `setImage` behavior and tool re-enable, disk preview cap/full-dimension metadata/RGB-level pixels, disk-tool guards, streaming Save delegation, replacement cleanup, application-close cleanup, Compose-session cleanup before/after Edit cleanup, cancellation without tab/source change, and stale completion cleanup. Use deterministic buffer/open instrumentation rather than heap measurements.
   - Done when: both Composite center action and File > Send Composite to Edit are enabled with three loaded channels; normal mode is unchanged; large mode switches tabs only after a generation-safe Edit-owned snapshot is installed, retains at most the bounded preview/histograms in memory, can stream-save the full result, clearly disables unsupported Edit operations, and leaves no `edit-*` directory after replacement, cancellation, or shutdown.

11. [ ] Remove temporary large-mode guards and run integration/regression validation.
   - Files: `internal/ui/workspace_compose.go` and only tests/docs directly required by the final wiring.
   - Centralize menu enablement by prerequisites rather than `largeMode`. Remove the guards that currently disable project load, copy/match, normalize, align, clean, reset, export, send-to-edit, auto/MTF/Magic, and composite preview. Retain only: mode-toggle blocked while any standard/overlay image is loaded, Blink forced off/disabled, and genuine missing-channel prerequisites.
   - Audit clear/remove/overlay-close/project-replace/app-close paths for descriptor removal, generation cancellation, Edit ownership, and `working/tmp` cleanup. Confirm normal mode does not create artifacts.
   - Run focused tests after each prior step, then `go test ./internal/fitsio`, `go test ./internal/processing`, `go test ./internal/export`, `go test ./internal/ui`, and finally `go test ./...` because the final handoff changes cross package/workspace boundaries.
   - Perform a manual smoke test with three standard channels plus multiple overlays: toggle gating; load; every channel stretch/auto/magic/rotate/offset; copy/match/normalize/align/clean/reset; project save/reload; calibration modes; composite preview; gray and RGB 8/16 exports; Send to Edit; cancel/failure paths; clear; and app exit. Observe instrumentation/logs to confirm only previews persist and full-plane/tile limits are respected.
   - Done when: every Compose operation except Blink is available in large mode, normal Compose regressions pass, temp artifacts have correct ownership/cleanup, and no large-mode path retains a full channel/composite in memory after a job.

## Overall completion criteria

- Large-file mode can be enabled/disabled only with no standard or overlay images loaded, persists in Compose Options, uses a unique CWD-relative `working/tmp` session, and disables Blink.
- Standard channels and colored overlays keep only metadata and bounded previews in memory between jobs. Channel-local work is limited to one temporary full plane; multi-channel work is sequential or tile-bounded.
- All channel controls, copy/match/normalize/align/clean/reset, RGB/overlay composition, project I/O, color calibration, grayscale/RGB 8-bit and PNG 16-bit export, and Send to Edit work in large mode with normal-mode semantics.
- Cancellation and failures are transactional and do not corrupt channel artifacts, previews, project state, calibration state, or output files.
- Session and transferred Edit artifacts are cleaned by their owner; no raw session path is persisted; normal mode remains unchanged; focused and full repository tests pass.

# Internal Code and Unit-Test Audit Plan

## Purpose and workflow

Review every production Go file beneath `internal/` with its relevant unit tests. Each lane follows: reviewer identifies concrete defects and meaningful test gaps; implementer fixes only confirmed findings and adds focused deterministic tests; a reviewer verifies the diff. Do not add tests for trivial wrappers or widget rendering details. Follow `docs/unit-test-standards.md` and use `docs/unit-test-audit.md` as the coverage baseline.

Keep lane ownership separate. Route cross-package findings to the owning lane rather than editing another lane. Preserve the disk-backed Compose invariants described above: transactional artifacts, bounded row/tile access, no accidental multi-plane memory retention, cancellable background work, and UI commits through `fyne.Do`.

## Audit lanes

1. [x] Core data, I/O, and services
   - Ownership: `internal/{fitsio,models,instrument,catalog/gaia,badpix,export,histogram,render,stretch,utils,config,debuglog,debugtime,version}` and their tests.
   - Review FITS parsing/writing, artifact lifetime, persistence compatibility, Gaia cache behavior, numeric/empty-input handling, and output error paths.
   - Completed: review found and the implementer fixed exact 32-bit DQ-mask loss, malformed FITS dimension safety, and FITS writer header/payload validation. Focused `fitsio` and `badpix` tests pass.
   - Completed: independent review approved the FITS/DQ fixes. The remaining audit found and fixed unsupported export formats truncating existing destinations; regression coverage preserves a sentinel output. All scoped core-package tests, `go vet ./...`, and `go test ./...` pass.
   - Validation: `go test ./internal/fitsio ./internal/models ./internal/instrument ./internal/catalog/gaia ./internal/badpix ./internal/export ./internal/histogram ./internal/render ./internal/stretch ./internal/utils ./internal/config ./internal/debuglog ./internal/debugtime ./internal/version`.

2. [x] Processing engine
   - Ownership: `internal/processing` and its tests.
   - Review WCS/alignment/catalog transforms, interpolation and dimensions, disk compositor boundedness and cancellation, calibration, cleaning/masks, NaN/short-slice behavior, and goroutine/resource safety.
   - Add focused synthetic tests only for concrete unprotected behavior, including the audit's `ImageDataForReferenceGrid` WCS-failure-to-resize fallback if still absent.
   - Completed: fixed disk-compositor edge clipping, reference-grid affine ordering, transactional preview publication, and the calibration reader's matching edge behavior. Added direct WCS-failure resize-fallback coverage. Independent review approved the changes; focused and full test/vet runs pass.
   - Validation: `go test ./internal/processing`.

3. [x] Mosaic engine
   - Ownership: `internal/mosaic` and its tests.
   - Review input/frame loading, drizzle/grid/weights, combine and normalization, sky subtraction, artifact services, sidecars, temporary-file cleanup, cancellation/progress, and malformed WCS/geometry.
   - Prioritize small regression tests for malformed WCS/CRPIX, multi-extension input, and failure bookkeeping/status where warranted.
   - Completed: fixed final-drizzle cancellation returning partial success and made combined-cache replacement recovery-safe, including preservation of legacy recovery backups. Added malformed numeric WCS status coverage. Independent review approved the changes; focused, full, and vet test runs pass.
   - Validation: `go test ./internal/mosaic`.

4. [x] Compose/Edit/Examine and shared UI
   - Ownership: Compose/Edit/Examine workspace and shared UI files, including `compose_*.go`, `workspace_compose.go`, `workspace_edit.go`, `workspace_examine.go`, `gaia_compose.go`, `viewport.go`, and their matching tests; excludes mosaic/artifact/alignment UI files in lane 5.
   - Review job generation/cancellation/stale completion, Fyne-thread confinement, project load/save/reset parity, large-store cleanup, calibration invalidation, bounded previews and Edit handoff.
   - Test pure state/coordinator seams, not widget implementation details.
   - Completed: fixed large-mode reset, alignment, and transactional artifact state; Edit handoff and stale-operation safety; Examine stale loads; and Reset/Magic synchronization and store-descriptor guards. Independent review approved the final guard changes. Focused UI tests, vet, and full test runs pass where the Go cache was available.
   - Validation: focused UI tests first, then `go test ./internal/ui` after lane 5 stabilizes.

5. [x] Mosaic, artifact, and alignment UI
   - Ownership: `mosaic_*.go`, `workspace_mosaic.go`, `artifact_mask_editor*.go`, alignment settings/results/debug files, and drizzle/exposure/sky-sub UI files with matching tests.
   - Review queues, project/sidecar round-trips, reference changes, artifact ownership, progress/cancellation/stale UI updates, error propagation, and pure settings/header/default logic.
   - Completed: fixed stale/cancellable mosaic alignment and builds, transactional project loading, queue ownership, artifact editor/export identity and generation guards, and disabled the no-op memory-heavy alignment debug option. Independent review approved the final preview/export lifecycle safeguards. Focused UI, vet, and full test runs pass with a workspace-local Go cache.
   - Validation: focused mosaic/artifact/alignment UI tests, then the coordinated full UI suite.

## Sequencing and final gate

- Complete lanes 1, 2, and 3 in order. Lanes 4 and 5 may then proceed concurrently because their file ownership is disjoint.
- After each implementation pass, run its narrow tests before reviewer verification.
- At the end, check ownership boundaries, rerun reviewer verification for each lane, then run `go test ./internal/fitsio ./internal/processing ./internal/mosaic ./internal/export ./internal/ui` followed by `go test ./...`.
- Record any manual-only UI/file-dialog smoke checks separately; they are not substitutes for unit tests.
