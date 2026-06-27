package processing

import (
	"math"
	"math/rand"
	"testing"
)

func TestAlignChannelResizesTargetToReferenceDimensions(t *testing.T) {
	refW, refH := 80, 80
	targetW, targetH := 40, 40

	ref := makeSyntheticStarField(refW, refH, [][2]int{{12, 14}, {52, 16}, {24, 34}, {63, 41}, {18, 60}, {57, 66}})
	target := ResizeChannel(ref, refW, refH, targetW, targetH)

	aligned, transform, err := AlignChannel(target, targetW, targetH, ref, refW, refH)
	if err != nil {
		t.Fatalf("AlignChannel returned error: %v", err)
	}

	if got := len(aligned); got != refW*refH {
		t.Fatalf("aligned length = %d, want %d", got, refW*refH)
	}

	if math.Abs(transform.A-1) > 0.2 || math.Abs(transform.E-1) > 0.2 {
		t.Fatalf("expected near-identity scale after resize, got %+v", transform)
	}
}

func TestAlignChannelByStarsRemovesKnownShift(t *testing.T) {
	w, h := 80, 80
	centers := [][2]int{{12, 14}, {52, 16}, {24, 34}, {63, 41}, {18, 60}, {57, 66}, {40, 40}, {30, 50}}
	ref := makeSyntheticStarField(w, h, centers)

	// target = ref content moved by (+10, -7): a source at ref (cx,cy) sits at
	// (cx+10, cy-7) in target. A correct alignment must undo that shift.
	shifted := make([][2]int, len(centers))
	for i, c := range centers {
		shifted[i] = [2]int{c[0] + 10, c[1] - 7}
	}
	target := makeSyntheticStarField(w, h, shifted)

	aligned, _, stats, err := AlignChannelByStars(target, w, h, ref, w, h, 30.0, "rscale")
	if err != nil {
		t.Fatalf("AlignChannelByStars error: %v", err)
	}
	t.Logf("matched=%d inliers=%d rms=%.2f", stats.MatchedStars, stats.GlobalInliers, stats.RMS)

	// After alignment each ORIGINAL ref star position should be bright again.
	for _, c := range centers {
		if c[0] < 3 || c[0] >= w-3 || c[1] < 3 || c[1] >= h-3 {
			continue
		}
		if v := aligned[c[1]*w+c[0]]; v < 100 {
			t.Errorf("aligned star at ref (%d,%d) = %.1f, want bright (>100) — shift not removed", c[0], c[1], v)
		}
	}
}

func TestAlignChannelByStarsCorrectsRotationAcrossFrame(t *testing.T) {
	w, h := 256, 256

	// Stars spread across the whole frame, including near the edges/corners, with
	// deterministic jitter so the field is NOT a periodic lattice (a regular grid
	// admits alignments shifted by whole cells and is not representative of a real
	// star field).
	rng := rand.New(rand.NewSource(1))
	var centers [][2]int
	for gy := 0; gy < 8; gy++ {
		for gx := 0; gx < 8; gx++ {
			jx := rng.Intn(17) - 8
			jy := rng.Intn(17) - 8
			centers = append(centers, [2]int{20 + gx*30 + jx, 20 + gy*30 + jy})
		}
	}
	ref := makeSyntheticStarField(w, h, centers)

	// Target = ref content moved by a known rotation (~3°) + slight scale +
	// translation about the centre. A translation-only fit would align the middle
	// but drift toward the corners; the alignment must recover the full transform.
	cx, cy := float64(w)/2, float64(h)/2
	ang := 3.0 * math.Pi / 180
	scale := 1.012
	cosA, sinA := math.Cos(ang)*scale, math.Sin(ang)*scale
	tx, ty := 6.0, -4.0
	var tcenters [][2]int
	var refKept [][2]int
	for _, c := range centers {
		x := float64(c[0]) - cx
		y := float64(c[1]) - cy
		nx := cosA*x - sinA*y + cx + tx
		ny := sinA*x + cosA*y + cy + ty
		ix, iy := int(math.Round(nx)), int(math.Round(ny))
		if ix < 5 || ix >= w-5 || iy < 5 || iy >= h-5 {
			continue
		}
		tcenters = append(tcenters, [2]int{ix, iy})
		refKept = append(refKept, c)
	}
	target := makeSyntheticStarField(w, h, tcenters)

	aligned, tr, stats, err := AlignChannelByStars(target, w, h, ref, w, h, 30.0, "general")
	if err != nil {
		t.Fatalf("AlignChannelByStars error: %v", err)
	}
	t.Logf("matched=%d inliers=%d rms=%.2f max=%.2f", stats.MatchedStars, stats.GlobalInliers, stats.RMS, stats.MaxError)
	t.Logf("transform A=%.4f B=%.4f C=%.2f D=%.4f E=%.4f F=%.2f", tr.A, tr.B, tr.C, tr.D, tr.E, tr.F)
	// tr maps target → ref; residual at each kept star vs its ref position.
	worst := 0.0
	for i, tc := range tcenters {
		px := tr.A*float64(tc[0]) + tr.B*float64(tc[1]) + tr.C
		py := tr.D*float64(tc[0]) + tr.E*float64(tc[1]) + tr.F
		d := math.Hypot(px-float64(refKept[i][0]), py-float64(refKept[i][1]))
		if d > worst {
			worst = d
		}
	}
	t.Logf("worst target→ref residual over all %d kept stars: %.2f px", len(tcenters), worst)

	// After alignment every original ref star position — including the corners —
	// should be bright again. Edge drift would leave the corner positions dark.
	misses := 0
	for _, c := range refKept {
		if c[0] < 6 || c[0] >= w-6 || c[1] < 6 || c[1] >= h-6 {
			continue
		}
		if aligned[c[1]*w+c[0]] < 80 {
			misses++
		}
	}
	if misses > 2 {
		t.Errorf("%d/%d ref star positions dark after alignment — rotation/edge drift not corrected", misses, len(refKept))
	}
}

func makeSyntheticStarField(width, height int, centers [][2]int) []float32 {
	pixels := make([]float32, width*height)
	for _, center := range centers {
		cx, cy := center[0], center[1]
		for dy := -2; dy <= 2; dy++ {
			for dx := -2; dx <= 2; dx++ {
				x := cx + dx
				y := cy + dy
				if x < 0 || x >= width || y < 0 || y >= height {
					continue
				}
				dist2 := dx*dx + dy*dy
				pixels[y*width+x] = float32(200 - 20*dist2)
			}
		}
	}
	return pixels
}
