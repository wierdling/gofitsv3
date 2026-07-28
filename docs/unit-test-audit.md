# Unit Test Audit

## Purpose
This document tracks the current quality of unit tests in this repository using the standard in `docs/unit-test-standards.md`. It is the working source of truth for:

- which packages are already in good shape
- which tests are weak or too narrow
- which logic-bearing areas still need direct unit coverage
- which test additions should be prioritized next

## Rating Scale
- `well covered`: tests are behavior-focused, deterministic, and cover the important branches for the package's main logic
- `partially covered`: meaningful tests exist, but important logic or branches are still missing
- `weak tests`: tests exist, but they are too narrow, too integration-skewed, or leave most meaningful logic unprotected
- `no direct unit tests`: the package has no unit tests for its own logic

## Package Summary

| Package | Rating | Priority | Notes |
| --- | --- | --- | --- |
| `internal/processing` | well covered | medium | Broad behavior coverage now includes the main `processing.go` helper layer, calibration math/background estimation, canonical render paths, and a deterministic save/load/render regression fixture in addition to alignment, masking, compose, cleaning, and WCS |
| `internal/mosaic` | well covered | medium | Strong scenario coverage for drizzle/input planning, but still worth targeted edge-case additions |
| `internal/histogram` | well covered | low | Small logic surface and direct branch coverage |
| `internal/render` | well covered | low | Core rendering helpers are directly tested |
| `internal/stretch` | well covered | low | Transform behavior and invalid-mode behavior are covered |
| `internal/utils` | partially covered | low | Simple helpers covered, but only the currently used slice is exercised |
| `internal/ui` | partially covered | medium | Helper math/state/defaults plus core Compose state-application and preview-data branches are covered; larger workflow wiring remains mostly indirect |
| `internal/export` | partially covered | low | Main write paths covered, but options and failure branches are still thin |
| `internal/badpix` | partially covered | medium | DQ masking, repair fallbacks, mirror behavior, and empty-mask handling are now covered; a few lower-level math helpers still remain indirect |
| `badpix` | partially covered | medium | Cleaning now covers DQ presence, missing-DQ behavior, bad-bit filtering, and load failures, with only rarer malformed-file edge cases still thin |
| `internal/fitsio` | partially covered | medium | Synthetic parser/writer tests now cover core branches; a few low-level edge cases still remain |
| `internal/models` | partially covered | medium | Persistence coverage now includes Compose and Mosaic project/state round-trips, with only backward-compat edge cases still thin |
| `internal/catalog/gaia` | well covered | medium | Provider/cache tests cover migrations, deduplication, endpoint semantics, retries, cancellation, cache-only misses, and an online-to-cache-only end-to-end fixture |
| `internal/config` | no direct unit tests | low | Currently small surface; add tests if validation or branching grows |
| `internal/debuglog` | no direct unit tests | low | Low-risk logging wrapper |
| `internal/debugtime` | no direct unit tests | low | Low-risk timing helper |
| `internal/instrument` | no direct unit tests | low | No direct coverage for package behavior |
| `internal/version` | no direct unit tests | low | Likely trivial, low-risk surface |
| `webpwriter` | no direct unit tests | medium | Output-related code with no direct protection |
| `cmd` | no direct unit tests | low | CLI wiring can stay untested unless startup logic grows materially |

## Package Findings

### `internal/processing`
- Rating: `well covered`
- Covered behaviors:
  - alignment and WCS behavior across channel alignment, warp, tweakreg, and rotation-center helpers
  - compose and cleaning behavior for RGB composition and mask-based cleaning flows
  - direct helper coverage for autoscaling, auto levels, grayscale/mask rendering, flip helpers, resize interpolation, drizzle-grid matching, pixel-scale parsing, finite sampling, percentile interpolation, and robust sigma fallback behavior
- Gaps:
  - reference-grid helpers like `ImageDataForReferenceGrid` are still covered more indirectly than directly
  - some general utility functions in `processing.go` are now covered, but a second pass could still add direct tests for remaining reference-grid fallback branches if bugs appear there
- Recommended tests:
  - add direct tests for `ImageDataForReferenceGrid` fallback behavior when WCS alignment fails and resize fallback is used
  - add a small regression case for `ApplyRGBLevels` or `HistogramRGB` only if future bugs show up there, since those helpers are already exercised indirectly

### `internal/mosaic`
- Rating: `well covered`
- Covered behaviors:
  - drizzle build scenarios, overlap/weighting behavior, NaN handling, sky subtraction paths, chip placement, and shared canvas assembly
  - input loading and offset/filter behaviors through dedicated tests
- Gaps:
  - some rejection and malformed-header scenarios are still likely covered only indirectly
  - same-file/multi-extension edge cases and status-reporting branches deserve a second pass
- Recommended tests:
  - add direct negative tests for malformed WCS/CRPIX headers in planning and combine paths
  - add focused tests for status text and per-input bookkeeping where failures should remain diagnosable

### `internal/fitsio`
- Rating: `partially covered`
- Covered behaviors:
  - synthetic `readHeader` and `readImage` coverage across `BITPIX` modes `8`, `16`, `32`, `-32`, and `-64`
  - synthetic `LoadFile` round-trip coverage for multi-HDU files plus `SelectSCI`, `GetHDU`, `SelectDQ`, and `GetHDUByExtVer`
  - invalid-input coverage for unsupported `BITPIX`, short reads, and empty files
  - direct helper coverage for `skipPadding`, `ImageData.Normalize`, `ImageData.ToRGBA`, `CloneHeader`, `HeaderFloat`, `formatHeaderCard`, and `formatEndCard`
  - write/read round-trip coverage for `WriteFloat32Image`
  - smoke-level ability to load real FITS samples when local test images are present
- Missing or weak areas:
  - no direct test yet for `readImage` when `NAXIS < 2`
  - no direct short-write or writer-failure coverage for lower-level `writeHeader`, `writeFloat32Data`, or `writeCard`
  - limited explicit coverage around malformed numeric header values beyond the current `HeaderFloat` failure cases
- Recommended tests:
  - add a small regression test for `readImage` returning header-only HDUs when `NAXIS < 2`
  - add direct failure-path tests for `writeHeader`, `writeFloat32Data`, and `writeCard` using a failing writer
  - add a few malformed-header parsing cases if FITS header variability starts causing bugs

### `internal/models`
- Rating: `partially covered`
- Covered behaviors:
  - `MosaicProject` JSON round-trip preserving nested drizzle, alignment, and skysub settings
  - `MosaicInputState` round-trip preserving affine transform and lock state
  - omitted optional-field behavior for `ReferencePath`, `ReferenceSCIExt`, `SCIExt`, transform fields, and `UseERRWeighting`
  - negative and zero-value round-trips for persisted channel/input state fields
- Missing or weak areas:
  - no direct malformed-JSON or backward-compat decode tests yet for partially populated historical project files
  - optional-field omission is covered for the current main structs, but not exhaustively across every future persistence variant
- Recommended tests:
  - add a backward-compat decode test using minimal or legacy-shaped project JSON if real upgrade issues appear
  - add any new persistence-field tests when project schemas change rather than relying only on the current round-trips

### `internal/badpix`
- Rating: `partially covered`
- Covered behaviors:
  - DQ-based mask generation
  - repaired pixels change masked values without damaging nearby unmasked values
  - center and edge interpolation smoke paths
  - empty-mask and mask-length-mismatch no-op behavior
  - `bicubicAt` fallback behavior when the mask is malformed or no valid fill samples exist
  - `weightedMedianFill` success/failure behavior and `mirror` edge reflection logic
- Missing or weak areas:
  - lower-level helpers like `solve16` and `localBicubicFit` are still covered mostly through higher-level behavior rather than direct branch tests
  - there is still limited explicit coverage for more pathological multi-pixel masked neighborhoods
- Recommended tests:
  - add a focused regression case for multi-pixel masked clusters if interpolation artifacts appear in real data
  - add direct `localBicubicFit` or `solve16` tests only if numerical edge-case bugs show up, since the current public behavior is already protected

### `badpix`
- Rating: `partially covered`
- Covered behaviors:
  - one file-based happy-path clean flow using a synthetic FITS file
  - missing-DQ behavior for both allowed and rejected configurations
  - bad-bit filtering through `Config.BadBits`
  - load-error propagation for missing files
- Missing or weak areas:
  - malformed-FITS and dimension-mismatch behavior are still only lightly covered through lower layers
  - there is not yet a direct regression case for a loadable file whose DQ/SCI geometry mismatches at the package boundary
- Recommended tests:
  - add a direct top-level regression test for DQ/SCI mismatch if that becomes a recurring failure mode
  - add malformed-file cases only if real-world broken FITS inputs become part of supported behavior

### `internal/ui`
- Rating: `partially covered`
- Covered behaviors:
  - viewer coordinate math
  - compose-state clearing helper
  - stretch helper behavior
  - `channelStateFromImage` and `applyChannelState` behavior for restoring Compose channel state into images, controls, and viewport levels
  - `buildComposePreviewData` behavior for missing channels and optional compose override results
- Missing or weak areas:
  - `workspace_compose.go` and related settings/window logic contain substantial state and branch behavior with little direct unit protection
  - many UI helpers are currently covered only through indirect manual usage
- Recommended tests:
  - add direct tests for Compose project-load/save state plumbing that exercises the actual saved-project channel loop, not just `applyChannelState`
  - add a focused `applyComposePreviewData` test if preview/view-layer regressions start appearing, since that branch still depends on richer viewport state
  - add focused tests for small pure helpers in `workspace_mosaic.go`, `export_options.go`, `fits_helpers.go`, and settings dialogs where logic is separate from rendering

### `internal/export`
- Rating: `partially covered`
- Covered behaviors:
  - main image write flows for PNG, JPEG, and TIFF
  - unsupported-format and malformed-buffer rejection
- Missing or weak areas:
  - option-handling branches are thin
  - failure coverage does not deeply exercise encoder/write failures
- Recommended tests:
  - add tests for default-vs-explicit quality handling where behavior differs
  - add deeper negative-path tests for invalid destinations and encoder propagation

### `internal/histogram`
- Rating: `well covered`
- Covered behaviors:
  - empty input, all-invalid input, min/max updates, equal-value ranges, moments, and histogram bucket placement
- Gaps:
  - no urgent missing coverage found for the current logic surface

### `internal/render`
- Rating: `well covered`
- Covered behaviors:
  - RGB composition output shape
  - short-channel zero-fill and clamp behavior
  - byte conversion helpers
- Gaps:
  - no urgent missing coverage found for the current logic surface

### `internal/stretch`
- Rating: `well covered`
- Covered behaviors:
  - linear copy semantics
  - monotonic bounded transforms
  - histogram equalization behavior
  - invalid mode handling
- Gaps:
  - no urgent missing coverage found for the current logic surface

### `internal/utils`
- Rating: `partially covered`
- Covered behaviors:
  - clamping, float parsing, key sorting, and header formatting helpers
- Missing or weak areas:
  - tests cover the current helpers but not every formatting edge case
- Recommended tests:
  - add edge-case tests for empty headers, duplicate-like key ordering assumptions, and parse failures with odd whitespace or signs if those inputs are expected

### No-test packages
- `internal/config`, `internal/debuglog`, `internal/debugtime`, `internal/instrument`, `internal/version`, `webpwriter`, and `cmd` currently have no direct unit tests.
- Current priority judgment:
  - low priority for trivial constants, wrappers, and startup wiring
  - medium priority for `webpwriter` because output code can regress quietly and is user-visible
- Recommended tests:
  - add direct coverage to `webpwriter` first if it contains format-specific branching or output options
  - defer the other packages unless their logic surface grows beyond simple wrappers/constants

## Missing-Test Backlog

### High Priority
- Finish the remaining low-level `internal/fitsio` edge cases: `NAXIS < 2` handling and explicit writer-failure paths.

### Medium Priority
- Add a second-pass `internal/processing` reference-grid helper test for explicit WCS-failure and resize-fallback behavior.
- Add a second-pass `internal/mosaic` edge-case suite focused on malformed headers, bookkeeping/status branches, and same-file extension corner cases.
- Add direct `webpwriter` tests if the package contains format-specific logic or output options that can fail independently of higher-level export tests.
- Add a backward-compat `internal/models` decode test if we need to protect historical project JSON shapes explicitly.
- Add a smaller follow-up `badpix` regression test only if DQ/SCI mismatch or malformed-load behavior needs explicit top-level protection.
- Add a smaller follow-up `internal/ui` regression test only if project-loop wiring or `applyComposePreviewData` starts causing preview-state bugs.

### Low Priority
- Expand `internal/export` option-path and failure-path tests.
- Add targeted `internal/utils` formatting edge-case tests if those helpers begin carrying more UI/reporting significance.
- Defer tests for `internal/config`, `internal/debuglog`, `internal/debugtime`, `internal/instrument`, `internal/version`, and `cmd` unless they gain branching logic.

## Recommended Next Test Work
1. `internal/fitsio`: mop up the remaining low-level edge cases around header-only HDUs and write failures.
2. `internal/processing`: add a narrower second-pass test for explicit reference-grid fallback branches if we want to close that remaining helper gap.
3. `internal/mosaic`: add a targeted edge-case pass for malformed headers and bookkeeping/status branches.
4. `internal/ui`: add only targeted regression cases if project-loop wiring or preview application state starts causing bugs.
5. `internal/models`: add a backward-compat decode test only if legacy project-file compatibility becomes a real concern.

### Color calibration Phase 1 gate

The Phase 1 calibration suite includes direct tests for instrument metadata
boundaries, robust background estimation, transform fingerprints and staleness,
canonical render status handling, overlay-mode separation, and a JSON
save/load/render fixture. The fixture verifies Off behavior, a valid persisted
instrument/background result, stale non-application, and repeatable preview
bytes. UI-only dialogs, visual Before/After inspection, and actual file export
through platform widgets remain manual acceptance checks.

### Color calibration Phase 2 gate

Gaia provider and SQLite cache behavior now have deterministic mocked HTTP and
cache-only coverage. Manual acceptance is still required for first online use,
cancellation at each UI stage, insufficient-star messaging, preview/export
equality, and project save/reload on a representative workstation. The Gaia
documentation records network/privacy scope, cache growth/deletion, migration
behavior, passband limits, quality thresholds, and provenance fields.

## Validation Notes
- Existing package tests reviewed during this audit passed for:
  - `./internal/processing`
  - `./internal/models`
  - `./internal/fitsio`
  - `./internal/badpix`
  - `./internal/mosaic`
  - `./internal/ui`
- `internal/processing` was expanded after the initial audit with direct helper tests for `processing.go`, and the findings above reflect that newer state.
- `internal/fitsio` was expanded after the initial audit with a synthetic fixture-based suite, and the audit findings above reflect that newer state.
- `internal/ui` was expanded again with second-pass tests for channel-state application and preview-data helper behavior, and the findings above reflect that newer state.
- `internal/models` was expanded after the initial audit with broader project/state persistence round-trip tests, and the findings above reflect that newer state.
- `internal/badpix` and `badpix` were expanded after the initial audit with fallback, missing-DQ, bad-bit filtering, and load-error coverage, and the findings above reflect that newer state.
- The audit is based on targeted inspection of production files and current `*_test.go` files rather than numeric coverage alone.
