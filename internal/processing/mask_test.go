package processing

import "testing"

func TestBuildLayerStarMasksProtectsPerChannelFootprints(t *testing.T) {
	width, height := 5, 5
	channels := [][]float32{
		make([]float32, width*height),
		make([]float32, width*height),
		make([]float32, width*height),
	}
	sigmas := []float64{10, 10, 10}

	channels[0][12] = 25
	channels[0][13] = 18
	channels[0][14] = 18
	channels[1][12] = 24
	channels[1][17] = 18
	channels[1][22] = 18

	masks := BuildLayerStarMasks(channels, width, height, sigmas)
	if !masks[0][12] || !masks[1][12] {
		t.Fatalf("expected shared star seed to be protected in channels 1 and 2")
	}
	if masks[2][12] {
		t.Fatalf("did not expect channel 3 to get a star mask for absent star")
	}
	if !masks[0][14] {
		t.Fatalf("expected channel 1 mask to grow to that channel's local star footprint")
	}
	if masks[1][14] {
		t.Fatalf("did not expect channel 2 mask to copy channel 1 footprint beyond dilation")
	}
	if !masks[1][22] {
		t.Fatalf("expected channel 2 mask to grow to that channel's local star footprint")
	}
	if masks[0][22] {
		t.Fatalf("did not expect channel 1 mask to copy channel 2 footprint beyond dilation")
	}
}

func TestBuildMasterMaskProtectsModerateSharedSignal(t *testing.T) {
	width, height := 4, 4
	channels := [][]float32{
		make([]float32, width*height),
		make([]float32, width*height),
		make([]float32, width*height),
	}
	sigmas := []float64{10, 10, 10}

	channels[0][5] = 25
	channels[1][5] = 22
	channels[2][5] = 0

	mask := BuildMasterMask(channels, width, height, sigmas)
	if !mask[5] {
		t.Fatalf("expected moderate shared signal to be preserved in master mask")
	}
}

func TestBuildMasterMaskHandlesShorterChannelWithoutPanicking(t *testing.T) {
	width, height := 4, 4
	channels := [][]float32{
		make([]float32, width*height),
		make([]float32, width*height),
		make([]float32, 10),
	}
	sigmas := []float64{1, 1, 1}

	channels[0][5] = 20
	channels[1][5] = 21
	channels[2][5] = 22

	mask := BuildMasterMask(channels, width, height, sigmas)

	if len(mask) != len(channels[2]) {
		t.Fatalf("mask length = %d, want %d", len(mask), len(channels[2]))
	}
	if !mask[5] {
		t.Fatalf("expected shared bright pixel to remain masked")
	}
}
