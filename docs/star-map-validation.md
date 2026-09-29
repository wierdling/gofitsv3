# Star-map implementation and validation

Initial implementation, 2026-09-12. This is a conservative first version of the broader `star-map-plan.md`, not a claim that its numerical accuracy targets have been met. No subagents were used for implementation or review.

## Delivered

- Separate Go detector with local noise/background, compact peak proposals, analytic Moffat-profile fitting, empirical radial-profile consistency checks when enough isolated seeds exist, finite feathered footprints, and explicit uncertain classifications. The alignment detector is unchanged.
- Saturated-core seeds from native WFC3/UVIS DQ bits 256/2048, robust fitting of unsaturated wings with center refinement, and independent exposure-profile confirmations. Four-quadrant support and a fitted width constraint limit false stars from elongated structures. Spikes/bleed-associated nearby candidates can remain uncertain.
- Saved-mosaic evidence loading uses explicitly selected originals and validated working-image alignment sidecars. Current workspace builds capture their contributing inputs and result geometry. Native masks follow paired SCI geometry, including distortion and manual placement; DQ WCS is not used.
- Primary soft FITS mask, integer LABELS/FLAGS images, binary STARS catalog, and a binary INPUTS table containing JSON provenance. Primary coordinates/WCS match the science mosaic. STARS X/Y use FITS one-based pixel coordinates. IDs are stable for a repeated identical run, but are local to one filter's catalog.
- The source pixel SHA-256 and normalized output grid/filter protect saved review reopening. Original evidence file size/mtime is checked before saving/reopening. Save writes a sibling temporary product, flushes/closes it, and uses the existing safe-replacement routine. Non-star-map files cannot be overwritten through this writer.
- Compose > Create Star Map supports a current drizzle result, saved primary-image mosaic, or reopening its existing map. Review shows a full-resolution local stamp with footprint, status/reason, fit diagnostics and exposure counts. Accept/exclude/automatic decisions and radius changes persist in FITS. Recreating replaces existing review edits; reopening retains them.
- Full image processing and serialization run in cancellable background jobs. Only small review stamps render on the UI thread. A detection result is not attached to a changed current mosaic. Derived map files are rejected as science inputs.
- `cmd/starmap` provides reproducible command-line runs; `cmd/starmap/review.py` independently verifies FITS and exports an overview plus every selected source stamp. Python/Astropy/NumPy/Matplotlib are developer diagnostics only, not new application dependencies.

## Trifid results

Maps were generated in `TestImages/Trifid/working/`, without modifying the original observations or mosaics.

| Map | Evidence | Selected | Uncertain | Selected saturation-tagged sources |
|---|---|---:|---:|---:|
| F673N_starmap.fits | Six native FLC observations | 117 | 1145 | 7 |
| F656N_starmap.fits | Six native FLC observations | 30 | 1342 | 1 |
| F502N_starmap.fits | Mosaic only | 100 | 437 | 0 (native flags unavailable in this mode) |

Each of the seven selected F673N saturated sources was confirmed in all six original exposures. These are source detections, not counts of flagged detector pixels. Two additional saturation-associated candidates near bright stars remain uncertain. All 117 selected F673N stamps and the 30 selected F656N stamps were inspected and looked stellar at the reviewed scale, including stars over nebular backgrounds. This is developer visual review on the development dataset, not independently labeled validation.

The F502N working-image sidecars reference an older F502N reference-file identity. Native verification correctly refused those stale records. No sidecar or reference timestamp was edited to bypass that check. Re-aligning against the current reference and producing a new mosaic/sidecars allows an evidence-backed F502N run. Its present map is explicitly mosaic-only and has lower evidential support.

Measured end-to-end CLI runs were about 5 seconds for the F673N/F656N native-evidence paths and about 2 seconds for mosaic-only F502N on this machine. These are indicative single-run timings with warm filesystem/build caches, not performance guarantees or peak-memory measurements.

## Checks performed

- Focused `go test ./internal/processing ./internal/mosaic ./internal/ui ./cmd/starmap -run '^TestStarMap' -count=1`.
- Broader affected-package suites: `go test ./internal/processing ./internal/mosaic ./internal/ui -count=1` passed. The mosaic suite took about 63 seconds. No full-repository suite was run.
- Desktop build: `go build -o tmp/gofits-starmap.exe ./cmd`.
- Independent Astropy `verify('exception')` on all three products; verified matching mosaic WCS/dimensions, finite 0-1 masks and zero selection at nonfinite science pixels. Existing science-file header warnings did not occur for the new map products.
- Unit scenarios include synthetic stars on slopes, broad/saturated knots, ridges and hot pixels, saturated-core recovery, NaN/edge rendering, overrides, finite mask boundaries, cancellation, FITS block alignment/catalog round trips, saved science mismatch, SCI/DQ pairing, native bit handling, duplicate observation suppression, independent-confirmation requirements, and distorted/direct-versus-combined geometry.
- Source diff inspection preserved the user's pre-existing drizzle metadata fixes and tests.

Example saved-mosaic run (mosaic-only):

```powershell
go run ./cmd/starmap -input TestImages/Trifid/F502N_drizzle.fits
```

For native evidence, add `-reference` with the alignment-reference FITS and `-originals` with a comma-separated list of the filter's original FLC/FLT paths. The F673N/F656N supplied sidecars use `TestImages/Trifid/F502N_drizzle.fits` as reference. Stale sidecars fail explicitly.

Independent review:

```powershell
python cmd/starmap/review.py TestImages/Trifid/F673N_drizzle.fits TestImages/Trifid/working/F673N_starmap.fits tmp/starmap-review-F673N
```

## Limits and next accuracy work

The implementation does not yet establish the proposed 99% precision / 95% bright-star recall targets. There is no independently labeled spatial holdout or measured nebular-footprint leakage. Conservative selection sacrifices completeness; the large uncertain catalogs include genuine stars as well as artifacts/nebular candidates.

Current analysis is per-filter. It does not yet fuse cross-filter catalogs or perform joint multi-band fitting; IDs are not shared across filters. The empirical model is a radial consistency check rather than a spatially varying full 2-D PSF. Native fitting uses local residual background noise, not full ERR/correlated-noise likelihood modeling. Fixed pixel search/fit scales target these HST-like mosaics; unusual sampling or PSFs need further validation.

The outer 12 pixels are not searched. Close blends are not jointly deblended. Spikes and bleeding are not reconstructed or automatically selected as separate artifact footprints. Masks identify bounded core/wing selections, not stellar flux, a full star-removal model, or recovered clipped measurements. There is no RGB whitening behavior in this change.

Next high-value work is a versioned human-reviewed benchmark with certain/ambiguous labels and held-out regions, measured precision/recall and footprint leakage, plus joint cross-filter confirmation to improve completeness without selecting nebula. The UI was compiled and its stamp rendering tested; full interactive UI race/close/cancellation testing remains a follow-up.
