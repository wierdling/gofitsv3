# Wite-Star: gentler stellar stretch in red-contributing filters

Status: plan created 2026-09-15; first preview prototype implemented and checked 2026-09-16; blended-source and large-saturated-star work checked on real data 2026-09-19 (see the last checkpoint below). Planning was performed directly, without the planning agent. Implementation used specialized implementer and test-engineer agents, followed by direct parent review; the independent reviewer could not complete because of a usage limit.

### Implementation checkpoint, 2026-09-16

Delivered **Compose > Gentler Star Stretch Preview...** and `cmd/starstretch`, backed by the same original-source diagnostic pipeline. Saved map identities and accept/exclude decisions are validated. Background fitting rejects annular outliers, a bounded circular Gaussian search measures stellar width, and profile mismatch/edge/blend cases are surfaced. The renderer replaces a smooth stellar increment and rejects unsafe corrections as a whole. Normal/gentler/mask stamps and CLI radial profiles are available; inputs are unchanged.

The initial post-clipping reference curve has been replaced following the user's all-skipped preview report. The renderer now evaluates the source stretch before its display clamp, estimates the positive stellar excess `E` above the locally fitted background, and uses `E/(1+4*strength*E)`. It blends that change through the prepared treatment mask before the final [0,1] clamp. The background remains on the ordinary curve; strength zero preserves the existing output. MTF is continued above its white point with its endpoint tangent, avoiding a rational-function pole and preserving the MTF midtone-0.5 linear case. This remains a visual prototype, not recovered stellar photometry.

Current scope: original-source cutouts using current scalar source settings; no Compose Apply control or RGB export modification. A bounded radial/sector pass now prepares the visible core/halo support, keeping full treatment through the clipped plateau and tapering outside it. Neighbor and cutout boundaries limit expansion; asymmetric spikes and a complete spatial PSF model are not delivered. Histogram equalization and GHS are rejected. Saturation-tagged stars now have a separate wing-validation path; insufficient wings, poor fits and unsafe expanded footprints are skipped per star (as of 2026-09-19 an invalid sample no longer skips the star; see below). Display clipping alone is supported without changing the user's normal settings.

Saturated-core treatment: exclude the suspect central region when validating the surrounding stellar profile, retain saturation provenance in diagnostics, and use the measured footprint to compress the recorded core and wings together. The excluded core must remain inside the full-strength mask. This is an appearance adjustment, not reconstruction of missing detector flux. Saturated-source cutouts include room for the wider wing/background measurements.

Saturated-step review: independent test coverage passed. Independent code review identified delayed cancellation in wing scans; context checks were added to those scans and their caller, with passing cancellation regressions and direct parent review. A subsequent independent final review approved this bounded step with no material findings. The parent reran the focused saturated-wing, fitting, mask and rendering tests successfully; `git diff --check` also passed. The reviewer relied on that test run because its session could not access the configured Go cache. Cancellation during an active scan is not directly regression-tested (the existing test cancels before scanning); bounded scans and explicit context checks were reviewed, with stronger mid-scan coverage left as a future improvement if scan bounds grow. This approval does not establish full footprint visual acceptance or Compose integration readiness.

Validation performed:

- Focused processing, preview, UI parser, Compose RGB and existing normal/disk comparison tests passed. Added exact stellar-increment tests, noisy-sample continuity, width fitting, overflow/invalid-input/cancellation checks, no-dark-hole and display-clipping regressions, plus supported-mode zero-strength parity with the existing Compose scalar renderer.
- `go build -o tmp/gofits-wite-star-preview.exe ./cmd` passed.
- Trifid F673N source 483 at Asinh peak 50, scaled peak 20, strength 0.65: usable preview with a reduced central radial mean and unchanged outer samples. Inspected normal/gentler/mask images and radial values.
- M16 F673N brightest 12 selected sources at Asinh peak 1000, scaled peak 100, strength 0.65: four usable previews, three saturation-tagged skips and five invalid-sample skips. These are developer examples, not independent accuracy measurements.
- Inspection of an earlier display-clipped M16 preview exposed a hollow-core failure. Initially this was avoided by rejecting display-clipped stars, which blocked the user's entire selection. The current regression requires actual pre-clipping treatment with radial monotonicity and complete mask support through a clipped plateau; it must not merely allow a darker center than its surrounding ring.
- User-reported Trifid F673N regression: MTF, background -0.0219, peak 0.1730, scaled peak 10 and midtone 0.5. The corrected run at strength 0.75 provides 16 usable previews of the same brightest 24 sources; three saturation-tagged, two poor-fit and three neighbor/boundary cases remain explicit skips. Source 673 has a gently decreasing central radial profile rather than a hollow core. The Normal view is unchanged; skipped sources no longer show fake corrected panels. See the generated developer report `tmp/starstretch-mtf-fixed/index.html`.
- Subsequent saturated-wing implementation at the same settings provides **18/24** usable previews. Saturation-tagged sources **771 and 702** pass wing validation and are treated; **906** still fails the profile residual check (about 0.378 against a 0.35 limit). The other five exclusions are unchanged. Inspected normal/gentler/mask PNGs for the recovered stars. Report: `tmp/starstretch-saturated-final/index.html`. Gaussian/Moffat wing candidates, significant quadrant coverage, declining radial support, robust background fitting and neighbor boundaries govern acceptance; no core flux is reconstructed.

Next implementation gate: validate full stretched-halo footprints and the reference curve on reviewed cutouts, then integrate actual Compose application with equivalent normal/disk geometry and weighted-source behavior. The rest of this document remains the target plan, not a claim that all milestones have shipped.

Footprint validation started: see `docs/wite-star-footprint-validation.md` for synthetic measurements and Trifid cutouts at three strengths. A curved-nebula fixture exposed excess background suppression despite an acceptable initial profile fit; a conservative ordinary-star directional-residual guard now skips that ambiguous footprint. Faint/subpixel, broad smooth halo and blend regressions pass. The selected real-data previews are unchanged. Full halo/spike and complex-background acceptance remains open; this is not approval for bulk Compose integration.

Extended halo/spike work (2026-09-18): the preview now measures a wider background, circular halo support and a rotated orthogonal four-arm diffraction cross, with a shared finite mask and smooth core/background transition. The search remains bounded; incomplete components are explicit. Source 771 provides a real-data example extending beyond the old 32-pixel circle, while source 702 remains limited by nearby structure. See `docs/wite-star-halo-spike-validation.md`; arbitrary asymmetric PSFs and complex nebular separation remain open.

Extended-step final review: the resumed independent code review passed with no material findings. Independent tests, affected-package tests and the desktop build were already passing; no production changes were needed to close this review. The bounded halo/spike preview step is complete, with its documented morphology and background limitations retained.

Complex-nebulosity validation (2026-09-18): known-background fixtures exposed a curved-rim leak below the old peak-relative directional threshold. The ordinary-star guard now checks directional excess against the fitted stellar tail and a noise allowance. Curved-rim, rotated/subpixel, offset-knot, filament and planar-control tests pass, including a nonidentity MTF case. The fixed eight Trifid examples retain six usable previews at all three strengths. See `docs/wite-star-nebulosity-validation.md` for numerical evidence and the explicit identical-profile knot limitation; this does not establish general background separation or Compose readiness.

### Implementation checkpoint, 2026-09-19: per-star skips, blended sources, large saturated stars

Motivation: the user's over-saturated red-layer stars. A mask-code review found that several whole-set or whole-star vetoes removed exactly those stars, and that stars that run together were never treated. Changes, validated on the M16 WFC3 F502N map (23,016 sources, 987 accepted) and the Trifid F502N/F656N/F673N maps:

- **Per-star skips.** `PrepareStarStretchFits` marks a star `Usable=false` with a reason ("visible halo reaches a neighbor or preview boundary", "structured local residual; stellar footprint is ambiguous") instead of failing the whole fit set. `ApplyGentlerStarStretch` leaves a nonfinite sample at its ordinary rendering and treats the rest of the star instead of rejecting it; nothing is reconstructed in its place. The preview returns the reason for a skipped star rather than an empty "preview".
- **Blended groups** (`internal/processing/star_treatment_blend.go`). Accepted sources whose cores overlap (separation below 2x the larger FWHM) are clustered (`starBlendGroups`) and fit jointly (`fitBlendedStarGroup`): one robust plane from an annulus around the group; non-negative amplitudes solved simultaneously for fixed-center profiles; per-member widths by coordinate descent (a map FWHM measured on an unresolved blend is a placeholder, so a shared width cannot describe both stars); saturated members exclude their measured clipped core and may use a Moffat basis. Each accepted member gets its own `StarTreatmentFit` with `GroupID` and `Companions`; masks and corrections still max-combine, so the union footprint is treated once. Map sources left **uncertain** by the detector (not explicitly rejected) within 2x FWHM of an accepted star join as *companion-only* members: modelled, never treated alone, never enlarging the fitted region, field-PSF width prior; those inside a saturated member's clipped core are plateau detections and are dropped. Footprint growth, the structured-residual guard, the outer-radius measurement and the halo/spike probe subtract the companion model (skipping companion clipped cores), companions never bound `safeLimit`, and the residual guard allows `max(Residual, 0.3) x companion` of model error. The preview renders the group's other usable members with the selected star.
- **Saturated wing residual.** The 0.35 gate now discards the 15% most deviant samples only when they are azimuthally balanced (paired spikes, rings); a one-sided excess keeps the full residual and still fails. Saturated group fits use only positive excess, as the single-star path already did, so zero-coverage holes in drizzled data are not read as one-sided misfit.
- **Display-defined quiet level.** Footprint growth and halo closure treat linear excess whose stretched contrast is below 2% of the display range (`starDisplayQuietLevel`) as quiet, in addition to the noise floor, so a very extended HST halo can close inside the search limit; the correction at that level is negligible, so the feather ends without a visible step.
- **Halo closure at the safe boundary.** A bright star's halo can stay above the quiet level all the way to the halo search limit (Trifid F673N **906** under MTF peak 0.173: spikes validate to 122 px). If the halo is still declining and its light at the boundary is below the level where the gentler correction itself is visible (`starNegligibleContrast`, 6% stretched excess, i.e. under about 1.5% correction at full strength), the full-strength region ends a feather-width inside the limit instead of skipping the star. 906 now renders under both MTF and Asinh with a 66 px halo and no ring; previously "visible halo reaches a neighbor or preview boundary".
- **Fixes found on the way.** Halo inner radius is clamped to at least the core outer radius (previously could produce "invalid mask geometry"); raw FWHM NaN/zero no longer disables the blend test; per-pixel neighbor exclusion scans only nearby sources, taking the M16 fit from 36 s to about 4 s.

Real-data outcome: M16 saturated stars **10126** and **17294** (amplitude about 90; previously "saturated source wings are not a bounded stellar profile") now render with 87 px and 76 px validated halos and, for 17294, four validated spike arms; inspected normal/gentler/mask PNGs show the bloated core compressed with rings and spikes retained. The user-accepted Trifid F673N pair **817/819** (previously "neighbor overlaps stellar core") fits jointly and is treated as one footprint. M16 **12462** remains an explicit skip: a genuinely bright, flat-topped companion 10 px away cannot be absorbed by a profile basis (residual 0.357). The previously listed Trifid examples are unchanged. Package tests for `processing`, `starstretchpreview`, `ui` and `mosaic` pass; details and constants are in `docs/wite-star-footprint-validation.md`.

Real-data regression: `internal/starstretchpreview/realdata_test.go` (run with `GOFITS_REALDATA=1 go test ./internal/starstretchpreview -run RealData`) fits the full M16 F502N map and asserts the decisions above: 10126/17294 usable with validated halos (17294 with four spikes), 12462 an explicit blend-residual skip, at most a handful of blend groups, fit time under 30 s; Trifid 817/819 jointly fit with shared plane and distinct widths, 819 rendered and 817 an explained structured-residual skip whose core is still covered by the group footprint (819's real PSF wings are not carried by the Gaussian companion model; an empirical-PSF companion is the follow-up); 906 usable under both the MTF and Asinh settings with a 66 px halo and four spikes. Every rendered preview is checked for no brightening, no change outside the mask, an actual reduction, and no ring that the normal profile does not also show. It is skipped without the environment variable so ordinary `go test ./...` is unaffected.

Open items from this checkpoint: the Gaussian companion model omits PSF wings (see 817 above); the 2% quiet contrast, 15% trim and 0.35 balance limit were tuned on these two datasets only; spikes that fail validation are dimmed inside the feather and untouched outside it (visible along the arm at the mask edge, e.g. 10126); Compose integration remains the next gate.

### Implementation checkpoint, 2026-09-19 (later): Compose integration stage A, pipeline hooks

Pipeline audit. The disk compositor (`internal/processing/disk_compose.go`) evaluates the stretch per output pixel at a sampled *source* position (`mapper.mapCoordinate` then `stretchDiskValue`), in `prepareDiskChannel` (artistic), `streamWeightedDiskSource` and `calibrateWeightedDiskSource` (weighted). The in-memory compositor (`stretchForReferenceGrid` in `processing.go`) resamples *linear* data to the reference grid and then runs `ApplyStretchParallel`, discarding source positions; on a shared drizzle grid it does not resample at all. Star fits live in the source grid, so the treatment is defined once as "linear sample at a source-grid position -> treated stretched value" and applied at that boundary in both paths.

Delivered:

- `processing.StarTreatmentModel` (`star_treatment_model.go`): prepared usable fits + the scalar stretch settings they were prepared for + strength + a 64 px grid index of footprints. `TreatedStretch(v, x, y)` is the single per-sample rule (max-combined corrections, invalid samples untouched). `ApplyGentlerStarStretch` now renders through it, so preview and Compose cannot diverge. The index took a whole-frame M16 render (7631x4678, 609 treated stars) from more than 20 minutes to 0.38 s (ordinary stretch 0.13 s).
- `models.LoadedImage.StarTreatment` (interface `models.StretchTreatment`: `MatchesStretch`, `SourceSize`, `TreatedStretch`), render-time only, never persisted. Disk path: `diskStretchFunc` uses it at all three sites and errors, never falls back, when the treatment was prepared for other stretch settings or another grid. In-memory path: a treated source is stretched on its own grid (`TreatedStretchForSource`, parallel) and the stretched result is resampled when grids differ; a stale treatment there falls back to the ordinary stretch with a debug log because that composer has no error path. Stage B validates before rendering.
- `PrepareStarStretchFits` runs stars in parallel (`prepareOneStarStretchFit`), 29 s -> 5 s for the 609 usable M16 stars.
- Tests (`star_treatment_model_test.go`): indexed model equals a brute-force evaluation over every fit and equals `ApplyGentlerStarStretch`; strength zero and pixels outside every footprint equal the ordinary stretch; HistEq rejected; stale settings and a changed grid rejected; disk and in-memory compositors agree within 1e-5 on a treated red channel in artistic and weighted modes with untreated channels unchanged; stale treatment rejected by the disk compositor. The real-data regression still passes.

Known behavior: for sources on a different grid from the reference, the treated path stretches then resamples (the untreated path resamples then stretches); the difference is bilinear interpolation of already-stretched samples, and it matches the disk compositor, which also treats at the sampled source position. (Artistic disk mode was found to stretch every base channel with channel 2's settings; fixed on 2026-09-20, see below.)

### Implementation checkpoint, 2026-09-19 (later): Compose integration stage B, controls and lifecycle

Delivered **Compose > Gentler Star Stretch...** (`internal/ui/compose_star_treatment.go`): per loaded source, an enable toggle and strength, with the source's composite role shown so the user picks the red-contributing ones (nothing is inferred from filter names). Apply prepares in a cancellable background job with a progress dialog; the result attaches a `StarTreatmentModel` to the source and refreshes the preview.

Lifecycle. `composeStarTreatments.sync` runs before every render generation: a source whose prepared model still matches its stretch settings, strength and grid renders treated; otherwise it renders untreated while a background job re-prepares (about 5 s on M16), and the preview refreshes when the model lands. Stretch-independent fits (`starstretchpreview.FitTreatment`) are cached per source and reused while the file, map (size and modification time) and grid are unchanged; only `Model` (prepare + index) is rebuilt on settings changes. A source rotated in Compose, or whose loaded grid differs from the original file, is refused with an explanation. In disk-backed Compose the original file is reopened by the job.

Rendering. Channel tiles and gray export use `processing.StretchForDisplay`; a treated channel with manual offsets or a fitted alignment is stretched on its source grid first and its stretched result is warped (identity stretch on the warped clone), matching the disk compositor. Persistence: `ChannelState.StarStretch` (`{enabled, strength}`), written only when enabled so untouched projects are unchanged; the model is never persisted and is rebuilt on load. Per-star exclusion is by rejecting the star in Star Map Review.

Validation: controller tests (attach/detach/stale on settings, strength and effective settings; rotated and pathless refusal; entry pruning), project round trip, and the env-gated real-data build test (`FitTreatment` from file 6 s, `Model` 5 s, whole-frame render 0.7 s, 335k pixels reduced, nothing brightened). Package tests for `processing`, `starstretchpreview`, `ui`, `models` pass and the desktop binary builds. Visual acceptance in the running app on the user's scenes is the remaining gate.

### Fix, 2026-09-20: disk artistic composition stretches each channel with its own settings

Artistic-mode disk Compose (`ComposeDisk` in `internal/processing/disk_compose.go`) stretched channels 1 and 3 with Channel 2's settings, unlike normal Compose, and its HistEq handling zeroed the prepared base rasters before computing a shared CDF (`prepareDiskChannel`'s internal pass ran with an empty histogram), so a HistEq base channel rendered as a constant. Both are fixed: every base channel is mapped and stretched with its own settings, a HistEq channel gets its own CDF from its own mapped raster (the path overlays already used), and the dead internal HistEq pass in `prepareDiskChannel` is removed (it now refuses to fuse HistEq). `TestComposeDiskArtisticUsesEachChannelsOwnStretch` renders three channels with different modes including HistEq through both engines and requires identical planes; the old test that asserted the constant output is gone. With this, the star treatment's temporary "effective settings" hook was unnecessary and has been removed: a treatment is always prepared for its own source's settings.

### Implementation checkpoint, 2026-09-20: White Stars (post-stretch neutralization)

Delivered **Compose > White Stars...** as the optional neutralization of section 0, built on the footprint machinery rather than a new mask: `processing.StarNeutralizer` (`star_neutralize.go`) takes one reference source's prepared `StarTreatmentModel`, maps composite pixels back to that source's grid with the compositors' own `diskCoordinateMapper` (its affine is sampled and inverted for star centers and extents), and applies a two-pass operation on the final R,G,B planes: per star a sigma-clipped median background per channel from an annulus 1.1-1.5x its extent excluding every footprint (subsampled to 4096 samples), then per pixel with weight `w` from the model (overlaps owned by the largest weight) the excess `S = P - B` moves toward a neutral `N`: `P += strength*w*(N - S)` in the selected channels. `N` is the excess luminance in "luminance" mode; in "white" mode it blends from luminance toward the brightest channel's excess by `t = max(S)/peakExcess`, the star's own profile, so cores go white and faint halos are neutralized without being brightened into a plate (the pure brightest-channel level did that on Trifid 906). Backgrounds are never neutralized. Both compositors apply it after mixing and colored layers and before LRGB (`ApplyRGBA`/`Apply` in the UI closure, `DiskComposeRequest.StarWhitening` -> `ApplyDisk`). Setting: `ComposeProject.StarWhitening` (reference BlinkID, strength, level, R/G/B); the reference source gets a model even when its own gentler stretch is off (`composeStarTreatments.whiteningRef`).

Validation: synthetic tests (core excess neutral with background color retained, feather keeps background color, nothing changes outside the footprint, channel selection, luminance mode, strength zero, reference offset followed, disk equals memory); Trifid F502N/F656N/F673N composite inspected: magenta 906 and small pink stars become white; with a 0.75 gentler stretch on two channels 906 shows a dim disc with a rim where the F502N map's halo ends short of the F673N one. Package tests pass; desktop binary builds. Next: an option to use the reference map's geometry for every channel's gentler stretch (re-estimating only each channel's background planes), which removes the rim and is the literal "one mask applied to all channels".

### Implementation checkpoint, 2026-09-20 (later): shared star geometry, whitening falloff

`processing.DeriveStarTreatmentFits` (`star_treatment_derive.go`) transfers a reference source's prepared geometry (position, core, inner/outer radii, validated halo, spikes) to another source on the same grid and re-measures only what lives in that source's units: the annulus background plane and noise, the extended background for halo/spikes (core-only if unavailable), and a diagnostic central excess. Reference-unit companions are dropped; the result is not re-prepared. `starstretchpreview.DeriveTreatmentModel` wraps it (reloading the file in disk mode, refusing other grids). Compose: **Gentler Star Stretch... > Star geometry** (`ComposeProject.StarStretchGeometryBlinkID`); the controller keeps the reference prepared, records `derivedFrom` per entry so a change of reference or of the reference's model invalidates every borrowed model, and reports "waiting for the reference source's star geometry" until the reference is ready.

White Stars "White" mode now scales the applied amount by `sqrt(max(S)/peakExcess)`, the star's own profile, toward the brightest-channel level, instead of blending the level; the core turns white and a faint halo keeps most of its color. "Preserve luminance" is full neutralization at constant luminance.

Trifid check (F673N map shared by all three channels, gentler strength 0.5, whitening 0.9): the colored rim around 906 is gone, its halo compresses uniformly into a soft tinted glow with a white core, and the mid-brightness magenta cluster stars become clean white points; the nebula is unchanged. Faint stars not in the accepted map are untouched by design. Tests: derive keeps geometry and re-measures background; controller waits for the reference and rejects own-map models under shared geometry; package tests and the real-data regression pass; desktop binary builds.

## Primary plan: reduce the red stellar stretch

The latest user preference is to make stars less prominent and less red by stretching their contribution less in the filters assigned to red. This supersedes post-stretch whitening as the first implementation. The desired result is a better-balanced image with restrained stars, not necessarily perfectly white stars. The footprint work and saturation analysis below remain useful, but automatic whitening is not part of the initial release.

### 1. User-facing behavior

- Add **Gentler star stretch** in Compose, with a master strength and optional per-source strength overrides.
- Initially select source filters that contribute to the output red channel, using the actual channel assignments/mixing weights. Do not infer this from wavelength or filter name.
- At zero strength, reproduce today's output exactly. Increasing strength reduces the stretch applied to the estimated stellar contribution while retaining the ordinary stretch for the background/nebula.
- Apply only to accepted/manually accepted mapped stars. Preserve exclusions, and offer per-star treatment exclusions where fits are unreliable.
- Show before/after cutouts and footprint overlays. Include bright stars, faint stars, stars on nebular edges, and stars with broad red halos in preview.
- Do not automatically run the previous whitening operation afterward. The user should be able to judge the gentler stretch by itself.

### 2. Preserve and measure the linear stellar signal

Keep the original linear filter data available. Transform reviewed star positions into each source's working grid and verify registration before processing. Process each selected filter independently: its stellar width, flux and background can differ from the other filters.

For each mapped star, fit a robust local background `B` and a nonnegative stellar component `S` from a bounded linear-data cutout. Exclude neighbors and defects; use wings for a clipped core, with explicit uncertainty where its signal cannot be recovered. A local background plane is the initial model; a poor fit on complex nebulosity must be flagged rather than trusted. Resolve blends jointly when practical or leave them unchanged pending review (joint group fitting delivered 2026-09-19). Never turn a nebular knot into a star by following a brighter neighboring peak.

This is a local estimate, not a full-image star-removal feature. Preserve residual noise and unmodeled structure in the image. Missing detector saturation information must not block correction of ordinary unclipped stars. Actual clipped information cannot be restored by a gentler stretch.

### 3. Change the stellar stretch without darkening the nebula

Use a shared, testable stretch evaluator for the normal curve `T` and a proposed gentler stellar curve `U`, with identical input units, black/white normalization and output range. Do not obtain `U` merely by guessing which direction to move a parameter; parameter meanings differ among stretch modes.

For each sample, the normal curve's modeled stellar increment is:

```text
normalStar = T(B + S) - T(B)
gentleStar = U(B + S) - U(B)
output = T(I) + m * (gentleStar - normalStar)
```

`I` is the original linear sample, and `m` is a smooth treatment mask in [0,1]. This retains the normal rendering `T(I)` and replaces only the modeled stellar increment. When `I = B + S` and `m = 1`, it becomes `T(B) + gentleStar`. The background continues to use `T`, even though the star uses `U`. When `S = 0` or `m = 0`, the ordinary rendering is unchanged.

This is preferable to blending a less-stretched version of the entire cutout, which also changes the nebula beneath the star. It still depends on background/model accuracy; test that limitation explicitly.

Prototype `U` by smoothly interpolating the current stretch toward a gentler reference curve in the same normalized domain. An identity/linear reference is a candidate, not a universally safe choice: validate that its stellar increments are actually smaller over the applicable background/signal range. Require monotonic curves and `0 <= gentleStar <= normalStar` over the supported range. If a stretch/reference pair fails this condition, use a validated mode-specific reference or mark the combination unsupported; do not conceal the problem with per-pixel clipping of the correction. Define numerical and range checks for noisy samples so a model overestimate cannot create negative pixels or dark holes.

Start with supported scalar stretch modes after auditing their existing evaluators. Histogram equalization and other image-dependent mappings need the same frozen mapping/statistics for comparable evaluations; defer any mode that cannot satisfy this contract. Do not recompute image statistics from the treated result or feed the corrected image back into the fit.

### 4. Build the feather from both linear and stretched evidence

Use the linear fit for component separation and the ordinary stretched red contribution for visible halo extent. Reuse the bounded profile/sector analysis described below to obtain a reliable inner region and an outer boundary where stellar signal falls into background uncertainty. The detection-map radius is a starting scale, not a hard treatment cutoff.

Keep full treatment through the reliably modeled core/inner wings, then use a smooth taper with zero slope at both boundaries. The correction naturally also approaches zero as the modeled stellar signal vanishes. Do not stack arbitrary Gaussian blurs or use one fixed feather radius for every star. If the cutout contains no reliable outer background, flag the boundary rather than growing into the nebula.

Diffraction spikes need separately validated, bounded support. A successful circular core treatment must not be presented as complete if conspicuous red spikes remain. Preview unresolved halos/spikes and provide treatment boundary adjustment/exclusion. Use the same full-resolution mask definition in preview and export.

### 5. Handle filters that contribute to several RGB channels

The default operates on a physical source filter before mixing. If that filter also contributes to green or blue, its gentler stellar stretch affects those contributions too. Show the affected RGB weights in the UI rather than implying the operation changes red alone.

If source-wide adjustment cannot address a weighted mixture without unwanted changes, a later explicit **red contribution only** mode can keep the normal prepared source for green/blue and substitute the gentler version only in the red accumulator. Treat that as a separate behavior with its own tests; it is not an implicit change to filter data. The first prototype should use a simple one-filter-to-one-channel composition to establish the effect.

### 6. Integration and caching

Audit the in-memory and disk Compose paths to find the same normalized pre-stretch stage. `internal/processing/disk_compose.go` currently applies stretch during channel preparation; stellar replacement must happen at that evaluation boundary, before channel accumulation, not by modifying exported or already-clipped RGB pixels. Inspect the corresponding normal path before choosing shared helper boundaries.

Proposed focused files: `internal/processing/star_treatment_mask.go` for bounded profile/support estimation, `internal/processing/star_stretch.go` for curve evaluation and stellar-increment replacement, plus focused tests. Wire existing Compose controls narrowly after the prototype passes. Avoid full-image star-layer copies where tiled/cutout processing suffices. Ensure tile halos cover background fitting and feather support and avoid double-applying overlaps.

Cache linear fits by science identity, registration, catalog decisions and fitting options. Invalidate visible support/curve-dependent results when stretch or relevant channel settings change. Strength-only edits may reuse fits and valid masks, but must recompute the output from unchanged source data. Process in cancellable background jobs, debounce sliders, and reject results from obsolete settings.

### 7. Delivery milestones

1. **Stretch contract and pipeline audit:** identify current normal/disk evaluation stages, normalization/clipping behavior, supported stretch modes, source-to-RGB weights and reusable registration. Document unsupported combinations before UI wiring.
2. **One-star prototype:** use a known synthetic star/background and a reviewed real linear FITS cutout. Produce normal versus gentler red renderings, modeled background/star, mask and radial profiles. Establish that the star's red contribution is reduced without changing the surrounding nebula or creating a ring.
3. **Footprint milestone:** validate fitted support on faint stars, bright halos, nebulosity and blends. Add regressions for previous boundary failures before bulk application.
4. **Batch processing and normal/disk parity:** apply to accepted stars, handle overlaps once, and verify tiled/full-frame agreement, cancellation, memory behavior and unchanged input arrays.
5. **Compose controls and visual acceptance:** add strength, per-source selection/overrides, preview and per-star exclusions. Compare the user's scene types at several strengths; do not declare success from synthetic tests alone.
6. **Documentation:** update feature inventory, user guide and test audit when implementation lands. Add optional saturation diagnostics later if useful; keep post-stretch whitening an explicitly separate future option.

### 8. Required tests and release gates

- Zero strength and empty selection reproduce the existing pipeline; unsupported modes are clearly surfaced.
- Known synthetic `B + S` scenes match the expected stellar-increment equation, preserve background and reduce red stellar signal monotonically with strength for supported curves.
- A star on a sloping/colored background does not leave a dark disk, red ring, abrupt edge or bleached nebula. Measure background error separately from stellar suppression.
- Curve evaluation is finite and monotonic over supported domains; verify normalization boundaries, negative/invalid science, high signals, clipped samples and behavior near the display limits.
- Source-wide filter behavior matches its RGB weights. Green/blue-only sources remain unchanged when unselected; selected shared filters have explicitly tested effects in all their contributing channels.
- Small/faint stars, subpixel centers, elongated halos, neighboring stars and tile boundaries receive continuous treatment without double subtraction. Unreliable fits produce a reviewable skip rather than an artifact.
- Full-resolution preview/export and normal/disk implementations agree within declared floating-point tolerance. Input FITS and star-map decisions remain unchanged.
- Validate the one-star prototype visually on underlying FITS data before committing to a particular gentler reference curve or default strength. Screenshots alone cannot provide those linear measurements.

## 0. Optional later approach: post-stretch red/magenta neutralization

The user's three example composites show strongly red/pink/magenta stars. A star need not be detector-saturated, clipped, or exceptionally bright to qualify for treatment. The following direct-neutralization design is retained as an optional alternative; the primary plan above now implements the user's preference for gentler stellar stretching first.

The examples visibly include pink/magenta cores, halos and diffraction spikes. They establish the desired visual target but cannot establish detector saturation or the original filter-to-RGB mapping. A red-heavy appearance should be measured in the actual composite, rather than inferred from the name of a physical filter.

### Revised processing design

1. **Locate stars using the existing reviewed maps.** Transform accepted stellar footprints into the exact Compose output grid. Across filters, associate sources by validated geometry, resolving duplicate/ambiguous detections; do not match local catalog IDs. This registration work is required for the color workflow even though the saturation-only work below can operate per filter.
2. **Measure the color that the user actually sees.** Use floating-point RGB after the chosen filter mixing, channel stretches and color adjustments that establish the appearance, before final quantization/export. Audit both normal and disk Compose paths to choose an equivalent stage; the earlier repository inspection established the disk stretch boundary, not the complete final color pipeline. Original linear data remains the right place to assess physical saturation, but cannot alone predict the displayed star color.
3. **Separate stellar color from local nebulosity.** Estimate a robust local background in each RGB channel around each source, excluding neighboring stars and invalid samples. Start with a local plane when a constant background is inadequate. Measure the star's positive excess above that background over reliable core/inner-wing samples. Flag poor background fits or severe blends for review rather than whitening the surrounding nebula.
4. **Select red and magenta excess.** Use a brightness-normalized score on reliable stellar excess: require red to exceed green by a configurable margin; use blue to distinguish red from magenta, not to reject magenta. Requiring red to exceed both other channels would miss the user's pink/purple examples where red and blue are both strong. Add a noise/reliability floor so faint color noise does not select a source. Thresholds are visual controls to validate, not physical saturation limits.
5. **Neutralize only the stellar contribution.** Prototype blending the background-subtracted stellar RGB toward equal channel values while preserving its chosen luminance. Conceptually, for RGB `C`, background `B`, stellar excess `S = C - B`, neutral target `N = (L(S), L(S), L(S))`, and bounded weight `a`, use `C_out = B + (1-a)*S + a*N`. Define the working RGB space and luminance weights before implementing; do not assume display-encoded RGB is linear light. Handle negative/noisy residuals explicitly and skip unreliable samples. This local separation is an approximation, so test nebular edges carefully.
6. **Measure and feather the stretched footprint.** Build a separate treatment mask from the stretched red-channel profile using the procedure below. The existing detection radius is an initial search scale, not the final treatment boundary. Let `a` combine the user's strength, source eligibility, and this smooth treatment mask. Use a source-level decision with smooth spatial application, avoiding pixel-by-pixel threshold speckling. Resolve overlapping footprints once rather than repeatedly desaturating shared pixels. Preserve source position and structure; do not force every corrected pixel to full-scale white. A neutral faint star remains faint.
7. **Account for colored outer structure.** Existing circular star maps do not cover full diffraction spikes or bleed trails. Core-only correction can leave visible pink spikes in these examples. Offer a separate, bounded halo/spike treatment only after validating source association and nebula protection; do not expand masks indiscriminately. If the first release corrects only cores/inner halos, expose that limit clearly in preview and documentation.
8. **Use saturation evidence when available.** Clipped cores may need color estimated from reliable wings or an explicit uncertain flag. Missing native DQ must not prevent ordinary red/magenta star correction. Retain the original science and produce a reversible rendered result.

### Required second pass: map the star's stretched red footprint

The user reports that earlier attempts failed primarily at finding the star boundary and feathering it. Treat footprint accuracy as its own implementation milestone and release gate, before adding whitening controls. Do not merely blur the existing circular star mask or search the entire stretched image for new red objects.

**A. Anchor each measurement to an established star.** Transform the reviewed catalog center into the final composite grid, then extract a full-resolution floating-point red-channel cutout. Here, red means the final red layer after source mixing/stretch, potentially containing more than one physical filter. Permit only bounded center refinement consistent with registration error; if the bright peak moves to a nearby nebular knot, fail the association rather than following it. Read green and blue cutouts for color/background checks and later correction.

**B. Estimate the local background and usable extent together.** Begin outside the mapped core and estimate background/noise from robust annular sectors, excluding neighboring sources, invalid data and detected spikes. Inspect larger bounded cutouts if the proposed background region still contains stellar wings. Require a reliable outer background region; report a truncated/uncertain footprint if the image edge, neighbor or structured nebula prevents one. Neither an arbitrary radius nor a single brightness threshold defines the true edge.

**C. Fit the observed stretched stellar profile.** Estimate a smooth, nonnegative core/halo component above the background using multiple radial sectors, with modest ellipticity when supported. Use agreement across sectors to distinguish stellar wings from one-sided nebular structure. Fit the actual stretched profile; do not assume the unstretched Moffat parameters still describe it. Compare a robust sampled profile with a compact parametric fit on development cases before choosing the simplest adequate implementation. Preserve valid asymmetry only when supported; flag poor fits and blends instead of forcing a circular explanation. A clipped core may be anchored by reliable wings.

**D. Define two boundaries from the fit and uncertainty.** The inner boundary encloses reliably star-dominated pixels, where full selected correction is appropriate. The outer boundary is where the fitted stellar contribution becomes indistinguishable from the local background uncertainty across reliable sectors. Require sustained evidence over several samples rather than reacting to one noisy pixel. Keep these boundaries bounded by the validated cutout and source associations. The original map radius may expand for treatment only when measured wings justify it; leave the saved detection footprint unchanged.

**E. Build a smooth two-dimensional treatment mask.** Use the fitted shape for the core and halo, and taper between the inner and outer boundaries. A starting taper is `1 - 3*t*t + 2*t*t*t`, with `t` the normalized outward distance across the transition, clamped to [0,1]. It has zero slope at both ends. Use a smooth fitted boundary or normalized elliptical distance, not raw pixel contours; noisy contours produce patchy edges. Adapt transition width to measured wing extent with a minimum tied to sampling, not a fixed number of pixels for all stars. Do not multiply the completed transition by another hard threshold. Mask smoothing alone cannot fix a contaminated background fit.

**F. Apply neutralization consistently across the core and transition.** Use this same scalar treatment weight for the three RGB channels while moving only the estimated stellar contribution toward neutral. Leave the modeled background intact. Inspect whether red wing signal remains beyond the transition and whether the correction creates a dark ring or a pale patch. Those artifacts indicate a footprint/background failure; expanding blur radius is not an automatic remedy. With a colored background, a neutral stellar component need not make the entire blended edge pixel gray; forcing that would also neutralize the nebula.

**G. Handle spikes and neighboring stars explicitly.** The halo fit is not a diffraction-spike mask. Extend treatment along a spike only when direction, continuity and association with the star are supported; taper across and along it. Ambiguous blends require joint fitting or a flagged/manual treatment boundary, not unbounded region growth. Provide per-star boundary adjustment/exclusion for cases that cannot be confidently automated, and persist those choices separately from the detection catalog.

**H. Prove the mask before whitening.** Preview the treatment mask, fitted inner/outer boundaries, and a radial red-profile/background plot. Review small faint stars, large halos, bright nebular edges, crowded fields and spikes at full resolution. Include synthetic stars on known backgrounds to measure stellar-wing coverage and background leakage separately. Add regressions for residual red rings, dark rims, abrupt transitions, neighboring-nebula capture and center drift. Set numerical tolerances from reviewed source-data examples before acceptance; screenshots alone cannot establish pixel-accurate boundaries.

Changing stretch changes the measured profile, so invalidate and recompute the treatment mask from the uncorrected current composite. Never reanalyze an already-whitened preview. Debounce interactive changes, cancel obsolete jobs, and publish only the result matching the latest settings. Preview and export must use the same full-resolution mask definition; downsample for display only.

The primary plan now applies a gentler stretch to the estimated stellar contribution. This still requires the accurate separation and boundary validation described here. Direct post-stretch neutralization is a separate optional follow-up.

### Controls and recomputation

- Main control: **Neutralize red/magenta stars**, with strength and color-selectivity settings.
- Preview: before/after, affected-star overlay, and representative cutouts on both dark sky and bright nebulosity.
- Preserve map accept/exclude decisions; provide per-star treatment exclusions without overwriting detection decisions.
- Explain luminance preservation: reducing vivid red/magenta can make a star look less striking even when the chosen luminance is preserved. A separate brightness boost is not part of the initial correction.
- Recompute the stretched treatment footprint, color eligibility and output when channel assignments, weights, stretches or relevant color settings change. Physical saturation evidence remains independently cached. Recompute geometry-dependent results when alignment or masks change.
- Run analysis and application in cancellable background jobs in both normal and disk workflows, with UI-thread updates and stale-result rejection.

### Revised delivery order

1. Verify map-to-Compose registration and normal/disk color pipeline insertion points.
2. Implement and validate stretched red-footprint estimation and smooth treatment masks, proposed in `internal/processing/star_treatment_mask.go`, with synthetic backgrounds, known stellar profiles and reviewed real cutouts. Deliver mask-only previews first; successful detection alone does not satisfy this milestone.
3. Implement a pure RGB star-color assessment/correction function, proposed in `internal/processing/star_neutralization.go`, then add Compose preview and controls. Test on the supplied types of scenes using the underlying FITS/composition settings when available. Screenshots guide visual acceptance but do not replace source-data tests.
4. Validate halo/spike scope, refine background protection and demonstrate consistent normal/disk output.
5. Add the optional saturation analysis described in sections 1-10 to improve clipped-core diagnostics and treatment decisions.

Initial acceptance tests must include an unclipped red star and a magenta star both becoming more neutral; neutral and blue stars remaining unchanged by default; missing DQ not blocking correction; red nebulosity outside selected stars remaining unchanged; smooth mask edges; stable overlaps; unchanged source arrays; cancellation; and consistent preview/export. A poor background fit must be surfaced rather than silently treated as reliable. Update feature inventory, user guide and test audit when behavior is implemented.

## 1. Supporting saturation analysis: outcome and scope

Given a reviewed star map and its original linear science image, produce a per-filter assessment of which stars contain saturated core pixels, where those pixels are, and how that conclusion was reached. Preserve uncertainty so the later Wite-Star color treatment can choose an appropriate policy.

This supporting step detects and records evidence. The color-neutralization workflow is defined in section 0; saturation analysis itself does not recover clipped flux, reconstruct stars, neutralize RGB colors, or change the existing detector's acceptance decisions.

Recommended flow:

```text
Linear mosaic + reviewed star map + optional original SCI/DQ exposures
    -> validate identities, units and geometry
    -> inspect mapped stellar regions in each available exposure/filter
    -> classify individual pixels and associate core components
    -> summarize evidence per star and exposure
    -> save saturation FITS product and show review overlays
    -> later Wite-Star step consumes these results before/alongside RGB rendering
```

Here, "raw linear data" means unmodified, unstretched science values. For Hubble, calibrated FLT/FLC SCI plus matching DQ is the preferred evidence source; this feature should not introduce a new calibration pipeline for raw detector files.

## 2. What the repository already provides

| Existing component | Reuse and limitation |
| --- | --- |
| `internal/processing/star_map.go` | Source IDs, positions, FWHM, radius, manual decisions, masks and a `Saturated` boolean. That boolean does not distinguish detector measurements from inferred saturation. |
| `internal/processing/star_map_saturation.go` | Mosaic morphology can rescue damaged-core stars. This remains supporting evidence, not confirmation of measured detector saturation. |
| `internal/mosaic/star_map.go` | Reads paired WFC3/UVIS SCI/DQ, preserves observation placement and checks independent observations. Existing DQ projection rounds to an output pixel; precise core support needs more careful footprint projection. |
| `internal/mosaic/star_map_fits.go` | Science hash, grid/filter checks, catalog/provenance persistence and safe file replacement. Current reader expects version 1 and a fixed catalog row layout. |
| `internal/ui/mosaic_star_map.go` | Background processing, cancellation and star review interface to extend narrowly. |
| `internal/processing/disk_compose.go` | Disk composition can apply stretch while preparing channels. A future consumer must attach analysis before that preparation, not inspect its stretched artifacts. |

Two important implementation details:

- `StarMap.Rasterize` sets saturation bit 4 over the selected source's footprint. It is not a measured saturated-pixel mask and must not be reused as one.
- `visitStarMapEvidence` replaces certain bad SCI samples with NaNs for profile fitting. A saturation evidence reader must capture original SCI and separate validity/DQ information before this mutation. The ordinary `cleanSCIWithMatchingDQ` path also must not supply the evidence pixels.

Existing source IDs are local to a filter catalog. Matching IDs across filter files does not identify the same physical star.

## 3. Recommended evidence rules

### 3.1 Do not equate the image maximum with detector saturation

Every finite image has a maximum, even when all its stars are unsaturated. A rule such as `pixel >= 0.99 * imageMaximum` always risks marking an ordinary bright star. A top percentile similarly selects bright pixels without establishing clipping.

Use this evidence hierarchy:

1. **Instrument DQ evidence:** preferred for supported original exposures. For WFC3/UVIS, distinguish full-well bit 256 from A-to-D bit 2048. Do not apply these meanings to other detectors. STScI documents their instrument-specific meanings and calibration behavior in the [WFC3 calibration steps](https://hst-docs.stsci.edu/wfc3dhb/chapter-3-wfc3-data-calibration/3-2-uvis-data-calibration-steps) and [file structure](https://hst-docs.stsci.edu/wfc3dhb/chapter-2-wfc3-data-structure/2-2-wfc3-file-structure).
2. **Known saturation limit:** compare values only when the limit's units and calibration stage match the analyzed pixels. A header keyword is not sufficient without understanding its meaning. Record whether the limit is instrument-derived or user-specified.
3. **Mosaic inference:** clipped plateaus, damaged cores, and existing stellar-wing diagnostics may indicate probable saturation. Retain this as inferred evidence.
4. **Optional brightness heuristic:** a user-requested fraction of image maximum or percentile can flag candidates for review, but never produces confirmed saturation by itself. Exclude invalid pixels and record the exact statistic and sample population used.

Do not use FITS `BITPIX`, `DATAMAX`, display white points, or normalized value 1 as an automatic physical saturation limit. Inspect the FITS reader's scaling behavior and account for `BSCALE`/`BZERO` exactly once. Exposure time and gain alone may not undo all calibration effects; reject unsupported conversions.

### 3.2 Explicit classifications

Use separate evidence flags and a summary state, rather than one boolean:

| State | Meaning |
| --- | --- |
| Confirmed | A core-associated pixel has applicable detector saturation flags or exceeds a validated physical limit. |
| Probable | Clipping/morphology evidence suggests saturation but direct confirmation is unavailable. |
| Near limit | A valid value approaches a known applicable limit but has not met it. |
| No saturation detected | Examined valid evidence has no qualifying hits; report its coverage and method. |
| Unknown | Evidence, units, registration, or usable core coverage is insufficient. |

Retain overlapping conditions: a confirmed star can also contain near-limit pixels. Missing evidence is never a zero-valued unsaturated measurement.

For a known limit `T` in the same data domain, use:

```text
at/above limit: valid pixel v >= T
near limit:    alpha*T <= v < T
```

Propose `alpha = 0.98` as an editable initial near-limit setting, subject to real-data validation. This is a fraction of a known limit, not a percentile rank. If no applicable limit exists, near-limit analysis is unavailable. Any numeric tolerance must follow the input quantization/precision and be recorded, not silently broaden the threshold.

## 4. Spatial analysis

1. Validate science identity, star-map identity, filter, dimensions, WCS/grid and finite coordinates. Use zero-based coordinates internally and FITS one-based positions only at serialization boundaries.
2. Analyze existing catalog sources without rerunning detection. Keep accepted, excluded and uncertain selection states separate from saturation state. Only accepted/manually accepted sources become automatic candidates for later treatment; other sources can retain diagnostics for review.
3. Inspect each source's bounded footprint. Do not infer a precise core from the entire soft selection mask. Catalog geometry remains useful where a saturated core is nonfinite and therefore absent from the rasterized selection mask.
4. Find connected components of qualifying saturation evidence inside the footprint. Associate a component with the core using its distance to the fitted center and the source width. Start with a documented association distance of one FWHM, capped by the footprint radius; validate this parameter against subpixel and damaged-core examples.
5. Keep the entire associated component within the footprint, including a clipped plateau larger than that association distance. Mark saturation elsewhere in the footprint separately; do not automatically call a wing hit a saturated core. Touching the footprint boundary creates a truncation warning.
6. A single reliable DQ hit associated with a known stellar core can establish saturation. Do not require two exposures or several pixels: that would miss short-lived or undersampled saturation. Heuristic single-pixel hits need stronger support and remain probable/unknown when defects or blending prevent attribution.
7. Retain overlapping-source ambiguity explicitly. Do not use the existing single winning `LABELS` ID to force ambiguous shared pixels into one star. Bleed trails and spikes are separate diagnostics; this step does not extend selection along them.

For native evidence, transform each star neighborhood to the appropriate SCI chip using existing placement/distortion geometry. Threshold on native samples before interpolation. Project qualifying pixel footprints back to the mosaic as geometric support, not interpolated DQ integers. Conservatively intersect projected footprints with output pixels; validate subdivision/tolerance for distorted footprints. Store this as projected evidence support, not a claim that each output sample itself clipped.

Process one original exposure/chip at a time and summarize all relevant stars from it. Avoid reopening the same exposure for every star. Use bounded cutouts or sparse pixel runs where practical rather than retaining every exposure's full-frame masks.

## 5. Per-filter and per-exposure aggregation

- Analyze physical source filters independently, such as F656N and F502N, before RGB channel assignment, stretch, color weights or channel mixing.
- A native saturation flag in any contributing exposure remains evidence even when another exposure is clean. Record `saturated observations / assessable observations`, plus missing/invalid coverage and saturation type.
- Deduplicate FLT/FLC representations of an observation using the existing identity mechanism, checking collisions explicitly. Overlapping chips must not count as independent observations.
- Preserve usable clean exposure evidence separately. A mosaic may contain valid core information despite some saturated input exposures. Do not conclude that all final core information is lost from an "any exposure saturated" flag.
- Never average exposures first and then try to rediscover their detector saturation from a lower combined peak.
- The first release produces a result for each filter's existing catalog. Cross-filter association is a later bounded integration step: use validated sky/output coordinates and distance/ambiguity checks, never equal local IDs. A missing detection in another filter remains unmatched, not unsaturated.

## 6. Persistence and compatibility

Recommend a separate sibling product:

```text
working/<mosaic stem>_saturation.fits
```

This preserves reviewed star-map files and avoids changing their fixed version-1 catalog layout. Reuse safe FITS-writing primitives where possible without broad writer refactoring.

Proposed version-1 saturation product:

| Element | Contents |
| --- | --- |
| Primary header | Product/schema/algorithm versions, science grid and filter, source science hash, star-map identity and settings hash. |
| `SATFLAGS` image | Documented bits for direct-DQ support, validated-threshold support, inferred support, near-limit support, and ambiguous association. Zero means no flags, not proven clean. |
| `COVERAGE` image | Evidence assessment/availability state, distinct from saturation and ordinary science coverage. Define enum/bit meanings before implementation. |
| `SATSTARS` table | Source ID, classification/reasons, core hit counts by evidence type, finite/invalid counts, peak and its units, applicable threshold, observation counts, ambiguity/truncation status. |
| `SATEVID` table | Per-source/per-observation summaries, SCI extension, DQ types, coverage and contributing evidence identifiers. Distinguish detector-pixel counts from projected mosaic-pixel counts. |
| `INPUTS` provenance | Science/map identities, original file identities, geometry, units/conversion policy, thresholds, algorithm settings and warnings. |

Define stored count denominators explicitly; omit/null unavailable numeric measurements rather than encoding them as zero. Mask support can exist at nonfinite mosaic pixels when original DQ provides evidence, but later image treatment must still respect science validity.

Invalidate results when science, catalog positions/radii, manual decisions, registration, original evidence, thresholds or algorithm version changes. Include star-map content identity, not just its science hash, because review edits alter the analysis region. Stretch and RGB display settings do not invalidate physical saturation analysis.

Reuse current source fingerprints and verify before and after analysis/save. For originals, prefer streaming content fingerprints while loading evidence so size/mtime alone cannot silently accept replaced files. Reuse cached hashes only under a defined validity policy.

Write to a sibling temporary file, close/flush, validate cancellation and identities, then replace only an existing recognized saturation product. Reject derived saturation products as science inputs in relevant loaders. Opening a legacy star map must not invent a saturation result; missing originals should allow an explicit mosaic-only path without weakening normal provenance checks.

## 7. UI and future Wite-Star integration

Extend Star Map Review with **Analyze Saturation...** and a result overlay:

- Default mode: automatic evidence selection, with native DQ preferred when available.
- Show filter and evidence availability before analysis; unavailable originals produce a clear reduced-evidence result.
- Advanced controls: an explicit threshold with units, the known-limit near-saturation fraction, and optional brightness-heuristic mode. Validate all values before starting.
- Overlay separate colors for confirmed core support, probable support and near-limit pixels. Preserve the existing star footprint as a distinct outline.
- Source details explain the result, for example: "F656N: core saturation flagged in 2 of 6 assessable exposures; full-well evidence."
- Provide status filtering and summary counts, including unknown and partial-coverage cases.
- Run reads, analysis, projection, hashing and saving in a cancellable background goroutine with staged progress. Apply UI updates on the UI thread and discard stale results after input changes or window closure.

Later Wite-Star code should consume the per-filter evidence and current accepted-star selection. Whitening all selected stars versus only saturated stars is a separate treatment choice; neither requires changing this detection contract. Geometric changes in Compose require transforming masks with the same registration as science, with categorical support handled separately from intensity interpolation.

## 8. Implementation sequence and files

### Milestone A: data contract and pure classification

Add `internal/processing/star_saturation.go` and focused tests. Define options, classifications, evidence bits, coverage semantics, bounded core association and known-limit comparisons. Keep the existing star detector unchanged. Exit criterion: deterministic pixel/core decisions and no input mutation.

### Milestone B: native evidence and projection

Add `internal/mosaic/star_saturation.go` and tests. Reuse placement helpers from `star_map.go`; extract only a small shared evidence-reading helper if needed to access pristine SCI/DQ. Implement unit validation, instrument-specific DQ handling, observation deduplication, core support projection and per-exposure summaries. Exit criterion: synthetic native evidence lands in the correct mosaic location and retains missing/partial evidence.

### Milestone C: saved products

Add `internal/mosaic/star_saturation_fits.go` and round-trip tests. Implement the separate product, safe replacement, fingerprint checks and derived-product rejection. Add narrow loader changes only where required. Exit criterion: legacy maps still open, stale products are rejected, and failed/cancelled saves preserve existing files.

### Milestone D: review workflow

Extend `internal/ui/mosaic_star_map.go`, or place the new dialog in `internal/ui/mosaic_star_saturation.go` if that keeps responsibilities clearer. Add tests for option parsing, overlay rendering and state transitions where they exercise meaningful behavior. Exit criterion: analyze/review/save/reopen works without blocking the UI or altering source data.

### Milestone E: validation and documentation

Use selected M16/Trifid regions for developer verification, keeping development and held-out samples distinct. Add a narrow CLI entry in `cmd/starmap` if useful for reproducible saturation runs. Update `docs/feature-list.md`, `docs/user-guide.md`, and `docs/unit-test-audit.md` when implementation lands, documenting supported instruments and reduced-evidence limitations.

Cross-filter catalog fusion, RGB whitening, clipping reconstruction and broad loader/Compose refactoring remain separate follow-up work.

## 9. Validation plan and acceptance criteria

Follow `docs/unit-test-standards.md`; consult the star-map sections of `docs/unit-test-audit.md` before adding overlapping coverage.

Focused deterministic cases:

1. An unsaturated brightest star is not confirmed solely because it contains the image maximum.
2. Values below, exactly at, and above a validated threshold; near-limit boundary; invalid/unknown units; nonfinite thresholds; appropriate FITS scaling and unsupported conversions.
3. WFC3/UVIS full-well, ADC, combined and unrelated DQ bits; another detector must not inherit UVIS meanings.
4. Core versus wing hits, a one-pixel DQ core, broad plateaus, subpixel centers, hot pixels, saturated nebular neighbors, overlapping stars and clipped footprint boundaries.
5. NaN/Inf/missing cores, negative calibrated pixels, no coverage and partial coverage. Valid DQ evidence survives unusable SCI; unusable SCI cannot establish a numeric threshold crossing.
6. Translation, rotation, distortion, scale changes and FITS coordinate origins. Small projected supports must not disappear between output pixel centers.
7. One saturated plus several clean observations; duplicate FLT/FLC records; overlapping chips; missing exposures; independent per-filter outcomes.
8. Mask/catalog/provenance round trips, legacy map compatibility, malformed product rejection, stale inputs/review edits, cancellation and failed replacement.
9. Results unchanged by display stretch or RGB channel weights; source arrays and reviewed catalog remain unmodified.

Start with newly named focused tests, for example:

```powershell
go test ./internal/processing -run '^TestStarSaturation' -count=1
go test ./internal/mosaic -run '^TestStarSaturation' -count=1
go test ./internal/ui -run '^TestStarSaturation' -count=1
```

Then run existing affected star-map regressions and a desktop build. Broaden to affected-package suites if shared readers/geometry change; a full-repository suite is not the default.

Real-data checks must compare native SCI/DQ cutouts, projected support, and the mosaic for selected bright stars and hard negatives. Independently verify new FITS products with Astropy. Report confirmed/probable/near-limit/unknown counts and false positives against reviewed evidence; do not claim calibrated accuracy from visual spot checks. Measure elapsed time, peak memory and cancellation behavior on representative large mosaics before choosing a performance target.

Release gate: no maximum-only confirmed flags; no missing-evidence-as-clean results; correct per-filter attribution; verified geometry; reproducible persisted results; unchanged science/maps; responsive cancellation; and documented mosaic-only limitations.

## 10. Main remaining decisions

- Treatment policy is now clarified: first apply a gentler stretch to mapped stars in selected red-contributing filters, whether or not physically saturated. The primary plan at the top defines this workflow; section 0 preserves an optional later neutralization design.
- The proposed 98% near-limit setting and one-FWHM association distance are initial engineering parameters, not established instrument accuracy thresholds. Validate and record them.
- Initial automatic native evidence support is WFC3/UVIS, matching the repository. Other instruments require their own DQ/unit policies; generic linear mosaics remain usable with explicitly reduced evidence.
