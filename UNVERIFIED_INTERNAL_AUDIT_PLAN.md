# Unverified Internal File Audit Plan

## Objective

Directly review every Go file currently marked `Verified=false` in `INTERNAL_CODE_REVIEW_EVIDENCE.md`, fix only confirmed defects, add focused deterministic regression tests, and record file-level evidence. The evidence ledger is the authoritative manifest. `internal/ui/assets/icon.png` is excluded as a non-code asset.

## Mandatory group workflow

For each group: (1) a `planner` reads every owned production file and relevant test, recording a finding or explicit no-finding result per file; (2) an `implementer` fixes only confirmed findings and runs focused tests; (3) a `code_reviewer` independently reviews the actual diff and evidence. A group closes only after PASS.

Do not mark a file verified from package-level test success alone. Record direct evidence as: direct review/no finding; direct review/fixed with regression; or direct review/no-test rationale. Keep ownership disjoint, route cross-group findings to the owner, add no dependencies, and avoid broad refactors. Follow `docs/unit-test-standards.md` and `docs/unit-test-audit.md`. Preserve disk-backed Compose bounds/transactions/cancellation and Fyne UI-thread confinement.

No more than five groups may have active implementation/review pairs.

## Audit groups

### 1. Gaia catalog provider and cache

Scope: all remaining `internal/catalog/gaia/*.go` and tests.

Review migrations, SQLite rollback, cache keys/deduplication, cache-only behavior, retries, cancellation, malformed HTTP responses, query construction, and resource closure. Use mock HTTP and temporary databases only.

Validation: `go test ./internal/catalog/gaia`.

### 2. FITS headers, resize, and streaming

Scope: remaining `internal/fitsio/{header_only,resize,stream}.go` and tests.

Review malformed headers/dimensions, overflow, truncated data, interpolation edges, row/tile clipping, sync/close errors, cancellation, and bounded buffers. Use synthetic FITS and injected I/O failures.

Validation: `go test ./internal/fitsio`.

### 3. Export, histogram, render, and stretch

Scope: remaining files/tests in `internal/export`, `internal/histogram`, `internal/render`, and `internal/stretch`.

Review destination safety, encoder failures, options/bit depth, dimensions, NaN/Inf, short buffers, numeric edge cases, clamping, invalid modes, and bounded artifact export.

Validation: `go test ./internal/export ./internal/histogram ./internal/render ./internal/stretch`.

### 4. Models and support packages

Scope: remaining files/tests in `internal/{models,instrument,utils,config,debuglog,debugtime,version}`.

Review JSON compatibility/defaults, queue state, metadata parsing, nil/error behavior, and meaningful concurrency. Explicitly document no-test decisions for trivial wrappers/constants.

Validation: `go test ./internal/models ./internal/instrument ./internal/utils ./internal/config ./internal/debuglog ./internal/debugtime ./internal/version`.

### 5. Processing alignment, transforms, and WCS

Scope: remaining alignment/matching/star/refinement/warp/WCS production and test files in `internal/processing`.

Review coordinate conventions, affine order/inversion, rotation centers, fallbacks, degenerate catalogs, outliers, unequal dimensions, masks, NaN/Inf, and cancellation. Use small synthetic catalogs/raster fixtures.

Validation: `go test ./internal/processing -run "Test.*(Align|Affine|Star|Tweak|Warp|WCS|Rotation)"`.

### 6. Processing render, helpers, edit, and magic

Scope: remaining `canonical_render.go`, `downsample.go`, `edit.go`, `magic_levels.go`, `processing.go`, and related tests.

Review reference-grid/resize fallback, interpolation, finite samples, percentiles, RGB levels/histograms, dimensions, fingerprints/status, and non-mutating helpers.

Validation: `go test ./internal/processing -run "Test.*(Canonical|ComposeRGB|Magic|MTF|Downsample|Resize|ReferenceGrid|RGBLevels|Histogram)"`.

### 7. Processing calibration, Gaia projection, and photometry

Scope: remaining color-calibration, Gaia aperture/calibration/projection, instrument-photometry files/tests.

Review provenance, invalid footprints, background math, nonpositive flux, aperture bounds, insufficient stars, passband selection, cancellation, and bounded sampling. No live Gaia tests.

Validation: `go test ./internal/processing -run "Test.*(Calibration|Gaia|Photometry|Aperture|Projection|Background)"`.

### 8. Processing masks and cleaning

Scope: remaining `clean.go`, `cross_channel_clean*.go`, `mask.go`, `rgb_clean.go`, and tests.

Review mismatched channels, halos, tile seams/borders, NaN/Inf, cancellation, transactional output, and legacy parity.

Validation: `go test ./internal/processing -run "Test.*(Clean|Mask|Cosmic|Speck|CrossChannel)"`.

### 9. Mosaic input planning and sidecars

Scope: remaining mosaic alignment-sidecar, catalog-consensus, filter-scan, input-loader, and offset-file files/tests.

Review multi-extension identity, malformed WCS/CRPIX, offsets, sidecar compatibility, lock/reference behavior, statuses, and file closure.

Validation: `go test ./internal/mosaic -run "Test.*(Input|Filter|Offset|Sidecar|Consensus|Lock|PlateScale|WCS|CRPIX)"`.

### 10. Mosaic artifacts and detector corrections

Scope: remaining pedestal, artifact-mask service, temporary-file, mask-projection, MIRI/NIRCam correction, and destriping files/tests.

Review detector gating, geometry, temporary ownership/cleanup, cancellation, short data, correction bounds, and unaffected-pixel preservation.

Validation: `go test ./internal/mosaic -run "Test.*(Artifact|Mask|Pedestal|MIRI|NIRCam|Wisp|Destripe|JWST|Temp)"`.

### 11. Mosaic normalization and sky subtraction

Scope: remaining `exposure_norm.go`, `skysub.go`, and sky/streaming tests.

Review disconnected overlap, robust invalid statistics, difference planes, reference selection, streaming parity, cancellation, and statuses.

Validation: `go test ./internal/mosaic -run "Test.*(ExposureNorm|Sky|Background|Difference|Streaming|Overlap)"`.

### 12. Compose UI controllers and state

Scope: remaining Compose/Blink/Gaia/Magic controllers/state/tests, including `compose_*`, `channel_tabs`, `blinker_window`, and `gaia_compose`.

Review stale generations, cancellation, atomic batch state, calibration invalidation, eligibility/reference rules, large-mode Blink gating, picker coordinates, dialog validation, and UI-thread commits.

Validation: `go test ./internal/ui -run "Test.*(Compose|Blink|Magic|Gaia|Alignment|Offset|ChannelState|Accordion|CompositeStatus)"`.

### 13. Edit/Examine tools and viewport

Scope: remaining crop/heal/curve/drag/level/measure/star-picker/viewer/viewport files and tests.

Review geometry/bounds, zoom/pan, stale loads, empty images, disk-backed edit eligibility, tool safety, invalid levels, and UI-thread confinement.

Validation: `go test ./internal/ui -run "Test.*(Viewer|Viewport|Examine|Crop|Heal|Curve|Level|Measure|StarPicker|Drag|Stretch)"`.

### 14. UI FITS/export helpers and primitives

Scope: remaining UI export options, FITS helpers/resize, icon, memory, and primitive widgets/tests.

Review validation/defaults, extension/format agreement, dimension safety, nil resources, overflow, teardown callbacks, and no-test rationale for appearance-only wrappers.

Validation: `go test ./internal/ui -run "Test.*(ExportOption|FITS|Resize|Memory|App|Number|Select|Toggle|Icon)"`.

### 15. Mosaic UI alignment, build, references, and stars

Scope: remaining alignment results/apply/sidecars/debug, mosaic build/reference-change/stars files/tests.

Review cancellation/generation, stale results, reference changes, transform/sidecar identity, progress/error propagation, cleanup, and UI-thread commits.

Validation: `go test ./internal/ui -run "Test.*(MosaicAlignment|AlignmentResult|ReferenceChange|Sidecar|MosaicBuild|MosaicStar)"`.

### 16. Mosaic UI projects, paths, queues, layouts, and zoom

Scope: remaining mosaic header/helpers/layouts/project loader/paths/queue generator/zoom files/tests.

Review transactional loading, path portability, duplicate extension identity, queue ownership, compatibility, partial failures, layout prerequisites, and zoom bounds.

Validation: `go test ./internal/ui -run "Test.*(MosaicProject|MosaicQueue|MosaicHeader|MosaicPath|MosaicAccordion|MosaicZoom|MosaicLayout)"`.

### 17. Mosaic UI settings, exposure review, levels, measurement, and sky subtraction

Scope: remaining drizzle settings, exposure review, mosaic levels/measure, and sky-subtraction settings files/tests.

Review settings validation/defaults, numeric bounds/dependencies, stale review results, calculations, coordinates, and model/widget state separation.

Validation: `go test ./internal/ui -run "Test.*(DrizzleSetting|ExposureReview|MosaicLevel|MosaicMeasure|SkySubSetting)"`.

## Recommended waves

- Wave 1: groups 1, 2, 5, 9, 12.
- Wave 2: groups 3, 6, 10, 13, 15.
- Wave 3: groups 4, 7, 11, 14, 16.
- Wave 4: groups 8 and 17.
- Final reconciliation alone.

Accept group 5 before group 7 if calibration findings depend on transform conventions.

## Final reconciliation

Confirm every currently unverified Go row has direct evidence, preserve the PNG as excluded, check ownership and reviewer approvals, and run:

```powershell
go test ./internal/catalog/gaia ./internal/fitsio ./internal/export ./internal/histogram ./internal/render ./internal/stretch ./internal/models ./internal/instrument ./internal/utils
go test ./internal/processing
go test ./internal/mosaic
go test ./internal/ui
go vet ./...
go test ./...
```

Record manual-only UI smoke checks separately; they do not substitute for unit tests.

