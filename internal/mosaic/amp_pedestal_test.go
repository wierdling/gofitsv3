package mosaic

import (
	"math"
	"testing"

	"gofitsv3/internal/fitsio"
)

func nircamPrimaryHeader() fitsio.Header {
	return fitsio.Header{Cards: map[string]string{"INSTRUME": "'NIRCAM'", "DETECTOR": "'NRCA1'"}}
}

// ampTestFrame builds a width x height sci array made of a smooth "nebula"
// gradient plus a per-amp pedestal step and a handful of bright point-source
// outliers, matching the shape of a real nebula frame with amp banding.
func ampTestFrame(width, height int, pedestal [ampCount]float64) []float32 {
	ampWidth := width / ampCount
	pixels := make([]float32, width*height)
	for y := 0; y < height; y++ {
		row := y * width
		for x := 0; x < width; x++ {
			amp := x / ampWidth
			if amp >= ampCount {
				amp = ampCount - 1
			}
			base := 1000 + 0.002*float64(x) // smooth gradient, realistic vs. the amp band width
			pixels[row+x] = float32(base + pedestal[amp])
		}
	}
	// A few bright point-source spikes near amp boundaries, which the
	// sigma-clipped band median must reject rather than let bias the step.
	for _, idx := range []int{31, 32*3 + 31, width*10 + 32, width*20 + 96} {
		if idx >= 0 && idx < len(pixels) {
			pixels[idx] = 50000
		}
	}
	return pixels
}

func TestAmpBoundaryStepsRecoversInjectedPedestal(t *testing.T) {
	pedestal := [ampCount]float64{0, 5, -3, 8}
	width, height := 128, 64
	pixels := ampTestFrame(width, height, pedestal)

	steps, ok := ampBoundarySteps(pixels, width, height)
	if !ok {
		t.Fatal("ampBoundarySteps returned ok=false, want true")
	}
	want := [ampCount - 1]float64{
		pedestal[1] - pedestal[0],
		pedestal[2] - pedestal[1],
		pedestal[3] - pedestal[2],
	}
	for i := range want {
		if math.Abs(steps[i]-want[i]) > 0.1 {
			t.Fatalf("steps[%d] = %v, want ~%v", i, steps[i], want[i])
		}
	}
}

func TestAmpBoundaryStepsRejectsNarrowFrame(t *testing.T) {
	// ampWidth = 16 / 4 = 4, not greater than ampBandWidth (16), so there is no
	// room for a boundary band and the measurement must be refused rather than
	// silently degrade.
	pixels := make([]float32, 16*8)
	if _, ok := ampBoundarySteps(pixels, 16, 8); ok {
		t.Fatal("ampBoundarySteps returned ok=true for a frame too narrow for 4 amp strips")
	}
}

func TestAmpOffsetsFromStepsIsZeroMeanAndCumulative(t *testing.T) {
	steps := [ampCount - 1]float64{5, -8, 11} // implies raw offsets [0, 5, -3, 8]
	offsets := ampOffsetsFromSteps(steps)

	var sum float64
	for _, o := range offsets {
		sum += o
	}
	if math.Abs(sum) > 1e-9 {
		t.Fatalf("offsets sum = %v, want 0 (purely relative correction)", sum)
	}
	want := [ampCount]float64{-2.5, 2.5, -5.5, 5.5}
	for i := range want {
		if math.Abs(offsets[i]-want[i]) > 1e-9 {
			t.Fatalf("offsets[%d] = %v, want %v", i, offsets[i], want[i])
		}
	}
}

func TestApplyAmpPedestalCorrectionEqualizesAmpsAcrossGradient(t *testing.T) {
	pedestal := [ampCount]float64{0, 5, -3, 8}
	width, height := 128, 64
	pixels := ampTestFrame(width, height, pedestal)

	p := plannedInput{input: Input{
		PrimaryHeader: nircamPrimaryHeader(),
		HDU:           fitsio.HDU{Data: fitsio.ImageData{Width: width, Height: height}},
	}}
	applyAmpPedestalCorrection(p, pixels)

	// After correction every amp should sit on the same smooth gradient
	// (base + mean(pedestal)), so a straight column comparison across the
	// interior boundaries (avoiding the injected star spikes) must agree
	// with the analytic expectation to a small tolerance.
	var meanPedestal float64
	for _, v := range pedestal {
		meanPedestal += v
	}
	meanPedestal /= float64(len(pedestal))

	checkCols := []int{10, 40, 70, 100}
	for _, x := range checkCols {
		y := 5 // avoid the star-spike rows
		got := float64(pixels[y*width+x])
		want := 1000 + 0.002*float64(x) + meanPedestal
		if math.Abs(got-want) > 0.5 {
			t.Fatalf("corrected pixel at x=%d = %v, want ~%v", x, got, want)
		}
	}
}

func TestApplyAmpPedestalCorrectionSkipsNonNircamInput(t *testing.T) {
	width, height := 128, 64
	pixels := ampTestFrame(width, height, [ampCount]float64{0, 5, -3, 8})
	original := append([]float32(nil), pixels...)

	p := plannedInput{input: Input{
		PrimaryHeader: fitsio.Header{Cards: map[string]string{"INSTRUME": "'WFC3'"}},
		HDU:           fitsio.HDU{Data: fitsio.ImageData{Width: width, Height: height}},
	}}
	applyAmpPedestalCorrection(p, pixels)

	for i := range pixels {
		if pixels[i] != original[i] {
			t.Fatalf("pixel[%d] changed for a non-NIRCam input: got %v, want %v", i, pixels[i], original[i])
		}
	}
}

func TestApplyAmpPedestalCorrectionSkipsReferenceOnlyInput(t *testing.T) {
	width, height := 128, 64
	pixels := ampTestFrame(width, height, [ampCount]float64{0, 5, -3, 8})
	original := append([]float32(nil), pixels...)

	p := plannedInput{input: Input{
		ReferenceOnly: true,
		PrimaryHeader: nircamPrimaryHeader(),
		HDU:           fitsio.HDU{Data: fitsio.ImageData{Width: width, Height: height}},
	}}
	applyAmpPedestalCorrection(p, pixels)

	for i := range pixels {
		if pixels[i] != original[i] {
			t.Fatalf("pixel[%d] changed for a reference-only input: got %v, want %v", i, pixels[i], original[i])
		}
	}
}
