package processing

import (
	"math"
	"testing"
)

func TestBuildCRMasksFromModelRecoversAdjacentHalo(t *testing.T) {
	width, height := 7, 7
	pixels := make([]float32, width*height)
	model := make([]float32, width*height)

	pixels[3*width+3] = 10
	pixels[2*width+3] = 1.5
	pixels[4*width+3] = 1.5
	pixels[3*width+2] = 1.5
	pixels[3*width+4] = 1.5
	pixels[1*width+1] = 1.5

	frames := []FrameInfo{{
		Pixels:      pixels,
		Width:       width,
		Height:      height,
		SourceToRef: IdentityTransform(),
		RefToSource: IdentityTransform(),
		Sigma:       1,
	}}

	masks := BuildCRMasksFromModel(frames, model, width, height, 0, 0, 1, DrizzleStyleCROptions{
		SeedSNR:    4,
		DerivScale: 0,
	})
	mask := masks[0]

	for _, idx := range []int{
		3*width + 3,
		2*width + 3,
		4*width + 3,
		3*width + 2,
		3*width + 4,
	} {
		if !mask[idx] {
			t.Fatalf("expected halo pixel %d to be flagged", idx)
		}
	}
	if mask[1*width+1] {
		t.Fatalf("isolated bright pixel should not be pulled into the CR mask")
	}
}

func TestBuildCRMasksFromModelDoesNotOvergrowWeakNeighbor(t *testing.T) {
	width, height := 7, 7
	pixels := make([]float32, width*height)
	model := make([]float32, width*height)

	pixels[3*width+3] = 10
	pixels[3*width+4] = 0.4

	frames := []FrameInfo{{
		Pixels:      pixels,
		Width:       width,
		Height:      height,
		SourceToRef: IdentityTransform(),
		RefToSource: IdentityTransform(),
		Sigma:       1,
	}}

	masks := BuildCRMasksFromModel(frames, model, width, height, 0, 0, 1, DrizzleStyleCROptions{
		SeedSNR:    4,
		DerivScale: 0,
	})
	mask := masks[0]

	if !mask[3*width+3] {
		t.Fatalf("expected seed pixel to be flagged")
	}
	if mask[3*width+4] {
		t.Fatalf("weak neighbor should not be pulled into the CR mask")
	}
}

func TestBuildCRMasksFromModelNoisePlaneRaisesThreshold(t *testing.T) {
	width, height := 7, 7
	model := make([]float32, width*height)
	model[3*width+3] = 10

	pixels := make([]float32, width*height)
	pixels[3*width+3] = 15 // excess of 5 over the model

	base := FrameInfo{
		Pixels:      pixels,
		Width:       width,
		Height:      height,
		SourceToRef: IdentityTransform(),
		RefToSource: IdentityTransform(),
		Sigma:       1, // 5 > SeedSNR*1 => would flag without a noise plane
	}
	opts := DrizzleStyleCROptions{SeedSNR: 4, DerivScale: 0}

	// Without a noise plane the flat sigma flags the borderline excess.
	flat := base
	if m := BuildCRMasksFromModel([]FrameInfo{flat}, model, width, height, 0, 0, 1, opts); !m[0][3*width+3] {
		t.Fatal("expected flat-sigma detection to flag the borderline excess")
	}

	// A larger local noise (e.g. a bright region's ERR) raises the floor above
	// the excess, so the same pixel is no longer flagged.
	noise := make([]float32, width*height)
	for i := range noise {
		noise[i] = 2 // floor = SeedSNR*2 = 8 > excess of 5
	}
	withNoise := base
	withNoise.Noise = noise
	if m := BuildCRMasksFromModel([]FrameInfo{withNoise}, model, width, height, 0, 0, 1, opts); m[0][3*width+3] {
		t.Fatal("per-pixel noise plane should suppress the borderline excess")
	}
}

func TestBuildCRMasksFromModelTwoSidedResiduals(t *testing.T) {
	width, height := 7, 7
	center := 3*width + 3
	model := make([]float32, width*height)
	for i := range model {
		model[i] = 10
	}

	tests := []struct {
		name  string
		value float32
	}{
		{name: "positive", value: 15},
		{name: "negative", value: 5},
		{name: "non-positive science", value: -1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pixels := append([]float32(nil), model...)
			pixels[center] = tc.value
			frame := FrameInfo{
				Pixels:      pixels,
				Width:       width,
				Height:      height,
				SourceToRef: IdentityTransform(),
				RefToSource: IdentityTransform(),
				Sigma:       1,
			}
			opts := DrizzleStyleCROptions{SeedSNR: 4, TwoSided: true}
			if masks := BuildCRMasksFromModel([]FrameInfo{frame}, model, width, height, 0, 0, 1, opts); !masks[0][center] {
				t.Fatalf("two-sided detector did not flag value %v against model 10", tc.value)
			}
		})
	}
}

func TestBuildCRMasksFromModelLegacyModeIgnoresNegativeResidual(t *testing.T) {
	width, height := 7, 7
	center := 3*width + 3
	model := make([]float32, width*height)
	pixels := make([]float32, width*height)
	for i := range model {
		model[i] = 10
		pixels[i] = 10
	}
	pixels[center] = 5
	frame := FrameInfo{
		Pixels:      pixels,
		Width:       width,
		Height:      height,
		SourceToRef: IdentityTransform(),
		RefToSource: IdentityTransform(),
		Sigma:       1,
	}
	opts := DrizzleStyleCROptions{SeedSNR: 4}
	if masks := BuildCRMasksFromModel([]FrameInfo{frame}, model, width, height, 0, 0, 1, opts); masks[0][center] {
		t.Fatal("legacy positive-only detector flagged a negative residual")
	}
}

func TestBuildCRMasksFromModelTwoSidedGrowsNegativeHalo(t *testing.T) {
	width, height := 7, 7
	center := 3*width + 3
	neighbor := 3*width + 4
	model := make([]float32, width*height)
	pixels := make([]float32, width*height)
	for i := range model {
		model[i] = 10
		pixels[i] = 10
	}
	pixels[center] = 4
	pixels[neighbor] = 8.5
	frame := FrameInfo{
		Pixels:      pixels,
		Width:       width,
		Height:      height,
		SourceToRef: IdentityTransform(),
		RefToSource: IdentityTransform(),
		Sigma:       1,
	}
	opts := DrizzleStyleCROptions{SeedSNR: 4, TwoSided: true}
	masks := BuildCRMasksFromModel([]FrameInfo{frame}, model, width, height, 0, 0, 1, opts)
	if !masks[0][center] || !masks[0][neighbor] {
		t.Fatal("two-sided detector did not grow across a negative residual halo")
	}
}

func TestBilinearSampleRenormalizesOverFiniteCorners(t *testing.T) {
	width, height := 2, 2
	nan := float32(math.NaN())
	pixels := []float32{1, 2, 3, nan} // corner (1,1) is NaN (coverage edge)

	// Center sample: equal weights, NaN corner dropped => mean of {1,2,3}.
	if got := bilinearSample(pixels, width, height, 0.5, 0.5); math.Abs(got-2.0) > 1e-9 {
		t.Fatalf("center sample = %v, want 2.0 (renormalized over finite corners)", got)
	}
	// All-finite stencil still interpolates exactly at a corner.
	if got := bilinearSample(pixels, width, height, 0, 0); got != 1 {
		t.Fatalf("corner (0,0) sample = %v, want 1", got)
	}
	// Fully out of bounds stays NaN.
	if got := bilinearSample(pixels, width, height, -1, 0); !math.IsNaN(got) {
		t.Fatalf("out-of-bounds sample = %v, want NaN", got)
	}
}

func TestRemoveCosmicRaysHandlesShorterMasterMask(t *testing.T) {
	width, height := 4, 4
	pixels := make([]float32, width*height)
	masterMask := make([]bool, 10)
	pixels[5] = 100

	cleaned := RemoveCosmicRays(pixels, width, height, 1, 1, masterMask)

	if len(cleaned) != len(pixels) {
		t.Fatalf("cleaned length = %d, want %d", len(cleaned), len(pixels))
	}
}
