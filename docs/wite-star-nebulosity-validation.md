# Complex-nebulosity validation

Scope: source-grid gentler-star-stretch previews, before Compose integration. This check uses analytic scenes with separately known stellar and nebular contributions; real Trifid cutouts provide regression evidence, not background truth.

## Baseline failures

The independent fixture places a compact Gaussian star on a sloping background with a curved bright rim, offset knot, or narrow filament. A clean plane is the positive control. In display units, correction must not exceed `1.5 * known stellar increment + 0.003` at any treated pixel. This engineering tolerance detects substantial nebular removal; it is not a calibrated astrophysical accuracy guarantee.

Before the fix, the curved rim passed preparation but violated this bound: correction 0.0065048 versus known stellar increment 0.0000311 at the worst pixel. Maximum correction/stellar-increment ratios were approximately 209 for the rim and 45.3 for the filament (ratios exclude increments below 1e-5). The filament remained within the absolute tolerance; a large ratio alone is not a failed oracle. The offset knot already produced an explicit ambiguity skip. The planar control passed, with maximum ratio approximately 0.76.

## Identifiability limit

A centered nebular knot with the same profile as a stellar component can produce identical single-filter pixels. Neither a smooth mask nor radial symmetry proves the origin of that light. An explicit identical-observation test records this limitation. The validation does not establish protection against every symmetric knot, overlapping structure, or nebular feature within a genuine diffraction spike.

## Implementation and validation

The ordinary-star directional guard compares the strongest radial-sector residual with the fitted Gaussian tail and an explicit noise margin. The legacy peak-relative threshold caps this new limit, so adding sensitivity in the faint tail cannot weaken existing inner-footprint rejection. The existing directional contrast and repeated-bin requirements remain. Rejection applies to the whole footprint, preserving smooth masks instead of introducing local holes. Symmetric halos remain eligible; validated spike support remains excluded from this directional test. Saturated stars continue to use their separate wing-validation path and are not covered by this new guard.

Known-background tests pass for the curved rim, rotated/subpixel rim, offset knot, filament and planar control. A nonidentity MTF midpoint of 0.25 also safely rejects the rim. The centered-knot test compares fit, mask and rendered outcomes for identical observations; it records ambiguity rather than claiming rejection is possible from those pixels.

| Scene | Final outcome |
| --- | --- |
| Curved rim, Linear and MTF | Explicit whole-star ambiguity skip |
| Rotated/subpixel rim | Explicit whole-star ambiguity skip |
| Offset knot | Explicit whole-star ambiguity skip |
| Filament | Rendered; maximum excess over the oracle allowance -0.0014861 |
| Clean plane | Rendered; maximum correction/stellar increment 0.761905 |

These deliberately high-SNR analytic fixtures isolate background-model errors. Existing noisy-star and broad-halo tests remain regression controls; the new scenes do not sample an instrument-calibrated noise population.

The fixed eight Trifid sources retain six usable previews at strengths 0.25, 0.50 and 0.75. Sources 906 and 972 remain skipped for their original wing-profile and boundary reasons. Source 771 retains its halo to 87.4 pixels and four arms ending at 118–148 pixels; source 673 retains its 46-pixel halo. All 60 generated PNG hashes match the prior halo/spike reports. Reports regenerated after the review fix: `tmp/nebulosity-validation-reviewed/strength-{0.25,0.50,0.75}/index.html`.

`go test ./internal/processing ./internal/starstretchpreview -count=1`, `go build -o tmp/gofits-wite-star-nebulosity-preview.exe ./cmd`, and `git diff --check` passed using `GOCACHE=D:\goSource\GoFitsV3.gocache` for Go commands. Independent review identified the need to retain the legacy inner-footprint threshold cap; that correction and a weak inner-directional regression were added. Follow-up independent review passed with no material findings on 2026-09-19. This completes the bounded validation step, with the symmetric-knot, saturated-background and instrument-noise limitations above retained.
