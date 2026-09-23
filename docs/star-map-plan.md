# WFC3 star maps: proposed implementation plan

Status: initial implementation added 2026-09-12 using this plan as the starting point. The sections below retain the broader target design. See `star-map-validation.md` for implemented scope, observed results and remaining accuracy work. Work was performed by the current agent without subagents, as requested.

The earlier F673N metadata leak was corrected by the user and verified before implementation: EXPTIME=800 and SRCFILE=ifrp11w0q_flc.fits. The original audit table below records the earlier state, not a current defect.

## Recommendation and scope

Create star maps **after mosaic creation, on the exact mosaic pixel grid**, and validate candidates using the calibrated individual exposures. During future drizzle runs, preserve the geometry, source membership, coverage, and saturation evidence needed by that later step. Do not make the drizzle rejection mask double as the star detector.

This combines the mosaic's signal-to-noise and convenient RGB geometry with information that stacking can obscure: detector saturation flags, native stellar profiles, and independent exposure measurements. Existing mosaics should remain usable; missing exposure evidence must produce an explicitly reduced-evidence result rather than guessed provenance.

The first feature produces a conservative, reviewable catalog and **FITS masks**, not a reconstructed stellar flux image. A catalog identifies stars; a mask describes the pixels safe to select. Both must be accurate before applying color changes. Later star whitening will be an aesthetic RGB operation: narrowband measurements alone do not establish a star's natural broadband color, and a mask does not recover clipped flux. No particular online image's processing method has been established here.

Prioritize avoiding nebula selections over capturing every faint source. A useful outcome includes an `uncertain` classification; it does not force every compact object into star/non-star.

## Evidence from the supplied Trifid files

Read-only inspection used Python/Astropy/NumPy. The F673N mosaic was also rendered with an asinh stretch and inspected visually.

| Item | Observed |
|---|---|
| Calibrated observations | 18 FLT and corresponding 18 FLC files, six independent observations per filter |
| Filters / individual exposure times | F502N: 600 s; F656N: 500 s; F673N: 800 s |
| GoFits mosaics | Three float32 primary images, each 4121 × 4287 pixels, matching primary TAN WCS values |
| Mosaic auxiliary planes | No DQ, ERR, or WHT extensions in the three supplied GoFits mosaics |
| Working intermediates | 18 FLC combined files and alignment sidecars; inspected combined file has a WHT extension |
| Pipeline comparison product | Inspected F673N DRC has SCI, WHT, CTX, HDRTAB and a different grid; it is a comparison product, not an independent observation |
| FLC full-well flags | 764 F502N, 275 F656N, 1676 F673N pixel instances carrying bit 256 across both chips/all exposures; these are not star counts |
| ADC saturation | No bit-2048 pixels found in the inspected FLT/FLC DQ arrays |
| Coverage | F673N mosaic has 207,917 nonfinite pixels; zero intensity is not a coverage test |
| Geometry hazard | Native SCI uses TAN-SIP; paired DQ headers differ. DQ indexes the SCI detector pixels and must follow the SCI transform |
| Provenance hazard | F673N mosaic says FILTER=F673N but EXPTIME=600 and SRCFILE=ifrp11vvq_flc.fits, an F502N source. Inherited metadata cannot identify the actual input exposures or their units |

Only an F502N project JSON was present at the test-folder root. Other-filter source membership may be suggested from original FILTER headers, but must be established against valid alignment records and processing provenance. The example F673N combined alignment sidecar reports RMS about 0.44 pixels and median error about 0.21 pixels. Association tolerances must account for measured registration uncertainty, not demand an arbitrary perfect match.

The F673N display visibly contains sharp rims, narrow filaments, compact emission features, bright stars with spikes, and stars over structured backgrounds. Repeated detection in exposures alone cannot distinguish stars from these persistent sky structures. Matching WCS headers also does not prove subpixel registration of the observed stars.

Astropy reported existing FITS header-format warnings. New star-map outputs must independently pass strict FITS verification. These observations do not require rewriting the supplied files or expanding this work into a general FITS cleanup.

## Detection design

### 1. Establish inputs, geometry, and valid measurements

- Operate on linear science arrays before display stretch, RGB balance, clipping, or gamma.
- Choose a reference mosaic; verify dimensions/WCS and measure residual cross-filter offsets with isolated stars. If necessary, use explicit transforms for analysis. Do not silently shift science images.
- Prefer the FLC observations actually used to make these mosaics. FLT support remains possible, but FLT/FLC and DRZ/DRC variants of one observation must never count as independent confirmations.
- Read SCI/ERR/DQ by matching EXTVER, retaining saturation evidence before applying a science-validity mask. Interpret WFC3/UVIS flags by instrument: full-well bit 256 and ADC bit 2048 are different conditions [1].
- Reject unusable measurements, NaNs, detector defects and insufficient-coverage neighborhoods from fitting. Treat saturated pixels as evidence plus invalid fit samples, not automatic stars and not ordinary unsaturated flux. A missing flag does not guarantee linearity.
- Reuse the actual native-chip → combined → aligned-mosaic transform chain, including distortion, origin conventions and output offset/scale. Use SCI geometry for SCI-associated DQ/ERR pixels. A plain sky-coordinate conversion or sidecar affine alone may omit part of this chain.
- Keep coverage, quality flags and saturation occupancy separate. Project bit masks through pixel footprints/boolean accumulation; never interpolate integer DQ values as intensities. Do not reuse the science rejection rule to erase saturation evidence.
- Validate rate/flux normalization from processing records. Do not infer it from the inherited mosaic EXPTIME/BUNIT alone. A WHT extension is not automatically inverse variance.

### 2. Estimate the background and stellar profile

- Use a robust, spatially varying background/noise estimate with iterative compact-source masking. Choose scales relative to measured stellar width and check several scales near emission fronts.
- Fit local background offset and slope alongside each source. Compare against an extended-source/local-structure alternative with controlled model complexity. Background subtraction must not turn a ridge into a convincing point source.
- Build a small empirical point-spread-function (PSF) model from isolated, unsaturated, high-confidence stars, separately by filter and with spatial variation when supported by enough stars. This describes the shape a point source actually has in these images [2].
- Start candidate generation with a simple Gaussian/Moffat approximation. Use empirical PSFs for confirmation. If the field cannot supply a reliable PSF, report that limitation and support reviewed seed stars; a library PSF is a later option requiring filter, detector, sampling and drizzle compatibility checks.
- Estimate noise at the detection/fit scale. Drizzling correlates adjacent pixel noise, so pixel RMS alone gives misleading significance [3]. Calibrate scores empirically; do not advertise an uncalibrated score as a probability.

### 3. Generate candidates permissively, accept conservatively

Use a union of candidates from each filter's locally normalized PSF-filtered residual image and the saturation-seeded path. Do not rely on a raw sum dominated by one emission line. Existing peak detection may seed the list, but cannot be the final classifier. Standard stellar finders use profile, sharpness and roundness constraints; these are useful ingredients rather than proof of correctness on this field [4].

For each unsaturated candidate, measure profile-fit residuals, width relative to nearby stars, ellipticity, central concentration, azimuthal consistency, centroid stability, local gradient/curvature and support across scales. Compare the point-source-plus-background fit with an extended alternative. Penalize systematic residual arcs/ridges. Jointly fit close pairs when possible; classify unresolved blends as uncertain rather than fragmenting them into many stars.

Use multiple pieces of supporting evidence:

- Consistent position and stellar profile in independent exposures, with tolerances based on PSF size, S/N and measured registration error.
- Forced local measurements in other filters at the candidate position, allowing independent flux and background per filter. Do not demand equal colors or detections in all three narrow bands.
- A provisional requirement of two usable independent exposures when coverage allows, to be evaluated on labeled data. A nondetection only counts against a candidate if the observation could reasonably have detected it.

Persistent nebulosity and diffraction spikes also repeat across exposures and filters. Neither repeatability nor color is sufficient without profile evidence. External catalogs could corroborate some sources later, but sparse matches, blending and epoch effects make them unsuitable as the required detector or ground truth.

### 4. Handle saturated stars separately

- Find connected saturation regions on the native detector, including possible column bleeding, and seek a stellar center near each region.
- Fit the unsaturated wings with the appropriate PSF plus local background; exclude saturated core samples, suspect bleed pixels, defects and nearby sources. Require useful wing samples in multiple directions. Do not impose the unsaturated core's roundness/sharpness cuts on a bleeding star.
- Use consistent wing centers and native DQ support from multiple observations. Optional diffraction-spike evidence can support a bright star, but spikes must not become separate stars.
- If a core is clipped or missing in the mosaic, a well-supported native-exposure detection can still place a star on the mosaic grid. Record positional uncertainty and missing central information.
- Keep verified core/wing footprint and associated bleed/spike artifacts distinct. Never expand a mask by unrestricted flood fill into a bright nebular rim.
- Mark sources with inadequate wings, severe blending, disputed saturation or unreliable positions as uncertain. Do not invent their flux or silently accept them.

### 5. Build bounded masks from accepted objects

Use per-star, per-filter PSF extent and local stellar contrast to set a finite footprint with a smooth edge. Keep one shared set of source IDs/centers but allow different footprints by filter. A color-processing selection needs stricter protection near a rim than an object-detection catalog does.

The default mask includes only accepted stars. Preserve uncertain objects and rejected candidates with reasons for review. Store manual acceptance, rejection and footprint edits separately from the automatic decision, so parameter changes do not erase review history. Tie those edits to source identity, position and input geometry, invalidating unsafe reuse.

## FITS output contract

For this dataset, write under `D:\goSource\GoFitsV3\TestImages\Trifid\working`. In normal use, resolve the existing workspace `working` directory through the application's workspace rules.

Recommended primary deliverables: `F502N_starmap.fits`, `F656N_starmap.fits`, `F673N_starmap.fits`. Add a run/grid identifier when needed to avoid collisions. Each file uses its associated mosaic's exact dimensions, orientation and WCS; current Trifid files can share a reference grid.

| HDU | Proposed meaning |
|---|---|
| Primary float32 image | Soft selection mask, 0–1; 0 outside accepted footprints and outside coverage. Dimensionless; not stellar flux or probability |
| `LABELS` int32 image | Stable accepted source ID, 0 for no accepted star; resolve overlaps deterministically |
| `FLAGS` integer image | Documented map-specific bits: uncovered, uncertain source, verified saturation, associated bleed/spike, manual edit; not a copy of the instrument DQ convention |
| `STARS` binary table | Shared source ID, FITS one-based X/Y, sky coordinates/frame, positional uncertainty, status, decision reasons, evidence score, PSF width, footprint, per-filter measurements, usable/matched observation counts and review overrides |
| `INPUTS` binary table | Source observation identity, source variant/HDU, fingerprint, filter, exposure time, geometry/transform identity, processing and normalization provenance |

Record algorithm/schema versions, parameters, reference fingerprint, run ID and PSF/noise diagnostics. Use explicit BUNIT metadata for each plane; synthesize structural headers and whitelist valid output-grid WCS instead of cloning unrelated source metadata. Internal coordinates can remain zero-based, with tested conversion at the FITS boundary.

A quality or coverage failure must remain distinguishable from a valid region containing no stars. Do not publish a plausible-looking empty map as success when PSF estimation, source access or registration failed. A truly star-free valid field may legitimately produce an empty map with an explicit diagnostic.

Write to temporary paths, verify, then publish completed outputs. On cancellation or failure, preserve previous valid results. Group the per-filter outputs with a run manifest or equivalent completion marker so a partial run cannot masquerade as a complete set. Do not allow normal input discovery to treat these derived FITS masks as new science exposures.

## Accuracy experiment and acceptance gates

Before developing the full UI, build a reproducible offline evaluation on the supplied dataset. Save candidate overlays and zoomable cutouts with accepted/uncertain/rejected colors and reasons; a small whole-field preview cannot establish accuracy.

1. Label representative regions: quiet sky; strong nebular backgrounds; sharp rims and compact knots; saturated stars; spikes/bleed; close pairs; chip gaps and field edges. Include obvious difficult negatives, not just clean stars. Review bright/saturated stars across the whole field.
2. Define certain-star, certain-nonstar and ambiguous labels, plus conservative acceptable footprints. Separate development regions from held-out spatial regions before tuning. Neighboring crops or variants of the same observation are not independent validation samples.
3. Compare the existing detector baseline, mosaic-only profile validation, and mosaic-plus-native-exposure validation. Report precision/recall by class, brightness, background and coverage; inspect every false positive on the held-out set.
4. Provisional goals: at least 99% precision for automatically accepted stars; at least 95% recall for clearly identifiable bright stars; no accepted labeled nebular knots in the designated hard-negative regions; every labeled bright saturated star accepted or explicitly surfaced as uncertain. These are proposed gates, not measured results or guarantees. Report actual numerators/denominators and sample uncertainty; a small sample cannot establish field-wide 99% accuracy.
5. Measure footprint leakage into labeled nebula, not just correct center detection. Inspect star-on-rim examples at full resolution; with a simulated later whitening preview, check for altered filaments, halos and hard edges. Set numerical leakage and positional thresholds from the labeling protocol before held-out evaluation.
6. Run an independent synthetic suite: PSFs at subpixel positions and varying widths, stars on gradients/ridges, compact extended knots, saturation/bleed, noise correlations, cosmic rays, hot pixels, blends, missing bands, NaNs and variable coverage. Inject into held-out real background patches as an additional check, while acknowledging synthetic PSFs do not reproduce every real artifact.

The first milestone succeeds only when the evidence supports conservative selections. If it fails, revise the classifier/PSF/background model before connecting whitening. The benchmark must include uncertain and missed sources, so apparent precision cannot be inflated by silently hiding all difficult objects.

## Repository integration

Targeted source investigation found that `internal/processing/star_extracting.go` implements an alignment detector, `ExtractStars`, using a global background estimate and connected blobs. It rejects areas above 400 pixels and elongation above 2:1, explicitly excluding saturated/bleeding sources; its tiled catalog is capped. It should remain unchanged for alignment. It is a benchmark baseline, not the implementation of this feature.

The WFC3 loader currently includes saturation in its bad-DQ treatment and repairs those pixels through `cleanSCIWithMatchingDQ`. The new evidence reader must access unmodified SCI/ERR/DQ. Existing `matchingDQHDU` logic in `internal/mosaic/input_loader.go` provides the pairing pattern.

| Area | Proposed files / changes |
|---|---|
| Pure image analysis | New `internal/processing/star_map.go` and focused tests: background, PSF/candidate models, classification, footprints and diagnostics; split internally if necessary to keep responsibilities clear |
| Evidence orchestration | New `internal/mosaic/star_map.go` and tests: explicit inputs, source deduplication, raw-chip evidence, cross-exposure matching and output-grid fusion |
| Coordinate mapping | Reuse/extend `internal/mosaic/mask_projection.go`, `NewDetectorOutputMapper` and `MaskOutputGeometry`, which follow drizzle placement; verify against `plannedInput.mapOutputPixel` |
| FITS products | New `internal/mosaic/star_map_fits.go` and tests, with narrowly scoped `fitsio` support if the proposed table/schema types are not yet supported |
| UI | New `internal/ui/mosaic_star_map.go`, a small entry-point change in `workspace_mosaic.go`, and tests for job state/output integration |
| Documentation when implemented | `docs/feature-list.md`, `docs/user-guide.md`, `docs/unit-test-audit.md` |

For HST working inputs, `Input.Path` normally names a combined file without DQ while `Input.SourcePath` identifies the original FLT/FLC. Reopen that original and map each SCI chip with the active combined exposure's placement. Test that direct raw-chip projection agrees with the actual raw→combined→mosaic composition; do not assume attaching a manual affine without understanding its coordinate frame is sufficient. Use `Result.OutputHeader` for output geometry, with appropriate FITS header filtering.

Use the active `mosaicWorkspace.state.inputs` or explicitly loaded project to establish source membership. For the user's already-saved mosaics, provide a saved-mosaic entry path with explicitly selected originals/project records and validated sidecars. If those records cannot establish the original placement, offer a separately measured and quality-checked evidence registration, clearly recorded as such, or remain in mosaic-only mode. Never silently claim the original drizzle mapping was recovered. This standalone path is part of the intended usable feature, even if active-workspace integration is delivered first.

The UI job must capture immutable input/result identities and a generation token. If the workspace, alignment, selected filter or mosaic changes before completion, do not attach stale results. Handle partial filter coverage independently. Keep map outputs out of the existing scan/group/drizzle input lists by explicit product classification.

The existing `WriteFloat32ImageWithExtensions` calls `os.Create`, so writing directly to the destination would truncate an earlier map. Use a sibling temporary product and a platform-appropriate replacement strategy that preserves the old file on failure; test the Windows behavior rather than assuming all rename operations replace existing files atomically.

## Implementation sequence

1. **Benchmark and input audit.** Create a developer evaluation tool and versioned region-label specification; validate source membership, transform reconstruction and units on this dataset. Produce baseline cutouts and counts. Keep the multi-gigabyte FITS set out of ordinary unit tests.
2. **Geometry and evidence foundation.** Add typed input/coverage/saturation provenance and narrowly scoped mapping access where needed. Verify chip-to-mosaic positions using real isolated stars and deterministic geometry fixtures before implementing classification.
3. **Detection core.** Implement background/noise estimation, candidate generation, empirical profile validation, saturated-wing detection, cross-observation checks, stable catalog IDs and decision reasons in a UI-independent package. Add initial focused tests and run the benchmark after each substantive algorithm change.
4. **FITS serialization.** Add the mask/table schema, compliant writing, reload, staleness checks and safe publication. Independently verify with Astropy and reopen in GoFits. Confirm there are no shifts/flips relative to the science mosaic and no accidental science-input discovery.
5. **Review UI.** Add a `Create Star Maps…` action for a mosaic/filter group, conservative defaults, accepted/uncertain overlays, per-object reasons, before/after footprint preview and explicit overrides. Run work in a cancellable background goroutine, with staged progress and UI-thread updates. Controls should describe outcomes rather than expose dozens of algorithm constants.
6. **Independent validation and review.** Use the configured test engineer for coverage gaps and regressions; use the configured code reviewer for completed production/test changes. Keep these assignments sequential with respect to files being edited. Update `docs/feature-list.md` and `docs/unit-test-audit.md` when implementation lands, accurately documenting supported inputs and limitations.
7. **Later feature: star color neutralization.** Only after map quality is demonstrated, design RGB whitening/desaturation using these masks and separate controls for cores, wings and artifacts. Revalidate alignment with the RGB processing grid. This is a separate behavior change, not part of the initial map detector.

Use bounded tile/patch processing, sequential exposure loading and cached local measurements. A single 4121 × 4287 float32 plane is about 67.4 MiB; many copies plus 18 full native exposures would be wasteful. Benchmark peak memory and elapsed time on the user's machine before promising a latency target.

Unit tests should follow `docs/unit-test-standards.md`: deterministic decisions and mathematical outputs, not implementation-shaped assertions. Priorities include DQ bit combinations, paired SCI geometry, chip/reflection/rotation/origin transforms, duplicate observation rejection, missing evidence, saturated wings, nebular hard negatives, footprint bounds, invalid pixels, cancellation, stale inputs and FITS round trips. Start with the affected packages; use an opt-in Trifid integration benchmark for real-data acceptance.

## Sources

1. [STScI: WFC3 UVIS calibration and DQ definitions](https://hst-docs.stsci.edu/wfc3dhb/chapter-3-wfc3-data-calibration/3-2-uvis-data-calibration-steps).
2. [STScI: WFC3 PSF modeling resources](https://spacetelescope.github.io/hst_notebooks/notebooks/WFC3/point_spread_function.html).
3. [STScI: drizzle weight maps and correlated noise](https://hst-docs.stsci.edu/drizzpac/chapter-3-description-of-the-drizzle-algorithm/3-3-weight-maps-and-correlated-noise).
4. [Photutils: point-source detection methods](https://photutils.readthedocs.io/en/stable/user_guide/detection.html).

The combined architecture and thresholds above are engineering recommendations to test, not results claimed by these references.
