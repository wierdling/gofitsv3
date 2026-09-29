package processing

import (
	"math"
	"strings"
	"testing"
)

func tweakRegTestStars(points ...[2]float64) []Star {
	stars := make([]Star, len(points))
	for i, point := range points {
		stars[i] = Star{X: point[0], Y: point[1]}
	}
	return stars
}

func TestAlignmentStatsUsesMatchedPairConsensus(t *testing.T) {
	pairs := []MatchedPair{{RefX: 0, RefY: 0, TargetX: 0, TargetY: 0}, {RefX: 10, RefY: 0, TargetX: 10, TargetY: 0}, {RefX: 0, RefY: 10, TargetX: 0, TargetY: 10}, {RefX: 10, RefY: 10, TargetX: 13, TargetY: 10}}
	s := alignmentStats([]Star{{}, {}, {}, {}}, []Star{{}, {}, {}, {}}, pairs, IdentityTransform(), 3)
	if s.MatchedStars != 4 || s.AcceptedStars != 3 || s.RejectedStars != 1 || s.RANSACInlierPercent != 75 {
		t.Fatalf("consensus stats = %+v", s)
	}
	if len(s.Residuals) == 0 {
		t.Fatalf("residual diagnostics missing: %+v", s)
	}
}

func tweakRegTestTransform(t AffineTransform, points []Star) []Star {
	transformed := make([]Star, len(points))
	for i, point := range points {
		transformed[i] = Star{
			X: t.A*point.X + t.B*point.Y + t.C,
			Y: t.D*point.X + t.E*point.Y + t.F,
		}
	}
	return transformed
}

func tweakRegTestPairs(projected, refStars []Star) []MatchedPair {
	if len(projected) != len(refStars) {
		panic("test catalogs must have the same length")
	}
	pairs := make([]MatchedPair, len(projected))
	for i := range projected {
		pairs[i] = MatchedPair{
			RefX: projected[i].X, RefY: projected[i].Y,
			TargetX: refStars[i].X, TargetY: refStars[i].Y,
		}
	}
	return pairs
}

func assertRScaleFailsAndGeneralSolves(t *testing.T, projected, refStars []Star) {
	t.Helper()
	pairs := tweakRegTestPairs(projected, refStars)
	if _, err := SolveRScaleTransformationRANSAC(pairs, 300, 1.5); err == nil {
		t.Fatal("expected the non-similarity fixture to fail RScale RANSAC")
	}
	if _, err := SolveTransformationRANSAC(pairs, 2000, 1.5); err != nil {
		t.Fatalf("expected General RANSAC to produce a candidate: %v", err)
	}
}

func assertAffineNear(t *testing.T, got, want AffineTransform, tolerance float64) {
	t.Helper()
	values := [][2]float64{
		{got.A, want.A}, {got.B, want.B}, {got.C, want.C},
		{got.D, want.D}, {got.E, want.E}, {got.F, want.F},
	}
	for i, value := range values {
		if math.Abs(value[0]-value[1]) > tolerance {
			t.Fatalf("coefficient %d = %.12f, want %.12f", i, value[0], value[1])
		}
	}
}

func TestFitCatalogTransformRScaleFailureRecoversWithGeneral(t *testing.T) {
	projected := tweakRegTestStars([2]float64{40, 40}, [2]float64{160, 40}, [2]float64{40, 160})
	want := AffineTransform{A: 1, B: 0.08, C: 6, D: 0, E: 1, F: -5}
	refStars := tweakRegTestTransform(want, projected)

	got, stats, pairs, err := fitCatalogTransform(projected, refStars, 300, 300, 30, "rscale")
	if err != nil {
		t.Fatalf("expected guarded General recovery to succeed, got %v (matched pairs=%d)", err, len(pairs))
	}
	assertAffineNear(t, got, want, 1e-9)
	if stats.MatchedStars != 3 || stats.GlobalInliers != 3 {
		t.Fatalf("stats = %+v, want 3 matched and 3 global inliers", stats)
	}
	if stats.RMS > 1e-9 || stats.MaxError > 1e-9 {
		t.Fatalf("stats residuals = RMS %.12f, max %.12f; want zero", stats.RMS, stats.MaxError)
	}
}

func TestFitCatalogTransformSuccessfulRScaleRemainsUnchanged(t *testing.T) {
	projected := tweakRegTestStars(
		[2]float64{20, 20}, [2]float64{180, 20}, [2]float64{20, 180},
		[2]float64{180, 180}, [2]float64{80, 240}, [2]float64{240, 80},
	)
	want := AffineTransform{A: 1, B: 0, C: 5, D: 0, E: 1, F: -4}
	refStars := tweakRegTestTransform(want, projected)
	refStars[1].X += 0.4
	refStars[4].Y -= 0.3

	got, stats, pairs, err := fitCatalogTransform(projected, refStars, 300, 300, 20, "rscale")
	if err != nil {
		t.Fatalf("expected RScale fit to succeed, got %v", err)
	}
	if len(pairs) != 6 || stats.MatchedStars != 6 || stats.GlobalInliers != 6 {
		t.Fatalf("matched pairs/stats = %d/%+v, want 6 pairs and full support", len(pairs), stats)
	}
	wantRScale, err := solveRScaleLeastSquares(pairs)
	if err != nil {
		t.Fatalf("solveRScaleLeastSquares returned error: %v", err)
	}
	wantGeneral, err := solveLeastSquares(pairs)
	if err != nil {
		t.Fatalf("solveLeastSquares returned error: %v", err)
	}
	if math.Hypot(got.A-wantGeneral.A, got.B-wantGeneral.B)+math.Hypot(got.D-wantGeneral.D, got.E-wantGeneral.E) < 1e-6 {
		t.Fatal("expected General comparison to produce a distinguishable candidate")
	}
	if shouldUpgradeTweakRegFit(pairs, wantRScale, wantGeneral, 300, 300) {
		t.Fatal("expected established upgrade gate to retain the successful RScale result")
	}
	assertAffineNear(t, got, wantRScale, 1e-9)
}

func TestPreferIdentityTweakRegFitRequiresStrictSupportImprovement(t *testing.T) {
	projected := tweakRegTestStars(
		[2]float64{20, 20}, [2]float64{40, 20}, [2]float64{20, 40},
		[2]float64{120, 20}, [2]float64{140, 20}, [2]float64{120, 40}, [2]float64{140, 40},
		[2]float64{220, 20}, [2]float64{240, 20}, [2]float64{220, 40},
	)
	refStars := make([]Star, len(projected))
	for i, star := range projected {
		shift := 0.0
		if i >= 3 && i < 7 {
			shift = 4
		} else if i >= 7 {
			shift = 8
		}
		refStars[i] = Star{X: star.X + shift, Y: star.Y}
	}
	identity := AffineTransform{A: 1, E: 1}
	fourPixel := AffineTransform{A: 1, C: 4, E: 1}
	eightPixel := AffineTransform{A: 1, C: 8, E: 1}
	twentyPixel := AffineTransform{A: 1, C: 20, E: 1}
	identityStats := statsForTransform(projected, refStars, identity)
	fourPixelStats := statsForTransform(projected, refStars, fourPixel)
	eightPixelStats := statsForTransform(projected, refStars, eightPixel)
	twentyPixelStats := statsForTransform(projected, refStars, twentyPixel)
	if identityStats.GlobalInliers != 3 || twentyPixelStats.GlobalInliers >= 3 || eightPixelStats.GlobalInliers != 3 || fourPixelStats.GlobalInliers != 4 {
		t.Fatalf("fixture support = identity %d, +20 %d, +8 %d, +4 %d; want 3, <3, 3, 4", identityStats.GlobalInliers, twentyPixelStats.GlobalInliers, eightPixelStats.GlobalInliers, fourPixelStats.GlobalInliers)
	}

	for _, tc := range []struct {
		name      string
		candidate AffineTransform
		stats     AlignStats
	}{
		{name: "lower", candidate: twentyPixel, stats: twentyPixelStats},
		{name: "equal", candidate: eightPixel, stats: eightPixelStats},
	} {
		got, stats, preferred := preferIdentityTweakRegFit(projected, refStars, tc.candidate, tc.stats)
		if !preferred {
			t.Fatalf("%s candidate support %d: expected identity preference", tc.name, tc.stats.GlobalInliers)
		}
		if got != identity || stats != identityStats {
			t.Fatalf("%s candidate support %d: identity result = %+v, stats=%+v; want %+v", tc.name, tc.stats.GlobalInliers, got, stats, identityStats)
		}
	}

	got, stats, preferred := preferIdentityTweakRegFit(projected, refStars, fourPixel, fourPixelStats)
	if preferred || got != fourPixel || stats != fourPixelStats {
		t.Fatalf("strictly improved candidate = %+v, stats=%+v, preferred=%v", got, stats, preferred)
	}
}

func TestRefineGlobalAffinePreservesIdentityOnEqualSupport(t *testing.T) {
	target := tweakRegTestStars(
		[2]float64{20, 20}, [2]float64{180, 20}, [2]float64{20, 180},
		[2]float64{180, 180}, [2]float64{80, 240}, [2]float64{240, 80},
	)
	ref := tweakRegTestTransform(AffineTransform{A: 1, C: 1, E: 1}, target)
	identity := AffineTransform{A: 1, E: 1}
	got, stats := refineGlobalAffine(target, ref, identity, statsForTransform(target, ref, identity), 300, 300)
	if got != identity || stats.GlobalInliers != 6 {
		t.Fatalf("refinement = %+v, stats=%+v; want identity with full support", got, stats)
	}
}

func TestTransformGlobalSupportUsesDistinctReferenceStars(t *testing.T) {
	projected := tweakRegTestStars([2]float64{10, 10}, [2]float64{10.5, 10})
	refStars := tweakRegTestStars([2]float64{10, 10})
	if got := transformGlobalSupport(projected, refStars, AffineTransform{A: 1, E: 1}, 2); got != 1 {
		t.Fatalf("support = %d, want one-to-one support of 1", got)
	}
}

func TestFitCatalogTransformRScaleFailureDoesNotRecoverBelowThreePairs(t *testing.T) {
	projected := tweakRegTestStars([2]float64{50, 50}, [2]float64{50.00000001, 50})
	refStars := tweakRegTestStars([2]float64{50, 50}, [2]float64{50.00000001, 50})

	got, stats, pairs, err := fitCatalogTransform(projected, refStars, 300, 300, 5, "rscale")
	if err == nil {
		t.Fatal("expected fewer-than-three candidate pairs to remain rejected")
	}
	if !strings.Contains(err.Error(), "degenerate rscale transform") {
		t.Fatalf("error = %q, want the existing RScale failure", err)
	}
	if got != (AffineTransform{}) || stats != (AlignStats{}) || len(pairs) != 2 {
		t.Fatalf("unexpected result for fewer-than-three pairs: transform=%+v stats=%+v pairs=%d", got, stats, len(pairs))
	}
}

func TestFitCatalogTransformGeneralNeedsFourthCatalogSupport(t *testing.T) {
	projected := tweakRegTestStars([2]float64{40, 40}, [2]float64{160, 40}, [2]float64{40, 160}, [2]float64{160, 160})
	want := AffineTransform{A: 1, B: 0.08, C: 6, D: 0, E: 1, F: -5}
	refStars := tweakRegTestTransform(want, projected[:3])
	refStars = append(refStars, Star{X: 500, Y: 500})
	_, _, pairs, err := fitCatalogTransform(projected, refStars, 300, 300, 30, "general")
	if err == nil || !strings.Contains(err.Error(), "fit corroborated by too few stars") {
		t.Fatalf("expected fourth-support rejection, got err=%v pairs=%d", err, len(pairs))
	}
}

func TestFitCatalogTransformRScaleAndGeneralFailure(t *testing.T) {
	projected := tweakRegTestStars([2]float64{30, 50}, [2]float64{110, 50}, [2]float64{190, 50})
	refStars := tweakRegTestStars([2]float64{30, 50}, [2]float64{110, 50}, [2]float64{190, 70})

	got, stats, pairs, err := fitCatalogTransform(projected, refStars, 300, 300, 30, "rscale")
	if err == nil {
		t.Fatal("expected both RScale and General solves to fail for collinear correspondences")
	}
	if !strings.Contains(err.Error(), "rscale RANSAC failed") || !strings.Contains(err.Error(), "general recovery failed") {
		t.Fatalf("error = %q, want both RScale and General recovery context", err)
	}
	if got != (AffineTransform{}) || stats != (AlignStats{}) || len(pairs) != 3 {
		t.Fatalf("unexpected dual-failure result: transform=%+v stats=%+v pairs=%d", got, stats, len(pairs))
	}
}

func TestTweakRegGeneralCandidateStillFacesFalsePositiveGates(t *testing.T) {
	tests := []struct {
		name      string
		wantErr   string
		transform AffineTransform
	}{
		{name: "nonphysical", wantErr: "transform implausible", transform: AffineTransform{A: 1.4, B: 0.1, C: 2, D: 0.1, E: 1.3, F: -3}},
		{name: "corner shift", wantErr: "residual shift too large", transform: AffineTransform{A: 1, B: 0.08, C: 151, E: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projected := tweakRegTestStars([2]float64{40, 40}, [2]float64{160, 40}, [2]float64{40, 160})
			searchRadius := 80.0
			if tt.name == "corner shift" {
				projected = tweakRegTestStars([2]float64{0, 0}, [2]float64{1000, 0}, [2]float64{0, 1000})
				searchRadius = 2000
			}
			refStars := tweakRegTestTransform(tt.transform, projected)
			assertRScaleFailsAndGeneralSolves(t, projected, refStars)
			_, _, pairs, err := fitCatalogTransform(projected, refStars, 300, 300, searchRadius, "rscale")
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want General recovery candidate rejected by %s gate (pairs=%d)", err, tt.wantErr, len(pairs))
			}
		})
	}
}

func TestShouldUpgradeTweakRegFitForFieldShear(t *testing.T) {
	pairs := []MatchedPair{
		{RefX: 0, RefY: 0, TargetX: 2, TargetY: -3},
		{RefX: 100, RefY: 0, TargetX: 104, TargetY: -2},
		{RefX: 0, RefY: 100, TargetX: 1, TargetY: 99},
		{RefX: 100, RefY: 100, TargetX: 103, TargetY: 100},
		{RefX: 50, RefY: 200, TargetX: 55, TargetY: 197},
		{RefX: 180, RefY: 220, TargetX: 191.2, TargetY: 199.6},
		{RefX: 240, RefY: 40, TargetX: 248.8, TargetY: 20.2},
		{RefX: 260, RefY: 260, TargetX: 276.8, TargetY: 234.6},
	}
	rscale, err := solveRScaleLeastSquares(pairs)
	if err != nil {
		t.Fatalf("solveRScaleLeastSquares returned error: %v", err)
	}
	general, err := solveLeastSquares(pairs)
	if err != nil {
		t.Fatalf("solveLeastSquares returned error: %v", err)
	}
	if !shouldUpgradeTweakRegFit(pairs, rscale, general, 300, 300) {
		t.Fatal("expected field-dependent distortion to upgrade from rscale to general")
	}
}

func TestShouldUpgradeTweakRegFitKeepsGoodRScale(t *testing.T) {
	pairs := []MatchedPair{
		{RefX: 0, RefY: 0, TargetX: 5, TargetY: -4},
		{RefX: 100, RefY: 0, TargetX: 115, TargetY: -4},
		{RefX: 0, RefY: 100, TargetX: 5, TargetY: 106},
		{RefX: 100, RefY: 100, TargetX: 115, TargetY: 106},
		{RefX: 50, RefY: 180, TargetX: 60, TargetY: 194},
		{RefX: 220, RefY: 140, TargetX: 247, TargetY: 150},
	}
	rscale, err := solveRScaleLeastSquares(pairs)
	if err != nil {
		t.Fatalf("solveRScaleLeastSquares returned error: %v", err)
	}
	general, err := solveLeastSquares(pairs)
	if err != nil {
		t.Fatalf("solveLeastSquares returned error: %v", err)
	}
	if shouldUpgradeTweakRegFit(pairs, rscale, general, 300, 300) {
		t.Fatal("expected good rscale fit to remain rscale")
	}
}
