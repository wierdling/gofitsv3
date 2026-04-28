package processing

import "testing"

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
