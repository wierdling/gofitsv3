package mosaic

import (
	"math"
	"testing"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/processing"
)

// planeTestInput builds a plannedInput with an identity source-to-reference
// transform, so mapPixel (and therefore chipOutputExtent, used to scale the
// solver's slope regularization) returns coordinates unchanged.
func planeTestInput(path string, width, height int) plannedInput {
	return plannedInput{
		input: Input{
			Path: path,
			HDU:  fitsio.HDU{Data: fitsio.ImageData{Width: width, Height: height}},
		},
		sourceToRef: processing.IdentityTransform(),
	}
}

func TestComputeDifferenceSkyPlanesRecoversInjectedGradientAndPreservesNebula(t *testing.T) {
	planned := []plannedInput{
		planeTestInput("a_cal.fits", 512, 512),
		planeTestInput("b_cal.fits", 512, 512),
		planeTestInput("c_cal.fits", 512, 512),
		planeTestInput("d_cal.fits", 512, 512),
	}

	// A smooth, non-trivial "nebula" that is the SAME physical background at a
	// given (x,y), sampled identically by every frame's overlap cell there.
	nebula := func(x, y float64) float64 { return 50 + 0.02*x + 0.015*y }

	// Per-frame injected systematic (e.g. residual background error unique to
	// that exposure), which the solver must recover and remove. Frame 0 is the
	// graph root (lowest index) and is pinned to the zero plane by the solver,
	// so its injected plane must be zero for a direct comparison.
	inject := []skyPlane{
		{A: 0, B: 0, C: 0, Valid: true},
		{A: 0.010, B: -0.005, C: 3, Valid: true},
		{A: -0.008, B: 0.012, C: -2, Valid: true},
		{A: 0.015, B: 0.020, C: 5, Valid: true},
	}

	// A dense grid of shared overlap cells (all 4 frames "see" the same
	// footprint, like 4 dithers), spread over a wide enough x,y range that A
	// and B are well constrained.
	maps := make([]map[int64]float64, len(planned))
	for i := range maps {
		maps[i] = map[int64]float64{}
	}
	for _, x := range []float64{80, 140, 200, 260, 320, 380} {
		for _, y := range []float64{80, 140, 200, 260, 320, 380} {
			key := overlapCellKey(x, y)
			cx, cy := overlapCellCenter(key)
			for i := range planned {
				maps[i][key] = nebula(cx, cy) + inject[i].value(cx, cy)
			}
		}
	}

	options := SkysubOptions{Clip: 2, LSigma: 4, USigma: 4}
	planes, matched := computeDifferenceSkyPlanes(planned, maps, options)

	for i, ok := range matched {
		if !ok {
			t.Fatalf("matched[%d] = false, want true", i)
		}
	}

	const tol = 0.05
	for i := 1; i < len(planned); i++ {
		got, want := planes[i], inject[i]
		if math.Abs(got.A-want.A) > tol || math.Abs(got.B-want.B) > tol || math.Abs(got.C-want.C) > tol {
			t.Fatalf("planes[%d] = %+v, want ~%+v", i, got, want)
		}
	}
	if planes[0] != (skyPlane{Valid: true}) {
		t.Fatalf("planes[0] (root) = %+v, want the zero plane", planes[0])
	}

	// Nebula preserved: subtracting the solved plane from a raw sample must
	// recover the independently-computed nebula value, not the injected
	// systematic being folded back in.
	x, y := 200.0, 260.0
	raw := nebula(x, y) + inject[2].value(x, y)
	corrected := raw - planes[2].value(x, y)
	if math.Abs(corrected-nebula(x, y)) > tol {
		t.Fatalf("corrected value = %v, want ~nebula(x,y) = %v", corrected, nebula(x, y))
	}
}

func TestComputeDifferenceSkyPlanesChainsTransitively(t *testing.T) {
	planned := []plannedInput{
		planeTestInput("left_cal.fits", 256, 256),
		planeTestInput("middle_cal.fits", 256, 256),
		planeTestInput("right_cal.fits", 256, 256),
	}
	maps := []map[int64]float64{{}, {}, {}}

	// A-B overlap, flat pedestal-only difference (delta = 3).
	for _, x := range []float64{50, 90, 130, 170} {
		for _, y := range []float64{50, 90, 130, 170} {
			key := overlapCellKey(x, y)
			maps[0][key] = 10
			maps[1][key] = 13
		}
	}
	// B-C overlap, at DIFFERENT cells than A-B (no direct A-C overlap at all),
	// flat pedestal-only difference (delta = -2).
	for _, x := range []float64{300, 340, 380, 420} {
		for _, y := range []float64{300, 340, 380, 420} {
			key := overlapCellKey(x, y)
			maps[1][key] = 13
			maps[2][key] = 11
		}
	}

	planes, matched := computeDifferenceSkyPlanes(planned, maps, SkysubOptions{Clip: 2, LSigma: 4, USigma: 4})

	for i, ok := range matched {
		if !ok {
			t.Fatalf("matched[%d] = false, want true (chained through frame 1)", i)
		}
	}
	wantC := []float64{0, 3, 1} // A pinned to 0, B = 0+3, C = B-2 = 1
	for i, want := range wantC {
		if math.Abs(planes[i].C-want) > 1e-6 {
			t.Fatalf("planes[%d].C = %v, want %v", i, planes[i].C, want)
		}
		if math.Abs(planes[i].A) > 1e-6 || math.Abs(planes[i].B) > 1e-6 {
			t.Fatalf("planes[%d] = %+v, want zero slope (flat injected data)", i, planes[i])
		}
	}
}

func TestComputeDifferenceSkyPlanesRejectsEdgeBelowMinCells(t *testing.T) {
	planned := []plannedInput{
		planeTestInput("a_cal.fits", 256, 256),
		planeTestInput("b_cal.fits", 256, 256),
	}
	maps := []map[int64]float64{{}, {}}
	// Only 3 shared cells, below skyMinOverlapCells (8).
	for _, x := range []float64{50, 90, 130} {
		key := overlapCellKey(x, 50)
		maps[0][key] = 10
		maps[1][key] = 13
	}

	planes, matched := computeDifferenceSkyPlanes(planned, maps, SkysubOptions{Clip: 2, LSigma: 4, USigma: 4})

	for i, ok := range matched {
		if ok {
			t.Fatalf("matched[%d] = true, want false (edge has too few cells)", i)
		}
	}
	for i, p := range planes {
		if p.Valid {
			t.Fatalf("planes[%d] = %+v, want invalid (no plane fit)", i, p)
		}
	}
}

func TestComputeDifferenceSkyPlanesSkipsReferenceOnlyInput(t *testing.T) {
	planned := []plannedInput{
		{input: Input{ReferenceOnly: true, Path: "ref.fits"}, sourceToRef: processing.IdentityTransform()},
		planeTestInput("data_cal.fits", 256, 256),
	}
	maps := []map[int64]float64{
		{}, // ReferenceOnly input's map is never populated by planSkysub.
		{},
	}
	for _, x := range []float64{50, 90, 130, 170} {
		key := overlapCellKey(x, 50)
		maps[1][key] = 13
	}

	planes, matched := computeDifferenceSkyPlanes(planned, maps, SkysubOptions{Clip: 2, LSigma: 4, USigma: 4})

	if matched[0] || matched[1] {
		t.Fatalf("matched = %v, want both false (no usable edges)", matched)
	}
	if planes[0].Valid || planes[1].Valid {
		t.Fatalf("planes = %+v, want both invalid", planes)
	}
}
