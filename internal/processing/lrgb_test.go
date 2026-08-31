package processing

import (
	"context"
	"testing"
)

func TestComposeLRGBDedicatedContribution(t *testing.T) {
	r, g, b := []float32{.2}, []float32{.4}, []float32{.6}
	l := []float32{1}
	out, err := ComposeLRGB(context.Background(), r, g, b, l, 1, 1, LRGBConfig{LuminanceWeight: 1, UseDedicatedLuminance: true})
	if err != nil || len(out) != 3 || out[0] <= r[0] || out[1] <= g[0] || out[2] <= b[0] {
		t.Fatalf("out=%v err=%v", out, err)
	}
}

func TestComposeLRGBMissingDedicatedChannel(t *testing.T) {
	_, err := ComposeLRGB(context.Background(), []float32{1}, []float32{1}, []float32{1}, nil, 1, 1, LRGBConfig{LuminanceWeight: .5, UseDedicatedLuminance: true})
	if err == nil {
		t.Fatal("expected missing L error")
	}
}

func TestComposeLRGBSyntheticWeightsChangeLuminance(t *testing.T) {
	r, g, b := []float32{.9}, []float32{.1}, []float32{.1}
	base, err := ComposeLRGB(context.Background(), r, g, b, nil, 1, 1, LRGBConfig{LuminanceWeight: 1})
	if err != nil {
		t.Fatal(err)
	}
	red, err := ComposeLRGB(context.Background(), r, g, b, nil, 1, 1, LRGBConfig{LuminanceWeight: 1, SyntheticWeights: [3]float64{1, 0, 0}})
	if err != nil {
		t.Fatal(err)
	}
	if red[1] == base[1] || red[2] == base[2] {
		t.Fatalf("selected synthetic source did not affect output: base=%v red=%v", base, red)
	}
}

func TestApplyLRGBToRGBAAppliesChrominanceSmoothing(t *testing.T) {
	buf := []byte{255, 0, 0, 255, 0, 255, 0, 255, 0, 0, 255, 255}
	sharp := ApplyLRGBToRGBA(buf, 3, 1, LRGBConfig{LuminanceWeight: .5, SyntheticWeights: [3]float64{1, 0, 0}})
	smooth := ApplyLRGBToRGBA(buf, 3, 1, LRGBConfig{LuminanceWeight: .5, ChrominanceSmoothing: 1, SyntheticWeights: [3]float64{1, 0, 0}})
	if string(sharp) == string(smooth) {
		t.Fatalf("chrominance smoothing did not change synthetic preview: sharp=%v smooth=%v", sharp, smooth)
	}
}

func TestApplyLRGBToRGBAUsesVerticalSmoothingNeighbors(t *testing.T) {
	buf := []byte{255, 0, 0, 255, 0, 0, 255, 255}
	sharp := ApplyLRGBToRGBA(buf, 1, 2, LRGBConfig{LuminanceWeight: .5, SyntheticWeights: [3]float64{1, 0, 0}})
	smooth := ApplyLRGBToRGBA(buf, 1, 2, LRGBConfig{LuminanceWeight: .5, ChrominanceSmoothing: 1, SyntheticWeights: [3]float64{1, 0, 0}})
	if string(sharp) == string(smooth) {
		t.Fatalf("2D chrominance smoothing ignored vertical neighbors: sharp=%v smooth=%v", sharp, smooth)
	}
}

func TestComposeLRGBSyntheticLuminanceUsesUnblurredRGB(t *testing.T) {
	// The red source alternates while the canonical RGB luminance stays fixed.
	// Smoothing may change chroma ratios, but must not change the synthetic L
	// sampled from the original red plane at either pixel.
	r := []float32{1, 0}
	g := []float32{0, 1}
	b := []float32{0, 0}
	noSmooth, err := ComposeLRGB(context.Background(), r, g, b, nil, 2, 1, LRGBConfig{LuminanceWeight: 1, SyntheticWeights: [3]float64{1, 0, 0}})
	if err != nil {
		t.Fatal(err)
	}
	smooth, err := ComposeLRGB(context.Background(), r, g, b, nil, 2, 1, LRGBConfig{LuminanceWeight: 1, ChrominanceSmoothing: 1, SyntheticWeights: [3]float64{1, 0, 0}})
	if err != nil {
		t.Fatal(err)
	}
	// At full L contribution, the selected synthetic red luminance is carried
	// into the output's red component; smoothing must not swap those samples.
	if noSmooth[0] != 1 || noSmooth[1] != 0 || smooth[0] != 1 || smooth[1] != 0 {
		t.Fatalf("synthetic luminance changed with smoothing: noSmooth=%v smooth=%v", noSmooth, smooth)
	}
}
