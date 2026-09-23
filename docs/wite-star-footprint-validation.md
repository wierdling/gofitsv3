# Wite-Star footprint validation

Developer validation begun 2026-09-16. This is a source-grid appearance assessment, not calibrated background recovery or RGB acceptance. No science data or saved star-map decisions are changed.

## Real-data sample and method

Science: `TestImages/Trifid/F673N_drizzle.fits`; map: `TestImages/Trifid/working/F673N_starmap.fits`. Settings: MTF, background -0.0219, peak 0.1730, scaled peak 10, midtone 0.5. Strengths: 0.25, 0.50, 0.75.

Keep existing development cases 673, 771, 702, 906 and 972. Add sources at the 25%, 50% and 90% positions of the 239 accepted sources sorted by descending amplitude: 528, 714 and 802. These broaden the sample beyond the brightest 24; they are not a random or independently held-out accuracy set. Source 802 is relatively faint in this catalog, with catalog SNR about 61.6, rather than a near-detection-limit example. Source 528's cutout includes bright structured nebulosity.

The reproducible developer harness is `tmp/footprint-validation/main.go`. It uses the existing source reload, map validation and preview pipeline. Reports are under `tmp/footprint-validation/strength-{0.25,0.50,0.75}/index.html`; the comparison sheet is `tmp/footprint-validation/contact-sheet.png`. Run the harness only when these report directories do not already exist: publication deliberately refuses replacement.

## Observations

Six of eight selected sources produce previews at all three strengths. Source 906 fails wing-profile validation, and source 972 reaches a neighbor or cutout boundary. No acceptance thresholds were changed.

At strength 0.75, central radial-bin means (display values in [0,1]) are:

| Source | Normal | Gentler | Inner radius | Outer radius |
| --- | ---: | ---: | ---: | ---: |
| 673 | 1.000000 | 0.947263 | 9 | 14.4 |
| 771 | 1.000000 | 0.644154 | 19 | 30.4 |
| 702 | 1.000000 | 0.486415 | 17 | 27.2 |
| 528 | 1.000000 | 0.794764 | 6 | 9.6 |
| 714 | 1.000000 | 0.415170 | 6 | 9.6 |
| 802 | 0.587834 | 0.276289 | 5 | 8.0 |

Radial means beyond each outer radius plus one pixel match exactly in the saved floating-point profiles. This demonstrates bounded correction, not accuracy of the fitted background within the mask.

Visual inspection of normal/gentler/mask comparisons for 771, 702, 528 and 802 shows core reduction without an obvious abrupt circular edge in these examples. Source 771 retains conspicuous diffraction rings and spikes outside the circular treatment support. Those rings are already visible in the normal rendering; their survival is an incomplete-footprint limitation, not evidence of newly created rings. The previews therefore do not establish complete broad-halo or spike treatment. Source 528 provides a useful nebular-edge development example, but its true background beneath the star is unknown.

## Synthetic validation and discovered failure

`internal/processing/star_stretch_footprint_validation_test.go` exercises the complete fit, footprint, mask and render sequence. Cases include a faint subpixel Gaussian on a noisy sloping background, a broad smooth Gaussian with measurable wing correction beyond its catalog radius, and overlapping mapped stars that must remain unchanged through explicit skips.

A separate scene places a star with amplitude 4 and sigma 1.5 on a sloping background plus an offset broad nebular knot (amplitude 0.18, sigma 3.8, offset four pixels). The known background permits a stellar-only upper bound on correction. The original pipeline accepted the star (profile residual about 0.0845), but at radius five removed 0.02581 of display signal where the entire true stellar increment was only 0.00773. This is direct evidence of nebular leakage inside a seemingly valid footprint; checking unchanged distant pixels or the overall fit residual misses it.

The added ordinary-star guard checks directional residuals across multiple radial bins and rejects the entire footprint with an explicit ambiguous-structure reason. It does not punch holes into the feather or alter recorded samples. The regression now verifies an explicit skip, nil prepared output and unchanged input pixels/fit metadata. If preparation ever accepts this scene instead, the known-background leakage bound must still pass. Symmetric smooth halos and the subpixel sloping-background example remain usable; blends remain skipped. The noisy subpixel fixture is relatively low contrast but comfortably above the noise, not a near-detection-limit acceptance test.

The guard deliberately leaves saturated Gaussian/Moffat wing handling unchanged. It is a bounded directional-structure check, not proof that all nebulosity can be separated from stars; symmetric knots can remain ambiguous. Its engineering thresholds are not calibrated false-positive or false-negative rates.

Post-change reports are in `tmp/footprint-validation/guarded/strength-{0.25,0.50,0.75}/index.html`. The same six of eight sources remain usable at all three strengths, and all generated PNGs match the baseline exactly. This confirms no appearance change for this sample while the synthetic unsafe case now skips.

Validation: focused footprint/guard tests, the complete `internal/processing` package, the `internal/starstretchpreview` package and `git diff --check` passed. No full-repository test or vet result is claimed.

Desktop build also passed: `go build -o tmp/gofits-wite-star-footprint-preview.exe ./cmd`, using `GOCACHE=D:\goSource\GoFitsV3.gocache`. This executable includes the ordinary-star ambiguity guard.

Independent final review approved this bounded guard and validation step with no material findings. Direct mid-scan cancellation coverage remains a test gap; the new scan checks cancellation per bounded radial bin and the caller returns the context error before exposing results. Review approval does not close the broader footprint acceptance limits below.

## Acceptance limits

Do not advance to bulk Compose application based on these cutouts alone. Known-background synthetic measurements must assess leakage within the footprint, and asymmetric halos, spikes and more complex nebulosity remain separate acceptance cases. Normal/disk geometry, weighted RGB sources and export equivalence are outside this validation step.

## Update: per-star skips and blended groups

Several behaviors described above have changed.

**Per-star skips.** `PrepareStarStretchFits` no longer fails the whole fit set when one star's visible halo reaches a neighbor or its footprint shows structured (directional) residual. That star is returned with `Usable=false` and the reason; the other stars are still prepared and rendered. Likewise a nonfinite sample inside a footprint no longer fails the star in `ApplyGentlerStarStretch`: it keeps its ordinary rendering (nothing is reconstructed) and the remaining samples are treated. Both were whole-star vetoes that, on real HST mosaics with large footprints, removed exactly the brightest stars.

**Blended groups** (`star_treatment_blend.go`). Accepted sources whose cores overlap (separation below 2x the larger FWHM) are no longer skipped with "neighbor overlaps stellar core". They are clustered by `starBlendGroups` and fit jointly by `fitBlendedStarGroup`:

- one robust background plane from an annulus around the whole group, with every member excluded as a neighbor;
- non-negative amplitudes for fixed-center profiles, solved simultaneously, with a per-member width found by coordinate descent over a bounded grid (a map FWHM measured on an unresolved blend is unreliable, so a shared scale cannot describe both stars); saturated members exclude their measured clipped core and may use a Moffat wing basis;
- each accepted member receives its own `StarTreatmentFit` with the same plane, a `GroupID` and `Companions` (the other members' fitted profiles). A member below the SNR gate or with zero amplitude stays unusable but remains a companion, so a bright star's footprint can grow across a faint partner.

Map sources the detector left **uncertain** (typically "extended emission or unresolved blend") that sit within 2x FWHM of an accepted star join its group as *companion-only* members: modelled, never treated on their own, and never allowed to enlarge the fitted region; their width prior is the field PSF. Companion-only sources inside a saturated member's clipped core are plateau detections and are dropped. Explicitly rejected sources never join.

Footprint preparation, the structured-residual guard, the outer-radius measurement and the halo/spike probe subtract the companion model from each pixel (skipping companion clipped cores), and companions never bound a member's `safeLimit`. The structured-residual guard allows a model error of `max(Residual, 0.3) x companion` so a subtracted partner does not read as nebular structure. Masks and corrections still max-combine.

**Saturated wing residual.** The wing-fit residual gate now discards the 15% most deviant samples *only when those samples are azimuthally balanced* (paired diffraction spikes and rings); a one-sided excess keeps the full residual and still fails. Saturated group fits use only positive excess, as the single-star wing fit already did, so zero-coverage holes in drizzled data do not register as one-sided misfit.

**Display-defined quiet level.** Footprint growth and halo closure treat linear excess whose stretched contrast is below 2% of the display range (`starDisplayQuietLevel`) as quiet, in addition to the noise floor. Under a hard stretch this changes nothing; under a gentler one a very extended HST halo can now close before the search limit instead of leaving the star untreated. The correction at such a level is negligible, so ending the feather there produces no visible step.

**Neighbor prefilter.** Per-pixel neighbor exclusion in the background annuli scans only sources near the star (`nearbyStarSources`); on the 23k-source M16 map this took `FitStarTreatment` from 36 s to about 4 s.

Real-data check (M16 WFC3 F502N, Trifid F502N/F656N/F673N maps): the two brightest saturated M16 stars (10126, 17294; amplitudes ~90) previously failed wing validation and now render with 76-87 px validated halos; the user-accepted Trifid pair 817/819 now fits jointly (previously "blended profile does not match circular model"). Star 12462 remains skipped: it has a genuinely bright, flat-topped companion 10 px away that a profile basis cannot absorb, and the fit residual lands at 0.357 against the 0.35 gate.

Tests: `star_treatment_blend_test.go` covers an unsaturated pair (amplitudes recovered within 4%), a faint companion covered by its neighbor, a saturated pair with clipped cores, and transitive group clustering with the missing-FWHM fallback.
