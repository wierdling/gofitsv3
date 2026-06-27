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
