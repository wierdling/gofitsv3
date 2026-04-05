package processing

import (
	"math"
	"testing"
)

func TestAlignChannelResizesTargetToReferenceDimensions(t *testing.T) {
	refW, refH := 80, 80
	targetW, targetH := 40, 40

	ref := makeSyntheticStarField(refW, refH, [][2]int{
		{12, 14},
		{52, 16},
		{24, 34},
		{63, 41},
		{18, 60},
		{57, 66},
	})
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
