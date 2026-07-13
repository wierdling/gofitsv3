# Plan: JWST pretty-picture artifact processing

## Rules

Implement one stage per request. Keep each correction disabled by default, preserve old behavior when disabled, add focused synthetic tests, and run the narrowest relevant test. Do not refactor unrelated mosaic code or reimplement JWST ramp fitting.

This supplements sky-background-matching-plan.md. Do not redo the scalar/plane overlap solver, edge robustness, NIRCam amp-pedestal correction, or existing row destriper already in the working tree.

## Required processing order

load SCI + ERR + DQ
  -> exclude unusable pixels
  -> NIRCam wisp template, optional
  -> NIRCam post-cal destriping, optional
  -> MIRI artifact mask, optional
  -> sky estimates / overlap maps
  -> match or match+plane
  -> outlier rejection
  -> drizzle

Detector-coordinate corrections must happen before sky estimation and must run identically in planning, CR, and final-drizzle loads.

## Stage 1 — exclude unusable JWST pixels

### Problem

cleanSCIWithMatchingDQ in internal/mosaic/input_loader.go repairs masked pixels with bicubic interpolation. For JWST science mosaics, repaired DO_NOT_USE pixels can enter sky statistics, overlap maps, and drizzle as artificial flux.

### Files

- internal/instrument/instrument.go
- internal/mosaic/input_loader.go
- internal/mosaic/frame_loader.go
- internal/mosaic/drizzle.go
- internal/mosaic/input_loader_test.go
- internal/instrument/instrument_test.go

### Implementation

1. Add a small instrument-level DQ policy: retain current repair behavior where needed for HST; use exclusion for NIRCam and MIRI.
2. For excluded JWST pixels, set SCI to NaN instead of calling RepairMaskedPixels. Ensure drizzle weight is zero as a second safeguard.
3. Match current JWST SkyMatch sky-statistics policy: exclude DO_NOT_USE and NON_SCIENCE. Before coding, verify the numeric NON_SCIENCE bit from current JWST documentation/data models; do not guess it.
4. Make eager LoadInputsFromPath and lazy loadChipFromDisk produce identical SCI validity masks.
5. Do not exclude other informational JWST DQ bits without evidence.

### Tests and acceptance

- Synthetic NIRCam/MIRI SCI+DQs: DO_NOT_USE/NON_SCIENCE pixels are NaN and contribute neither sky nor final flux.
- Informational DQ bit remains usable.
- Existing HST repair behavior is unchanged.
- Eager/lazy paths match.
- Run go test ./internal/mosaic ./internal/instrument.

## Stage 2 — NIRCam wisp templates

### Goal

Subtract detector-fixed additive wisps before matching. Sky matching cannot remove a detector-fixed pattern.

### Files

- new internal/mosaic/nircam_wisp.go
- new internal/mosaic/nircam_wisp_test.go
- internal/mosaic/skysub.go, or a narrowly scoped new artifact-options struct
- internal/models/models.go
- internal/ui/skysub_settings_window.go
- internal/mosaic/frame_loader.go
- docs/user-guide.md

### Configuration

Add persisted, default-off settings:

- Enable NIRCam wisp correction.
- Local template directory; never download templates.
- Auto-scale, default true, or fixed non-negative scale.
- Deterministic template lookup using detector plus resolved filter/pupil, for example nircam_wisp_detector_band.fits.

No template is a logged no-op. An unreadable or dimension-mismatched selected template is a clear build error. Initially support only STScI-template-covered imaging detectors, notably B4, A3/A4/B3. Do not claim claws can use this template mechanism.

### Algorithm

1. Validate NIRCam, detector/filter, dimensions, and finite template data.
2. Construct fit pixels from finite SCI/template data, valid DQ, and a source mask. Extend the current mask enough to exclude bright-source wings.
3. Fit one additive template scale with robust clipped weighted least squares through zero: scale = sum(w * template * residual) / sum(w * template * template). Remove only a robust scalar/local baseline for fitting; do not fit and subtract a frame-owned 2-D background.
4. Iterate 2–3 MAD-clipped passes. If scale is negative or ill-conditioned, log and skip rather than add a pattern.
5. Subtract scale times template from finite SCI pixels, preserving NaNs.
6. Invoke from prepareFramePixels before pedestal/destriping and skysub planning. Log scale, valid count, rejected count, RMS, and maximum correction.

### Tests and acceptance

- Recover injected positive template scale with stars and smooth extended background.
- A masked bright source does not bias recovered scale.
- No-op: disabled, non-NIRCam, unsupported detector, reference-only, absent template.
- NaN preservation and dimension mismatch error.
- Run go test ./internal/mosaic.
- Validate on a B4 frame using debug per-input drizzle products: wisp residual falls without negative template-shaped bowls.

## Stage 3 — mask-aware NIRCam destriping

### Files

- internal/mosaic/row_destripe.go
- internal/mosaic/row_destripe_test.go
- new shared internal/mosaic/artifact_mask.go plus test if Stage 4 reuses it
- internal/models/models.go
- internal/ui/skysub_settings_window.go
- docs/user-guide.md

### Implementation

1. Extract source-mask construction from buildDestripeMask into a pure helper that combines finite/DQ validity, automatic positive-residual mask, and optional user mask.
2. Add optional mask FITS path. Nonzero means excluded; dimensions must exactly match input in version one.
3. Persist advanced values with current defaults: mask sigma 3, trend window 129, row direction. Do not add column correction yet.
4. Emit diagnostics per amplifier, not only frame-wide RMS.
5. Retain the current rule: subtract high-frequency row residual only, never the smooth row trend.

### Tests and acceptance

- Existing gradient/stripe tests remain.
- A bright broad source spanning a row does not bias correction when externally masked.
- Fully masked or sparse rows are skipped safely.
- Mask mismatch errors before SCI mutation.
- Run go test ./internal/mosaic.

## Stage 4 — MIRI artifact masks and shower-safe handling

### Goal

Give MIRI a safe instrument-specific path without pretending a single calibrated image can reproduce ramp-level shower detection.

### Files

- new internal/mosaic/miri_artifacts.go
- new internal/mosaic/miri_artifacts_test.go
- internal/mosaic/frame_loader.go
- internal/models/models.go
- internal/ui/skysub_settings_window.go
- docs/user-guide.md

### Implementation

1. Add default-off MIRI artifact-mask path/directory convention keyed by input basename. Start with user-provided binary FITS masks.
2. Combine artifact and DQ masks. Masked pixels become NaN and zero-weighted; exclude them from sky, overlaps, CR models, and drizzle.
3. Do not implement automatic single-frame shower subtraction. Optional later row-outlier detection may only mask rows, must be MIRI-only, default off, and needs separate real-data evidence.
4. Document that preferred remediation is rerunning the STScI pipeline with current MIRI jump/shower processing before importing cal files.

### Tests and acceptance

- Masked MIRI pixels cannot contribute to sky or output.
- Non-MIRI data is unaffected.
- Invalid/mismatched mask errors are explicit.
- If row guard is added, test synthetic shower rows and non-triggering smooth gradients/ordinary sources.
- Run go test ./internal/mosaic.

## Stage 5 — UI, persistence, diagnostics

### Files

- internal/models/models.go
- internal/models/models_test.go
- internal/ui/skysub_settings_window.go or new focused artifact dialog
- internal/ui/mosaic_project.go
- docs/user-guide.md

### Implementation

1. Prefer a compact JWST artifact corrections section/dialog over turning skysub controls into a long advanced form.
2. Persist every option. Zero values must exactly preserve old-project behavior.
3. Explain: match is relative offsets; match+plane is relative offset+gradient; wisp/destriping are detector corrections; raw-data flicker/shower correction belongs in STScI preprocessing.
4. Add JSON round-trip tests for every new setting.
5. Debug logs for each correction must include input, correction enabled/skipped reason, valid/masked count, RMS, maximum, and fitted scale where applicable.

## Real-data validation after each stage

Use identical inputs, WCS, drizzle parameters, and stretch for before/after runs. Save baseline/candidate mosaics, difference image, coverage image, and per-input debug drizzle products outside git.

Measure robust blank-region RMS, median absolute seam difference, correction RMS/max, and inspect real diffuse structure for negative bowls. Include one ordinary HST/non-JWST regression mosaic with all new controls disabled.

## Explicit non-goals

- No ramp fitting, clean_flicker_noise, jump detection, CRDS selection, or runtime template download.
- No frame-owned 2-D background subtraction for extended-emission fields.
- No automatic NIRCam claw model.
- No high-order background surface until real data shows overlap-difference planes are insufficient.

## Primary references

- [JWST background methods](https://jwst-pipeline.readthedocs.io/en/stable/jwst/user_documentation/background_subtraction_methods/main.html)
- [SkyMatch algorithm](https://jwst-pipeline.readthedocs.io/en/1.17.x/jwst/skymatch/description.html)
- [NIRCam 1/f guidance](https://jwst-docs.stsci.edu/known-issues/nircam-known-issues/nircam-1-f-noise-removal-methods)
- [NIRCam wisps](https://jwst-docs.stsci.edu/known-issues/nircam-known-issues/nircam-scattered-light-artifacts)
- [MIRI showers](https://jwst-docs.stsci.edu/known-issues/shower-and-snowball-artifacts)

