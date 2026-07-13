package mosaic

import (
	"math"
	"testing"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/processing"
)

func skyMapTestInput(path string, width, height int) plannedInput {
	return plannedInput{
		input: Input{
			Path: path,
			HDU:  fitsio.HDU{Data: fitsio.ImageData{Width: width, Height: height}},
		},
		sourceToRef: processing.IdentityTransform(),
	}
}

func TestBuildOverlapSampleMapUsesMedianNotMean(t *testing.T) {
	width, height := 64, 32 // two 32x32 output-px cells side by side
	pixels := make([]float32, width*height)
	for i := range pixels {
		pixels[i] = 10
	}
	// A handful of bright "star" pixels in the first cell, placed in the very
	// first scanned row so they are guaranteed to survive the per-cell sample
	// cap regardless of scan order.
	for x := 0; x < 5; x++ {
		pixels[x] = 5000
	}

	p := skyMapTestInput("a_cal.fits", width, height)
	got := buildOverlapSampleMap(p, pixels, SkysubOptions{})

	key := overlapCellKey(15, 15) // interior of the first cell
	v, ok := got[key]
	if !ok {
		t.Fatalf("cell %v missing from overlap map: %v", key, got)
	}
	if math.Abs(v-10) > 1e-6 {
		t.Fatalf("cell median = %v, want 10 (median should ignore 5/~1024 star pixels; a mean would be pulled toward ~%v)",
			v, (5*5000.0+91*10.0)/96)
	}
}

func TestDropOutlierSkyEdgesAndResolveDropsRedundantOutlier(t *testing.T) {
	component := []int{0, 1, 2, 3}
	// Weights mirror how computeMatchedSkyOffsets actually builds them
	// (sqrt of overlap cell count): the bad edge is also the thinnest one, as
	// a genuinely bad overlap realistically would be. With all edges equal
	// weight a single gross outlier can distort the whole solve enough to
	// mask its own residual (a "masking" effect); realistic weighting avoids
	// that because the outlier can't drag well-supported edges around.
	edges := []skyEdge{
		{i: 0, j: 1, delta: 3, weight: 20, cells: 400},
		{i: 1, j: 2, delta: -2, weight: 20, cells: 400},
		{i: 2, j: 3, delta: 3, weight: 20, cells: 400},
		{i: 0, j: 2, delta: 1, weight: 20, cells: 400}, // redundant path 0->2, consistent
		{i: 0, j: 3, delta: 100, weight: 3, cells: 9},  // inconsistent with the above (true offset3-offset0 = 4)
	}
	initial := solveSkyComponent(component, edges)

	kept, resolved := dropOutlierSkyEdgesAndResolve(component, edges, initial)

	for _, e := range kept {
		if e.i == 0 && e.j == 3 {
			t.Fatalf("outlier edge 0-3 (delta=100) was not dropped: kept=%v", kept)
		}
	}
	if len(kept) != 4 {
		t.Fatalf("kept %d edges, want 4 (5 minus the dropped outlier)", len(kept))
	}
	want := map[int]float64{0: 0, 1: 3, 2: 1, 3: 4}
	for idx, w := range want {
		if math.Abs(resolved[idx]-w) > 1e-6 {
			t.Fatalf("resolved[%d] = %v, want %v (all: %v)", idx, resolved[idx], w, resolved)
		}
	}
}

func TestDropOutlierSkyEdgesAndResolveNoOpWithoutRedundancy(t *testing.T) {
	// A pure chain has no redundant edge to compare a "bad" delta against, so
	// the least-squares solve always fits every edge exactly regardless of
	// how large its delta is (each edge is the sole equation for one more
	// unknown). There is nothing to flag as an outlier, and no edge is ever
	// the sole connection for a frame that WOULD be safe to drop, so the
	// function must return the input unchanged.
	component := []int{0, 1, 2}
	edges := []skyEdge{
		{i: 0, j: 1, delta: 3, weight: 1, cells: 100},
		{i: 1, j: 2, delta: 1000, weight: 1, cells: 8},
	}
	initial := solveSkyComponent(component, edges)

	kept, resolved := dropOutlierSkyEdgesAndResolve(component, edges, initial)

	if len(kept) != len(edges) {
		t.Fatalf("kept %d edges, want %d (no redundancy to detect an outlier from)", len(kept), len(edges))
	}
	for idx := range initial {
		if resolved[idx] != initial[idx] {
			t.Fatalf("resolved[%d] = %v, want unchanged %v", idx, resolved[idx], initial[idx])
		}
	}
}

func TestSameSkyComponentDetectsDisconnection(t *testing.T) {
	component := []int{0, 1, 2, 3}
	connected := []skyEdge{{i: 0, j: 1}, {i: 1, j: 2}, {i: 2, j: 3}}
	if !sameSkyComponent(component, connected) {
		t.Fatal("sameSkyComponent = false for a fully connected chain, want true")
	}

	disconnected := []skyEdge{{i: 0, j: 1}, {i: 2, j: 3}} // two disjoint pairs
	if sameSkyComponent(component, disconnected) {
		t.Fatal("sameSkyComponent = true for two disjoint pairs, want false")
	}
}
