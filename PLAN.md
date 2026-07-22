# Guarded RScale-to-General RANSAC Recovery Plan

## Scope and architecture constraints

- Keep the change inside `internal/processing/tweakreg_align.go` and its focused tests; do not change exported APIs, callers, matching stages, model types, UI/mosaic orchestration, or `internal/processing/ransack.go` solver behavior.
- Preserve the current matching order and thresholds exactly: 2D histogram, mutual-proximity refinement, wide-radius retry, triangle fallback, then RScale RANSAC with `300` iterations/`1.5` px and the existing optional General comparison with `2000` iterations/`1.5` px.
- Enter recovery only when `fitgeom == "rscale"`, RScale RANSAC returned an error, and at least three candidate pairs exist. A successful RScale result must continue through the current optional General-upgrade decision unchanged and must never be replaced merely because recovery exists.
- Recovery may call the existing General RANSAC with its existing `2000` iterations and `1.5` px threshold. Treat that result only as a candidate: it must pass the same downstream physical-transform, maximum-corner-shift, and global-support gates as every existing result. Do not relax pair counts, tolerances, support floors, or plausibility bounds.
- `estimateTweakRegFromCatalogs`, `FitCatalogResidual`, and `AlignChannelByStars` remain unchanged callers of `fitCatalogTransform`, so WCS alignment, mosaic chain alignment, and pixel-channel alignment inherit the behavior without duplicated fallback logic.

## Ordered implementation steps

1. [ ] Add focused regression coverage in `internal/processing/tweakreg_align_test.go` (using small deterministic catalogs, not image fixtures).
   - Add a case with at least three non-collinear candidate correspondences whose guarded RScale solve fails but whose General solve succeeds; call `fitCatalogTransform` or `FitCatalogResidual`, assert success, the expected affine mapping, populated `AlignStats`, and passage through the existing acceptance gates.
   - Add a successful-RScale control case that proves the returned transform remains the pre-recovery RScale result (and retains the existing optional-upgrade behavior/order where applicable), protecting coefficient-level behavior when RScale succeeds.
   - Add negative cases proving no broadened acceptance: fewer than three pairs retain the existing RScale failure, a failed General recovery reports failure rather than manufacturing a transform, and recovered General candidates that are nonphysical or exceed the residual-shift bound are rejected by the existing shared gates. Use the smallest set of cases that distinctly covers the guards without redundant solver tests.
   - Do not require or manufacture a recovered-General fixture rejected for fewer than three global supporters under the current solver/matcher contract. General RANSAC requires at least three inliers, each successful hypothesis exactly fits its three sampled non-collinear pairs, and the final least-squares refit cannot leave fewer than three confirmed pairs within the `2.0` px global tolerance: for `k` refit inliers its squared error is at most `(k-3)*1.5^2`, which is strictly less than `(k-2)*2.0^2`. The matchers are one-to-one, so those three pair sources count as at least three entries in `transformGlobalSupport`. A rejection fixture would therefore need a new solver seam or altered thresholds and would not represent reachable production behavior.
   - Preserve the existing direct `transformGlobalSupport` coverage, and make shared-gate routing an explicit implementation/review assertion: a recovered candidate must be assigned to the common result path, have support computed by the existing `transformGlobalSupport(projected, refStars, result, tweakRegGlobalTolPx)` call, and encounter the unchanged `support < tweakRegMinSupport` check with no recovery-specific success return or exemption. The recovery success test must assert `AlignStats.GlobalInliers`, while the reachable physicality and corner-shift rejection tests demonstrate fallthrough into the common gate block.
   - Keep tests deterministic, behavior-focused, and independent of exact debug-log wording, per `docs/unit-test-standards.md`.
   - Done when: the recovery test fails against the current early return; the successful-RScale control passes unchanged; fewer-than-three, dual-solver-failure, nonphysical, and excessive-corner-shift cases have focused coverage; the existing global-support helper test remains passing; and code review confirms recovery rejoins the single unchanged support computation/rejection path rather than attempting an unreachable low-support recovery test.

2. [ ] Implement the guarded recovery and diagnostics only in `fitCatalogTransform` in `internal/processing/tweakreg_align.go`.
   - On RScale RANSAC error, retain the current failure log context. If `len(pairs) < 3`, return that RScale error exactly as today; otherwise log that guarded General recovery is starting and invoke `SolveTransformationRANSAC(pairs, 2000, 1.5)`.
   - If General recovery fails, return an error that preserves both the original RScale failure and the General recovery failure for diagnosis; do not continue to stats or acceptance gates.
   - If General recovery succeeds, assign it as the candidate result and continue through the existing shared residual/statistics computation and all acceptance gates without special exemptions. Log recovery success with pair count, both solver outcomes/model selection, residual summary, and affine coefficients; ensure the final solved diagnostic identifies General as the effective recovered model rather than misleadingly reporting RScale.
   - Leave the RScale-success branch byte-for-byte equivalent in decisions: run the current optional General comparison only after success, apply `shouldUpgradeTweakRegFit` unchanged, and retain current thresholds, order, result selection, and diagnostics.
   - Do not add a new solver abstraction, retry loop, dependency, exported status field, or caller-side fallback.
   - Done when: only the guarded RScale-error path has new control flow; all recovered candidates face the existing false-match gates; successful RScale inputs produce the same transforms/stats; and failure logs distinguish RScale failure, recovery attempt, General failure/success, and downstream rejection.

3. [ ] Format and validate the narrow processing change.
   - Run `gofmt -w internal/processing/tweakreg_align.go internal/processing/tweakreg_align_test.go`.
   - Run the new focused tests first with `go test ./internal/processing -run "TestFitCatalogTransform.*RScale|TestFitCatalogResidual.*RScale"` (adjust the regex to the final test names).
   - Run existing solver/alignment regressions with `go test ./internal/processing -run "TestRANSACIsDeterministic|TestShouldUpgradeTweakRegFit|TestFitCatalogResidualRecoversRScale|TestTransformGlobalSupport"`.
   - Run `go test ./internal/processing`; run `go test ./...` only if package validation exposes a cross-package concern or repository policy requires it.
   - Inspect one debug trace for each of: RScale success, guarded General recovery success, General recovery failure, and recovered candidate rejected by a shared safety gate.
   - Done when: formatting is clean; focused and package tests pass; successful RScale outputs are unchanged; recovery occurs only with at least three pairs after RScale failure; malformed/implausible recoveries remain rejected; and diagnostics make the selected solver and terminal reason unambiguous.

## Overall completion criteria

- Existing RScale successes retain their transform, statistics, optional General-upgrade decision, thresholds, and execution order.
- An RScale solver failure with at least three usable candidate pairs gets exactly one General RANSAC recovery attempt using the existing General parameters.
- A recovered affine is accepted only through the same physicality, corner-shift, and catalog-corroboration gates already used by `fitCatalogTransform`, preventing a new false-positive path.
- RScale-only failures with fewer than three pairs and dual-solver failures remain errors with actionable diagnostics.
- No production file outside `internal/processing/tweakreg_align.go` changes, and all relevant `internal/processing` tests pass.
