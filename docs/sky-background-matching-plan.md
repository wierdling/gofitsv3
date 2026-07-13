# Plan: NIRCam-grade sky background equalization for mosaics

Status: PLANNED — not yet implemented. Implement **one stage per request**; do not
start a stage without being asked.

## Problem statement

JWST NIRCam mosaics (especially nebula fields) show unequal background levels
between chips in the drizzled output. NIRCam SW has 8 detectors per exposure
(4 per module, A1–A4 / B1–B4) separated by gaps; LW has 2. Chips only overlap
each other through dithers, so the overlap graph is sparse and chip-to-chip
pedestal errors accumulate across the mosaic. On nebula fields the "sky" is
dominated by real extended emission, so any method that estimates a per-chip
background from the chip's own pixels will subtract real nebulosity and make
the mismatch worse.

## What exists today (read these first)

- [internal/mosaic/skysub.go](../internal/mosaic/skysub.go) — full
  AstroDrizzle-style skymatch implementation:
  - `planSkysub` streams each frame, estimates sky (`estimateSkyValue`,
    sigma-clipped median/mode/mean), and for `SkyMethodMatch` /
    `SkyMethodGlobalMinMatch` builds a coarse overlap sample map per frame
    (`buildOverlapSampleMap`: 32-output-px cells keyed by
    `overlapCellKey`, cell value = mean of strided source samples mapped
    through `p.mapPixel` into output mosaic coordinates).
  - `computeMatchedSkyOffsets` builds pairwise edges from per-cell
    differences (`overlapDiffsForEdge`, delta = clipped stat of `vj - vi`),
    finds connected components, and solves a weighted least-squares system
    (`solveSkyComponent`) for one **scalar** offset per frame, root frame
    pinned to 0. Edge weight = sqrt(overlap cell count).
  - `computeOverlapAnchoredSkyPlanes` then fits a **per-chip plane to the
    chip's own background samples** (anchored to its overlap cells) and
    subtracts it via `applySkyPlaneInPlace`.
  - Offsets/planes are applied per frame at drizzle time in
    `prepareFramePixels` ([internal/mosaic/frame_loader.go:189](../internal/mosaic/frame_loader.go)).
  - Debug logging already exists (`skyDebugLog`, `logSkysubPlan`,
    `SKYSUB EDGE` lines) via `internal/debuglog`.
- UI: [internal/ui/skysub_settings_window.go](../internal/ui/skysub_settings_window.go)
  (settings dialog), wired through `workspace_mosaic.go`, `mosaic_build.go`,
  `mosaic_methods.go`, `mosaic_project.go` (project persistence of
  `SkysubOptions`).

## Why the current code fails on NIRCam nebulae

1. **`computeOverlapAnchoredSkyPlanes` is nebula-hostile.** It fits a plane to
   the chip's *own* background samples. On a nebula field those samples ARE
   the nebula, so each chip gets a different plane of real signal subtracted.
   Adjacent chips see different parts of the nebula → different planes →
   visible steps at chip boundaries plus destroyed nebular gradients. This is
   the most likely primary cause of the reported problem.
2. **A single scalar per chip cannot fix intra-chip structure.** NIRCam
   residuals after the STScI cal pipeline include per-amplifier 1/f banding
   (each 2048² detector is read through 4 vertical 512-px amplifier strips)
   and, on SW detectors A3/A4/B3/B4, "wisp" ghosts. Even with perfect scalar
   matching these leave stripes/blotches that read as unequal background.
3. **Sparse overlap graph → drift.** With small dithers, chips connect through
   thin overlap strips; the scalar solve is fine locally but small edge errors
   accumulate across an 8-chip × N-exposure graph. There is currently no
   post-solve residual iteration to detect/downweight bad edges.

## What STScI does (reference behavior)

- `calwebb_image3` runs the **skymatch** step with `skymethod="match"`:
  equalize backgrounds by least-squares over pairwise overlap differences —
  i.e. relative offsets only, preserving real extended emission. This is what
  `SkyMethodMatch` already mirrors. See the
  [jwst pipeline background-subtraction docs](https://jwst-pipeline.readthedocs.io/en/stable/jwst/user_documentation/background_subtraction_methods/main.html).
- Science teams (JADES, PEARLS, CEERS) additionally, on the stage-2
  (flattened, `_cal`) images and **before** background matching:
  1. subtract **wisp templates** (SW detectors),
  2. remove **1/f striping** per amplifier, row-by-row (and optionally
     column-by-column), with sources masked (Schlawin et al. 2020 approach),
  3. iterate background measurement with bright-source masking on deep fields.
- Key principle for nebulae: only *relative* (match) corrections; never
  subtract a per-chip 2D background estimated from the chip itself.

The generalization of scalar matching to structured backgrounds is the
Montage `mBgModel` approach: fit a **plane per frame from the overlap
difference data** (differences cancel the nebula), solve globally.

---

## Stage 0 — Diagnosis on the user's data (no code changes)

1. Build the mosaic with Skysub enabled, `Method = Match`, and inspect the
   `SKYSUB` / `SKYSUB EDGE` debuglog lines: per-frame rawSky, subtractSky,
   per-edge cell counts and deltas. Look for edges with few cells or deltas
   inconsistent around graph cycles.
2. Rebuild with the plane step short-circuited (temporarily make
   `computeOverlapAnchoredSkyPlanes` return invalid planes, or gate it behind
   a bool) and compare seams. Expectation: on nebula data, scalar-only is
   *better* than scalar+own-frame-plane. This confirms diagnosis #1.

### Stage 0 findings (recorded 2026-07-09, user's OMC3_SE NIRCam dataset)

Ran against real data (`jw05804002001_02103_*_nrca1/nrca3/nrcb1/nrcb2_cal.fits`),
Skysub enabled, `Method = Match`:

- `rawSky` ≈ 1–3 (plausible calibrated MJy/sr), `mapCells` mostly 1050–1100
  (healthy overlap support) — units and support are fine, not a data problem.
- `SKYSUB EDGE` deltas ranged 0.05–1.5, i.e. up to ~50% of `rawSky` on the
  weakest edges (`cells` ≈ 20–140). Well-supported edges (`cells` ≈ 500–900)
  were internally consistent: a 3-edge loop closed to within ~0.04 vs
  individual deltas of ~0.4, so the graph solve itself is trustworthy where
  support is good. The weak, low-cell edges are the ones to downweight in
  Stage 2.
- **Disabling `computeOverlapAnchoredSkyPlanes`** (temporary
  `skyDiagDisablePlaneFit = true` switch added to skysub.go for this test)
  produced **no visible change** in the drizzled mosaic vs. skysub fully off,
  even though real, non-trivial scalar corrections were confirmed to reach
  the final drizzle pass (`prepareFramePixels` at
  [internal/mosaic/drizzle.go:682](../internal/mosaic/drizzle.go)). This
  demonstrates the own-frame plane fit is **not** the dominant visible cause
  on this dataset — diagnosis #1 still stands as a real defect to fix (Stage 1
  replaces it regardless), but it is not the main driver of what the user is
  seeing, so Stage 3 should not be deprioritized relative to it.
- **A 100% zoom crop was decisive.** It shows two co-existing, distinguishable
  patterns: (a) continuous horizontal 1/f row striping with uniform
  spacing/contrast across the whole crop, and (b) sharp **vertical bands that
  are flat pedestal steps** (the striping texture passes through them
  unchanged — only the overall tone shifts), sitting *inside* a single
  chip/exposure footprint. This is the signature of NIRCam's 4-amplifier
  vertical readout structure (per-amp bias/gain zero-point offset), not
  chip-to-chip or dither-to-dither mismatch. Overlap-based sky matching
  (Stage 1) has no mechanism to correct this — there is no overlap between
  amplifier strips of the same exposure. **This elevates Stage 3 to
  co-primary with Stage 1** and expands its scope (see Stage 3 below): it
  must remove a per-amplifier pedestal step, not just high-frequency row
  banding.
- Housekeeping: `skyDiagDisablePlaneFit` in skysub.go is a temporary
  diagnostic-only switch (reverted to `false` after the comparison). Stage 1
  implementation replaces `computeOverlapAnchoredSkyPlanes` outright, at
  which point this switch should be removed (not just flipped back).

### Revised interpretation (2026-07-12, after amp-pedestal fix underperformed)

The amp-pedestal correction, once implemented and run on the real data,
measured only tiny steps at the true amplifier boundaries (`AMPPEDESTAL`
offsets 0.02–0.29 vs. sky ~1–3): STScI's stage-1 calibration already handles
amp bias well, so **amp pedestals are not the visible artifact**. A rebuild
with the correction enabled looked unchanged, including an nrca1-only build
(single detector, one connected match group) that still showed broad flat
vertical/horizontal zones.

Revised diagnosis — the flat steps are **dither coverage boundaries**: the
mosaic is tiled into zones covered by different subsets of exposures
(footprint edges are vertical/horizontal lines). Each exposure carries its own
residual level error (`SKYSUB EDGE` deltas up to ~1.5 pre-match; smaller but
nonzero after scalar match on a nebula) and its own 1/f stripe realization
(whose local mean differs exposure-to-exposure by roughly the stripe
amplitude). Where the contributing set changes, the zone average steps by
(residual error)/N. This also explains the Stage 0 zoom crop: stripes from
exposures common to both sides continue smoothly through a boundary while
only the DC level steps — previously misread as an amp boundary.

Consequences for the remaining stages:
- **1/f row destriping** attacks both the visible striping and a large share
  of the coverage-step amplitude (each exposure's stripe field integrates to
  a different regional mean). Highest expected visual payoff.
- **Stage 1 difference-fit planes** then handle the remaining smooth
  per-exposure mismatch (a scalar per exposure cannot reconcile frames that
  sample a nebula gradient at different dither positions).
- Also explains why multi-detector mosaics look worse: separate connected
  match components (e.g. nrca1 vs nrca3 dither groups that never overlap) are
  each pinned to an arbitrary independent zero-point under `match`.
  `globalmin+match` partially mitigates; Stage 1's gauge choice should
  consider anchoring components consistently.

## Stage 1 — Replace own-frame planes with difference-fit planes (core fix)

**Status: IMPLEMENTED (2026-07-12).** See
[internal/mosaic/skysub.go](../internal/mosaic/skysub.go)
(`computeDifferenceSkyPlanes`, `solveSkyPlaneComponent`,
`positionedOverlapDiffs`, `chipOutputExtent`/`componentOutputExtent`) and the
new `SkyMethodMatchPlane` (UI: "match+plane"). `computeOverlapAnchoredSkyPlanes`
(the own-frame plane fit) and its helpers were deleted outright, along with
the temporary `skyDiagDisablePlaneFit` switch — `SkyMethodMatch` and
`SkyMethodGlobalMinMatch` are now pure scalar (no plane component at all);
`SkyMethodMatchPlane` is the new opt-in method. Deviations from the design
below, both allowed by the plan's own wording:
- **Gauge:** root-frame-pinned-to-zero (matching the existing scalar solve's
  convention), not the sum-zero constraint — simpler and consistent with
  `solveSkyComponent`. `SkyMethodMatchPlane` doesn't run a separate scalar
  solve first; the joint (A,B,C) fit's C term already is the scalar case,
  so a disconnected/unmatched frame falls back directly to its own `rawSky`,
  same as `SkyMethodMatch`.
- **Regularization scale S:** each component's largest member chip's own
  output-space footprint extent (mapped via WCS), not the overlap-sample
  extent — using the overlap extent would under-regularize exactly the
  small, poorly-constrained overlaps that need it most, since the plane gets
  extrapolated across the whole chip, not just the overlap.
- Clip iterations for the joint solve are capped at 2
  (`skyPlaneClipIters`) regardless of `options.Clip`, since this is a much
  more expensive per-cell joint solve than the cheap scalar/per-value clips
  elsewhere in the file.

4 synthetic tests in
[internal/mosaic/difference_sky_plane_test.go](../internal/mosaic/difference_sky_plane_test.go)
cover: gradient recovery + nebula preservation (a smooth shared "nebula" plus
distinct per-frame injected planes, recovered to within 0.05 despite the
regularization), transitive chaining (A-B-C with no direct A-C overlap, same
scalar-equivalent case as the existing `computeMatchedSkyOffsets` chain test),
an edge below `skyMinOverlapCells` being ignored, and reference-only inputs
being excluded.

Not yet validated against the user's real data.

Montage-style: solve per-frame planes **from overlap differences only**.

Model: for frame *i*, correction `b_i(x, y) = A_i·x + B_i·y + C_i` in output
mosaic coordinates (same coordinates the overlap cell maps already use).

Data: for every edge (i, j) and every shared cell k at center `(x_k, y_k)`
(from `overlapCellCenter`), the residual to minimize is

```
r_ijk = (v_jk - v_ik) - [ b_j(x_k, y_k) - b_i(x_k, y_k) ]
```

Minimize `Σ w_ij · r_ijk²` over all frames' `(A, B, C)` jointly, per connected
component, with:

- **Gauge fixing:** the mean of all corrections over the component is
  unconstrained. Constrain `Σ_i C_i = 0` and `Σ_i A_i = Σ_i B_i = 0` (or pin
  the root frame's plane to zero, matching today's root-pinning). This keeps
  the correction purely relative — nebula flux level and large-scale gradient
  are preserved.
- **Slope regularization:** add `λ · (A_i² + B_i²) · S²` to the cost
  (S = typical chip extent in output px, so λ is dimensionless; start
  λ ≈ 0.01, expose as an advanced option only if needed). This prevents
  weakly connected edge chips from acquiring wild tilts.
- **Robustness:** reuse the existing per-cell values; iterate the solve 1–2
  times, sigma-clipping cells on `r_ijk` between iterations (same pattern as
  `fitSkyPlane`'s clip loop).

Implementation notes:

- Add `SkyMethodMatchPlane` (UI label e.g. "Match (offset + gradient)") to the
  `SkyMethod` enum in skysub.go; keep `SkyMethodMatch` as scalar-only. Default
  for new projects can stay `Match`; user opts into planes.
- The solver is a small dense system: 3 unknowns per frame, N frames → 3N×3N;
  `solveLinearSystem` already exists and is fine for N up to a few hundred.
  Build normal equations directly by iterating edges/cells (no giant design
  matrix).
- Cell data needed per edge: `(x_k, y_k, v_jk - v_ik)`. `overlapDiffsForEdge`
  currently discards positions — extend it (or add a sibling) to return
  position-tagged diffs.
- **Delete/bypass `computeOverlapAnchoredSkyPlanes`** when the new method is
  active. The existing `skyPlane` struct, `applySkyPlaneInPlace`, and the
  plumbing through `prepareFramePixels` are reused as-is — only the plane
  *fitting* changes. (Note `applySkyPlaneInPlace` evaluates the plane at
  `p.mapPixel(x, y)` — output coords — which matches the new fit domain.)
- Scalar offsets and planes can be unified: the scalar solve is the C-only
  special case. Simplest structure: solve scalars first (existing code) for
  the fallback/unmatched logic, then solve full planes for matched frames and
  fold the scalar into `C_i`.
- Persist the method choice in the project (`mosaic_project.go`) with a legacy
  default for old projects.

Tests (extend [internal/mosaic/drizzle_test.go](../internal/mosaic/drizzle_test.go)
or a new skysub_test.go, following docs/unit-test-standards.md):

- Synthetic component: 4 overlapping frames sampling a smooth synthetic
  "nebula" (e.g. a 2D Gaussian) plus per-frame injected pedestal + linear
  ramp. Assert recovered planes cancel the injected ramps to tolerance AND
  the mean correction over the component is ~0 (nebula preserved).
- Degenerate cases: single frame (no correction), chain topology (A–B–C with
  no A–C overlap), an edge below `skyMinOverlapCells`.

## Stage 2 — Robust edges + residual iteration (solver hardening)

**Status: IMPLEMENTED (2026-07-12).** See
[internal/mosaic/skysub.go](../internal/mosaic/skysub.go) and
[internal/mosaic/sky_edge_robustness_test.go](../internal/mosaic/sky_edge_robustness_test.go).

- `buildOverlapSampleMap` now keeps up to `skyOverlapCellSampleCap` (96) raw
  samples per cell and reports the **median**, not a mean, so a star or
  nebula knot sampled into a coarse cell doesn't bias it. `sampleAccum`
  changed from a sum/count pair to a bounded value slice accordingly.
- New `dropOutlierSkyEdgesAndResolve` (called from `computeMatchedSkyOffsets`,
  the scalar solver, after its normal solve): computes each edge's residual
  against the solved offsets, flags edges beyond `skyEdgeResidualSigma` (3.0)
  of the component's residual distribution, and re-solves once with them
  removed. Two things worth knowing:
  - **Median/MAD, not mean/sigma**, for the residual threshold. A first
    implementation using `meanAndSigma` failed to catch even a wildly
    inconsistent edge in testing: with few edges, a single gross outlier
    inflates the mean-based sigma enough to mask its own residual (a known
    "masking" effect). MAD-based sigma (`1.4826 * median(|residual-median|)`)
    doesn't have this problem — same pattern already used in
    `row_destripe.go`'s `buildDestripeMask`.
  - **Only applied if the drop doesn't disconnect the component**
    (`sameSkyComponent` check) — an edge that is the sole connection for some
    frame is kept even if flagged, since a noisy relative offset beats none.
    In practice a "bridge" edge's residual is always ~0 anyway (nothing else
    constrains it, so least-squares fits it exactly), so this mostly matters
    for rarer multi-edge-drop cases; the check is a cheap defensive backstop.
  - `SKYSUB EDGE` logging moved from edge-build time to post-solve, and now
    includes `residual=` alongside the existing `cells=`/`delta=` fields.
- The plane solver (`computeDifferenceSkyPlanes`/`solveSkyPlaneComponent`)
  already had Stage 1's own per-*cell* (not per-edge) sigma-clip iteration, a
  finer-grained equivalent of this hardening — not duplicated here. It now
  additionally logs one `SKYSUB PLANE EDGE ... rms=` line per edge
  (`logSkyPlaneEdges`) for diagnostic parity with the scalar path.


## Stage 3 — NIRCam per-amplifier pedestal + 1/f destriping (pre-match, per chip)

**Status: pedestal step (2026-07-09) IMPLEMENTED.** See
[internal/mosaic/amp_pedestal.go](../internal/mosaic/amp_pedestal.go) and
`SkysubOptions.AmpPedestal` / `models.SkysubSettings.AmpPedestal` (checkbox:
"Remove NIRCam per-amplifier pedestal", independent of the Enabled/Method sky
match controls above it, since it corrects an intra-chip artifact). Uses a
narrow sigma-clipped band flanking each of the 3 internal amp boundaries
(`ampBandWidth = 16` columns each side) rather than a whole-amp statistic —
deliberately more nebula-robust than the plan's original whole-strip-median
design, since a real background gradient contributes negligibly across such a
short span while a genuine amp step still shows up as a discontinuity right at
the boundary. Offsets are zero-mean re-centered so the correction is purely
relative and doesn't fight Stage 1's sky match. Note: on the user's data the
measured amp steps turned out small (see revised interpretation above) — the
correction is kept as a cheap, correct safeguard, but it is not the main fix.

**Status: row destriping (2026-07-12) IMPLEMENTED.** See
[internal/mosaic/row_destripe.go](../internal/mosaic/row_destripe.go) and
`SkysubOptions.RowDestripe` / `models.SkysubSettings.RowDestripe` (checkbox:
"Remove NIRCam 1/f row banding", independent toggle alongside the amp-pedestal
one). Per amplifier strip: source-masked row medians (coarse 64-px cell-median
background model, +3σ robust threshold) minus a 129-row smoothed trend;
only the high-frequency residual is subtracted. The trend smoother uses a
symmetric window that shrinks near the top/bottom edges, making it exactly
linear-preserving (a straight gradient is a fixed point), so real nebular
gradients are untouched everywhere including edges. Applied in
`prepareFramePixels` after the amp-pedestal step and before sky estimation,
so `planSkysub` measures sky on destriped pixels. `DESTRIPE` debuglog lines
report per-frame rows corrected, rms, and max correction.

**Priority note (2026-07-09, updated with Stage 0 findings):** a skysub-off
drizzle of the user's NIRCam nebula dataset shows strong horizontal 1/f
banding surviving into the final mosaic. A 100% zoom crop further showed
sharp *vertical, flat pedestal steps* at what are almost certainly amplifier
boundaries within single chip footprints (see Stage 0 findings above) —
confirmed **not** explained by the own-frame plane fit, which made no visible
difference when disabled. No sky matching (scalar or plane, Stage 1/2) can
correct this: it is intra-chip structure with no overlap to match against.
This stage is **co-primary with Stage 1**, not optional polish, for clean
output on this data. Scope is now two related corrections, both per
amplifier strip:

1. A **per-amplifier pedestal offset** (DC step) — this is the one the zoom
   crop shows most clearly.
2. **High-frequency row banding** (1/f) riding on top of that pedestal.

Off by default; checkbox in skysub_settings_window.go ("Remove NIRCam
per-amplifier banding"). Applied in `prepareFramePixels` (and in the
sky-planning load path) right after load, **before** sky estimation:

1. Detect amplifier geometry: 4 vertical strips of width/4 (only when the
   chip is 2048-wide-ish and instrument is NIRCam — read from header; skip
   otherwise).
2. Build a source/nebula-safe mask: heavily smooth the image (coarse boxcar
   median, e.g. 64-px bins); flag pixels > k·MAD above the smooth model.
3. **Per-amplifier pedestal:** using only unmasked pixels within each
   amplifier strip, compute a clipped median (or, better, anchor to the
   *median of the other three amps* so the correction is relative and the
   frame's overall level is unchanged — avoids fighting Stage 1's sky match).
   Subtract that per-amp scalar from the whole strip.
4. **Row banding:** per amplifier strip, per row, `rowMed = clipped median of
   unmasked pixels` (after step 3's pedestal removal). Smooth `rowMed` along y
   with a wide window (e.g. 129 rows) → `rowTrend`. Subtract only
   `rowMed - rowTrend` (the high-frequency component), so real horizontal
   nebular structure (low-frequency) is untouched.
5. Optionally repeat step 4 for columns (full-width, not per-amp).

Order matters: pedestal (DC per amp) before row banding (AC within amp), so
the row-trend smoothing isn't biased by a step change at its window edges.

Test: synthetic frame = smooth gradient + injected per-amp pedestal offsets +
injected per-row offsets + fake stars; assert both the amp steps and the row
banding are removed to tolerance while the gradient survives.

## Stage 4 — Backlog / explicitly out of scope for now

- **Wisp templates**: proper removal needs STScI wisp template files per
  detector/filter; large dependency surface. Document the limitation; the
  match solve + drizzle rejection partially mitigates. Revisit only on user
  request.
- Full 2D (higher-than-planar) difference surfaces: only if planes prove
  insufficient on real data.

## Validation on real data (after each stage)

- User's NIRCam nebula dataset: build LW and SW mosaics; visually inspect
  chip seams at aggressive stretch.
- Quantitative seam metric (can be eyeballed from debuglog first, automated
  later if useful): median |difference| across chip-boundary pixels in the
  drizzled output, before vs after.
- Regression: rerun an HST ACS mosaic (existing validated case) to confirm
  scalar `Match` behavior is unchanged when the new method is not selected.

## Sources

- [jwst pipeline: background subtraction methods](https://jwst-pipeline.readthedocs.io/en/stable/jwst/user_documentation/background_subtraction_methods/main.html)
- [JADES NIRCam reduction (wisps, 1/f per-amplifier destriping, iterative background)](https://arxiv.org/pdf/2306.02466)
- [PEARLS reduction (Schlawin 2020-style 1/f modeling per amplifier)](https://arxiv.org/pdf/2209.04119)
- Montage `mBgModel` (plane-per-frame background rectification from overlap differences) — algorithmic basis for Stage 1.
