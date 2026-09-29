package processing

import (
	"math"
	"testing"

	"gofitsv3/internal/fitsio"
)

// TestRANSACIsDeterministic verifies the solvers return identical results for
// identical inputs across runs. Previously the seed came from the wall clock, so
// the same pairs could yield different transforms on different runs — the root
// of the intermittent large misalignments.
func TestRANSACIsDeterministic(t *testing.T) {
	// 10 pairs consistent with Target = Ref + (5,-3), plus 2 gross outliers.
	var pairs []MatchedPair
	refs := [][2]float64{
		{10, 12}, {120, 30}, {200, 80}, {60, 150}, {300, 220},
		{45, 260}, {180, 300}, {330, 60}, {90, 90}, {250, 140},
	}
	for _, r := range refs {
		pairs = append(pairs, MatchedPair{RefX: r[0], RefY: r[1], TargetX: r[0] + 5, TargetY: r[1] - 3})
	}
	pairs = append(pairs,
		MatchedPair{RefX: 50, RefY: 50, TargetX: 999, TargetY: -400},
		MatchedPair{RefX: 70, RefY: 200, TargetX: -250, TargetY: 800},
	)

	a1, err1 := SolveTransformationRANSAC(pairs, 2000, 1.5)
	a2, err2 := SolveTransformationRANSAC(pairs, 2000, 1.5)
	if err1 != nil || err2 != nil {
		t.Fatalf("RANSAC errors: %v / %v", err1, err2)
	}
	if a1 != a2 {
		t.Fatalf("SolveTransformationRANSAC nondeterministic:\n%+v\n%+v", a1, a2)
	}
	// And it should recover the true translation, not lock onto outliers.
	if math.Abs(a1.C-5) > 0.5 || math.Abs(a1.F-(-3)) > 0.5 || math.Abs(a1.A-1) > 0.01 || math.Abs(a1.E-1) > 0.01 {
		t.Fatalf("affine RANSAC did not recover translation: %+v", a1)
	}

	r1, err1 := SolveRScaleTransformationRANSAC(pairs, 300, 1.5)
	r2, err2 := SolveRScaleTransformationRANSAC(pairs, 300, 1.5)
	if err1 != nil || err2 != nil {
		t.Fatalf("rscale RANSAC errors: %v / %v", err1, err2)
	}
	if r1 != r2 {
		t.Fatalf("SolveRScaleTransformationRANSAC nondeterministic:\n%+v\n%+v", r1, r2)
	}
	if math.Abs(r1.C-5) > 0.5 || math.Abs(r1.F-(-3)) > 0.5 {
		t.Fatalf("rscale RANSAC did not recover translation: %+v", r1)
	}
}

func TestRScaleRANSACWithInliersExcludesOutlier(t *testing.T) {
	pairs := []MatchedPair{{0, 0, 4, -2, 0}, {20, 0, 24, -2, 0}, {0, 20, 4, 18, 0}, {20, 20, 24, 18, 0}, {10, 10, 80, 80, 0}}
	transform, inliers, err := SolveRScaleTransformationRANSACWithInliers(pairs, 500, 1.5)
	if err != nil {
		t.Fatal(err)
	}
	if len(inliers) != 4 {
		t.Fatalf("inliers=%d, want 4", len(inliers))
	}
	if math.Abs(transform.C-4) > 0.1 || math.Abs(transform.F+2) > 0.1 {
		t.Fatalf("transform=%+v", transform)
	}
}

// TestFitCatalogResidualRecoversRScale verifies the catalog-only residual fit
// (used by the mosaic chain fallback) recovers a small rotation+scale+shift, not
// just a translation — the correctness upgrade over the old translation-only
// chain. The returned transform must map the source catalog onto the target.
func TestFitCatalogResidualRecoversRScale(t *testing.T) {
	// A spatially distributed source catalog so rotation/scale are well constrained.
	var src []Star
	for gx := 0; gx < 5; gx++ {
		for gy := 0; gy < 5; gy++ {
			src = append(src, Star{X: 40 + float64(gx)*120, Y: 40 + float64(gy)*120, Flux: 1000})
		}
	}
	// True residual: 1.5° rotation, 1.01 scale, (3,-2) shift about the centre.
	const cx, cy = 320.0, 320.0
	angle := 1.5 * math.Pi / 180
	scale := 1.01
	sinA, cosA := math.Sin(angle)*scale, math.Cos(angle)*scale
	target := make([]Star, len(src))
	for i, s := range src {
		dx, dy := s.X-cx, s.Y-cy
		target[i] = Star{
			X:    cx + (dx*cosA - dy*sinA) + 3,
			Y:    cy + (dx*sinA + dy*cosA) - 2,
			Flux: s.Flux,
		}
	}

	tr, stats, err := FitCatalogResidual(src, target, 640, 640, 30, "rscale")
	if err != nil {
		t.Fatalf("FitCatalogResidual returned error: %v", err)
	}
	if stats.MatchedStars < 10 {
		t.Fatalf("expected most stars matched, got %d", stats.MatchedStars)
	}
	for i, s := range src {
		x, y := ApplyAffineTransform(tr, s.X, s.Y)
		if math.Hypot(x-target[i].X, y-target[i].Y) > 0.5 {
			t.Fatalf("star %d mapped to (%.2f,%.2f), want (%.2f,%.2f)", i, x, y, target[i].X, target[i].Y)
		}
	}
	// The fit must capture rotation/scale, not collapse to a pure translation.
	if math.Abs(tr.B) < 1e-4 && math.Abs(tr.D) < 1e-4 {
		t.Fatalf("fit has no rotation component: %+v", tr)
	}
}

// TestRefineCentroidSubPixel verifies the iterative centroid refinement recovers
// a sub-pixel star centre from an integer-rounded starting guess.
func TestRefineCentroidSubPixel(t *testing.T) {
	const w, h = 41, 41
	const trueX, trueY = 20.37, 19.62
	const sigma = 1.6
	pix := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx := float64(x) - trueX
			dy := float64(y) - trueY
			pix[y*w+x] = float32(1000 * math.Exp(-(dx*dx+dy*dy)/(2*sigma*sigma)))
		}
	}
	gx, gy := refineCentroid(pix, w, h, 20, 20, 0, 6.0)
	if d := math.Hypot(gx-trueX, gy-trueY); d > 0.05 {
		t.Fatalf("refined centroid (%.3f,%.3f) is %.3f px off true (%.3f,%.3f)", gx, gy, d, trueX, trueY)
	}
}

// TestGlobalBundleAdjustReducesResidual verifies the bundle adjustment pulls an
// adjustable frame into agreement with the fixed frames it overlaps, and leaves
// fixed frames untouched.
func TestGlobalBundleAdjustReducesResidual(t *testing.T) {
	var grid []Star
	for gx := 0; gx < 5; gx++ {
		for gy := 0; gy < 5; gy++ {
			grid = append(grid, Star{X: 60 + float64(gx)*100, Y: 60 + float64(gy)*100})
		}
	}
	cats := make([][]Star, 3)
	cats[0] = append([]Star(nil), grid...) // reference, fixed
	cats[1] = append([]Star(nil), grid...) // another fixed frame
	// Frame 2 carries a small consistent error (<bundleMatchRadiusPx so the
	// correspondences still form): 1.5 px in x, -1.0 px in y.
	bad := AffineTransform{A: 1, C: 1.5, E: 1, F: -1.0}
	cats[2] = make([]Star, len(grid))
	for i, s := range grid {
		x, y := ApplyAffineTransform(bad, s.X, s.Y)
		cats[2][i] = Star{X: x, Y: y}
	}

	fixed := []bool{true, true, false}
	updates, ok := GlobalBundleAdjust(cats, fixed, func(a, b int) bool { return true }, "general", 600, 600, 5)
	if !ok {
		t.Fatalf("bundle adjustment did not improve the global residual")
	}
	if updates[0] != IdentityTransform() || updates[1] != IdentityTransform() {
		t.Fatalf("fixed frames must not move: %+v %+v", updates[0], updates[1])
	}
	for i, s := range cats[2] {
		x, y := ApplyAffineTransform(updates[2], s.X, s.Y)
		if d := math.Hypot(x-grid[i].X, y-grid[i].Y); d > 0.1 {
			t.Fatalf("frame 2 star %d still %.3f px off after adjustment", i, d)
		}
	}
}

func TestGlobalBundleAdjustShortFixedReturnsNoUpdates(t *testing.T) {
	updates, ok := GlobalBundleAdjust([][]Star{{{X: 1, Y: 1}}, {{X: 1, Y: 1}}}, []bool{false}, func(a, b int) bool { return true }, "general", 10, 10, 1)
	if ok || len(updates) != 2 {
		t.Fatalf("got updates=%d ok=%v", len(updates), ok)
	}
	for i, u := range updates {
		if u != IdentityTransform() {
			t.Fatalf("update %d = %+v, want identity", i, u)
		}
	}
}

func TestCatalogMatchingExcludesNonFiniteStars(t *testing.T) {
	bad := Star{X: math.NaN(), Y: 1, Flux: 1}
	if got := matchIndicesByProximity([]Star{bad}, []Star{{X: 1, Y: 1, Flux: 1}}, 3); len(got) != 0 {
		t.Fatal("non-finite catalog coordinate matched")
	}
	if got := matchStarsByMutualProximity([]Star{{X: 1, Y: 1, Flux: math.Inf(1)}}, []Star{{X: 1, Y: 1, Flux: 1}}, 0, 3, .85); len(got) != 0 {
		t.Fatal("non-finite catalog flux matched")
	}
}

func TestTransformGlobalSupport(t *testing.T) {
	stars := []Star{
		{X: 10, Y: 12}, {X: 120, Y: 30}, {X: 200, Y: 80}, {X: 60, Y: 150},
		{X: 300, Y: 220}, {X: 45, Y: 260}, {X: 180, Y: 300}, {X: 330, Y: 60},
		{X: 90, Y: 90}, {X: 250, Y: 140},
	}
	identity := AffineTransform{A: 1, B: 0, C: 0, D: 0, E: 1, F: 0}
	if got := transformGlobalSupport(stars, stars, identity, 2.0); got != len(stars) {
		t.Fatalf("identity support = %d, want %d", got, len(stars))
	}

	// A transform offset far beyond the tolerance aligns nothing — this is the
	// false-consensus case the verification gate must catch.
	shifted := AffineTransform{A: 1, B: 0, C: 500, D: 0, E: 1, F: 500}
	if got := transformGlobalSupport(stars, stars, shifted, 2.0); got != 0 {
		t.Fatalf("far-shift support = %d, want 0", got)
	}

	// A sub-tolerance jitter still counts as aligned.
	jitter := AffineTransform{A: 1, B: 0, C: 1.0, D: 0, E: 1, F: -1.0}
	if got := transformGlobalSupport(stars, stars, jitter, 2.0); got != len(stars) {
		t.Fatalf("jitter support = %d, want %d", got, len(stars))
	}
}

// TestEstimateTranslationFromCatalogs verifies the catalog-based chain-fallback
// estimator recovers a known offset from pre-extracted catalogs (no image warp).
func TestEstimateTranslationFromCatalogs(t *testing.T) {
	hdr := fitsio.Header{Cards: map[string]string{
		"CRPIX1": "100", "CRPIX2": "100",
		"CRVAL1": "100", "CRVAL2": "22",
		"CD1_1": "0.0001", "CD1_2": "0", "CD2_1": "0", "CD2_2": "0.0001",
	}}
	src := []Star{
		{X: 20, Y: 25, Flux: 100}, {X: 140, Y: 40, Flux: 90}, {X: 230, Y: 110, Flux: 80},
		{X: 70, Y: 180, Flux: 70}, {X: 320, Y: 250, Flux: 60}, {X: 100, Y: 300, Flux: 50},
	}
	const dx, dy = 12.0, -7.0
	inter := make([]Star, len(src))
	for i, s := range src {
		inter[i] = Star{X: s.X + dx, Y: s.Y + dy, Flux: s.Flux}
	}
	// Same WCS for both → the mapper projects identically, so the catalog match
	// must recover the pure translation.
	gotDx, gotDy, n, err := EstimateTranslationFromCatalogs(src, hdr, nil, nil, inter, hdr, nil, nil)
	if err != nil {
		t.Fatalf("EstimateTranslationFromCatalogs: %v", err)
	}
	if n < 4 {
		t.Fatalf("matched %d stars, want >= 4", n)
	}
	if math.Abs(gotDx-dx) > 0.01 || math.Abs(gotDy-dy) > 0.01 {
		t.Fatalf("got (%.3f, %.3f), want (%.1f, %.1f)", gotDx, gotDy, dx, dy)
	}
}

// TestDominantOffsetRecoversBulkShift verifies the 2D offset-histogram finds the
// bulk translation even when it exceeds a nearest-neighbour search radius and the
// reference catalog contains non-corresponding (noise) stars.
func TestDominantOffsetRecoversBulkShift(t *testing.T) {
	proj := []Star{
		{X: 20, Y: 25}, {X: 140, Y: 40}, {X: 230, Y: 110}, {X: 70, Y: 180},
		{X: 320, Y: 250}, {X: 100, Y: 300}, {X: 400, Y: 120},
	}
	const dx, dy = 47.0, -33.0 // larger than a typical ~30px proximity radius
	ref := make([]Star, 0, len(proj)+3)
	for _, s := range proj {
		ref = append(ref, Star{X: s.X + dx, Y: s.Y + dy})
	}
	ref = append(ref, Star{X: 600, Y: 700}, Star{X: 33, Y: 900}, Star{X: 780, Y: 60})

	gotDx, gotDy, ok := dominantOffset(proj, ref, histWindowPx, histBinPx)
	if !ok {
		t.Fatal("dominantOffset found no peak")
	}
	if math.Abs(gotDx-dx) > histBinPx || math.Abs(gotDy-dy) > histBinPx {
		t.Fatalf("offset = (%.2f, %.2f), want ~(%.0f, %.0f)", gotDx, gotDy, dx, dy)
	}
}

// TestMatchStarsByOffsetHistogramBeyondProximity verifies the histogram matcher
// recovers correct correspondences for an offset that would defeat plain
// nearest-neighbour matching.
func TestMatchStarsByOffsetHistogramBeyondProximity(t *testing.T) {
	proj := []Star{
		{X: 20, Y: 25}, {X: 140, Y: 40}, {X: 230, Y: 110}, {X: 70, Y: 180},
		{X: 320, Y: 250}, {X: 100, Y: 300}, {X: 400, Y: 120},
	}
	const dx, dy = 47.0, -33.0
	ref := make([]Star, len(proj))
	for i, s := range proj {
		ref[i] = Star{X: s.X + dx, Y: s.Y + dy}
	}
	pairs := matchStarsByOffsetHistogram(proj, ref, histWindowPx, histBinPx, histMatchRadiusPx)
	if len(pairs) < 5 {
		t.Fatalf("got %d pairs, want >= 5", len(pairs))
	}
	for _, p := range pairs {
		// Ref is the original projected position; Target = Ref + (dx,dy).
		if math.Abs((p.RefX+dx)-p.TargetX) > 1e-6 || math.Abs((p.RefY+dy)-p.TargetY) > 1e-6 {
			t.Fatalf("bad correspondence: ref(%.1f,%.1f) target(%.1f,%.1f)", p.RefX, p.RefY, p.TargetX, p.TargetY)
		}
	}
}

// TestEstimateTranslationFromCatalogsRejectsNonOverlap verifies the chain
// fallback rejects frames that share no real geometry (e.g. an ACS chip vs a
// different chip that barely overlaps) instead of returning a garbage offset —
// the bug that produced ~2000 px [sci,2] offsets.
func TestEstimateTranslationFromCatalogsRejectsNonOverlap(t *testing.T) {
	hdr := fitsio.Header{Cards: map[string]string{
		"CRPIX1": "100", "CRPIX2": "100",
		"CRVAL1": "100", "CRVAL2": "22",
		"CD1_1": "0.0001", "CD1_2": "0", "CD2_1": "0", "CD2_2": "0.0001",
	}}
	src := []Star{
		{X: 20, Y: 25}, {X: 140, Y: 40}, {X: 230, Y: 110},
		{X: 70, Y: 180}, {X: 320, Y: 250}, {X: 100, Y: 300},
	}
	// An unrelated catalog with different relative geometry: no consistent
	// transform aligns them, so the result must be an error (not a bogus offset).
	other := []Star{
		{X: 400, Y: 410}, {X: 405, Y: 600}, {X: 800, Y: 405},
		{X: 33, Y: 770}, {X: 610, Y: 90}, {X: 250, Y: 950},
	}
	_, _, _, err := EstimateTranslationFromCatalogs(src, hdr, nil, nil, other, hdr, nil, nil)
	if err == nil {
		t.Fatal("expected non-overlapping catalogs to be rejected, got a translation")
	}
}

// TestMatchStarsMutualBestOneToOne verifies the hardened triangle matcher pairs
// stars one-to-one (no target claimed by multiple refs) and recovers the correct
// correspondences for a translated copy of an asymmetric star field.
func TestMatchStarsMutualBestOneToOne(t *testing.T) {
	ref := []Star{
		{X: 20, Y: 25, Flux: 100}, {X: 140, Y: 40, Flux: 90}, {X: 230, Y: 110, Flux: 80},
		{X: 70, Y: 180, Flux: 70}, {X: 320, Y: 250, Flux: 60}, {X: 100, Y: 300, Flux: 50},
	}
	const dx, dy = 50.0, 30.0
	target := make([]Star, len(ref))
	for i, s := range ref {
		target[i] = Star{X: s.X + dx, Y: s.Y + dy, Flux: s.Flux}
	}

	pairs := MatchStars(ref, target, 50, 0.01)
	if len(pairs) < 4 {
		t.Fatalf("expected at least 4 matched pairs, got %d", len(pairs))
	}

	seenTarget := map[[2]float64]bool{}
	for _, p := range pairs {
		key := [2]float64{p.TargetX, p.TargetY}
		if seenTarget[key] {
			t.Fatalf("target star %v matched more than once (one-to-one violated)", key)
		}
		seenTarget[key] = true
		// Correct correspondence: target should be ref + (dx,dy).
		if math.Abs((p.RefX+dx)-p.TargetX) > 1e-6 || math.Abs((p.RefY+dy)-p.TargetY) > 1e-6 {
			t.Fatalf("wrong correspondence: ref(%.1f,%.1f) -> target(%.1f,%.1f), want +(%.0f,%.0f)",
				p.RefX, p.RefY, p.TargetX, p.TargetY, dx, dy)
		}
	}
}
