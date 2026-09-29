package processing

import (
	"context"
	"math"
	"testing"

	"gofitsv3/internal/models"
	"gofitsv3/internal/stretch"
)

func syntheticSaturatedWingField(width int, amplitude, sigma float64, asymmetric bool) ([]float32, *StarMap) {
	pixels := make([]float32, width*width)
	c := float64(width-1) / 2
	for y := 0; y < width; y++ {
		for x := 0; x < width; x++ {
			dx, dy := float64(x)-c, float64(y)-c
			r2 := dx*dx + dy*dy
			value := .1 + amplitude*math.Exp(-r2/(2*sigma*sigma))
			if asymmetric && dx > 0 && math.Abs(dy) < 2 {
				value += 2
			}
			if value > 1 {
				value = 1
			}
			pixels[y*width+x] = float32(value)
		}
	}
	return pixels, &StarMap{Width: width, Height: width, Sources: []StarMapSource{{
		ID: 73, X: c, Y: c, FWHM: 2.355 * sigma, Radius: 8, Status: "accepted", Saturated: true,
	}}}
}

func syntheticWidePlateauField(width int, cx, cy, plateau, amplitude, sigma float64) ([]float32, *StarMap) {
	pixels := make([]float32, width*width)
	for y := 0; y < width; y++ {
		for x := 0; x < width; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			r := math.Hypot(dx, dy)
			value := .1 + amplitude*math.Exp(-(dx*dx+dy*dy)/(2*sigma*sigma))
			if r <= plateau {
				value = 1
			}
			pixels[y*width+x] = float32(value)
		}
	}
	return pixels, &StarMap{Width: width, Height: width, Sources: []StarMapSource{{
		ID: int(cx*10 + cy), X: cx, Y: cy, FWHM: 2.355 * sigma, Radius: 8, Status: "accepted", Saturated: true,
	}}}
}

func TestFitStarTreatmentValidatesClippedCoreFromDistributedWings(t *testing.T) {
	pixels, starMap := syntheticSaturatedWingField(81, 5, 2, false)
	fits, err := FitStarTreatment(context.Background(), starMap, pixels, 81, 81, StarTreatmentOptions{})
	if err != nil {
		t.Fatalf("FitStarTreatment failed: %v", err)
	}
	if len(fits) != 1 || !fits[0].Usable {
		t.Fatalf("expected one usable saturated-wing fit, got %#v", fits)
	}
	f := fits[0]
	if !f.Saturated || !f.WingValidated || f.WingModel == "" {
		t.Fatalf("fit did not identify validated saturated wings: %#v", f)
	}
	if f.CoreRadius <= 0 || f.CoreRadius >= f.OuterRadius {
		t.Fatalf("invalid clipped-core radius: core=%v outer=%v", f.CoreRadius, f.OuterRadius)
	}
	if f.SNR <= 3 || f.Residual >= .35 {
		t.Fatalf("weak or poorly fit wings: SNR=%v residual=%v", f.SNR, f.Residual)
	}
}

func TestFitStarTreatmentRejectsOneSidedSaturatedWings(t *testing.T) {
	pixels, starMap := syntheticSaturatedWingField(81, 5, 2, true)
	fits, err := FitStarTreatment(context.Background(), starMap, pixels, 81, 81, StarTreatmentOptions{})
	if err != nil {
		t.Fatalf("FitStarTreatment failed: %v", err)
	}
	if len(fits) != 1 || fits[0].Usable {
		t.Fatalf("one-sided contamination must not produce a usable wing fit: %#v", fits)
	}
	if fits[0].Reason == "" {
		t.Fatal("rejected saturated source should explain why its wings were not trusted")
	}
}

func TestFitStarTreatmentRejectsCoreOnlySaturatedSource(t *testing.T) {
	pixels := make([]float32, 81*81)
	for i := range pixels {
		pixels[i] = .1
	}
	c := 40
	for y := c - 15; y <= c+15; y++ {
		for x := c - 15; x <= c+15; x++ {
			if math.Hypot(float64(x-c), float64(y-c)) <= 15 {
				pixels[y*81+x] = 1
			}
		}
	}
	starMap := &StarMap{Width: 81, Height: 81, Sources: []StarMapSource{{
		ID: 74, X: float64(c), Y: float64(c), FWHM: 4.71, Radius: 8, Status: "accepted", Saturated: true,
	}}}
	fits, err := FitStarTreatment(context.Background(), starMap, pixels, 81, 81, StarTreatmentOptions{})
	if err != nil {
		t.Fatalf("FitStarTreatment failed: %v", err)
	}
	if len(fits) != 1 || fits[0].Usable {
		t.Fatalf("a source with no measurable wings must be rejected: %#v", fits)
	}
}

func TestFitStarTreatmentRejectsSaturatedSourceWithWingsBelowNoise(t *testing.T) {
	const width = 81
	const center = 40
	pixels := make([]float32, width*width)
	for y := 0; y < width; y++ {
		for x := 0; x < width; x++ {
			dx, dy := float64(x-center), float64(y-center)
			noise := .02 * float64((x+3*y)%2*2-1)
			value := .1 + 2*math.Exp(-(dx*dx+dy*dy)/(2*1.25*1.25)) + noise
			if value > 1 {
				value = 1
			}
			pixels[y*width+x] = float32(value)
		}
	}
	starMap := &StarMap{Width: width, Height: width, Sources: []StarMapSource{{
		ID: 75, X: center, Y: center, FWHM: 2.94, Radius: 6, Status: "accepted", Saturated: true,
	}}}
	fits, err := FitStarTreatment(context.Background(), starMap, pixels, width, width, StarTreatmentOptions{})
	if err != nil {
		t.Fatalf("FitStarTreatment failed: %v", err)
	}
	if len(fits) != 1 || fits[0].Usable {
		t.Fatalf("wings below the measured noise floor must be rejected: %#v", fits)
	}
}

func TestFitStarTreatmentSaturatedCoreReachesWidePlateau(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cx, cy  float64
		plateau float64
	}{
		{name: "integer center", cx: 40, cy: 40, plateau: 5},
		{name: "half pixel center", cx: 40.5, cy: 40.5, plateau: 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pixels, starMap := syntheticWidePlateauField(101, tc.cx, tc.cy, tc.plateau, 5, 2)
			fits, err := FitStarTreatment(context.Background(), starMap, pixels, 101, 101, StarTreatmentOptions{})
			if err != nil {
				t.Fatalf("FitStarTreatment failed: %v", err)
			}
			if len(fits) != 1 || !fits[0].Usable {
				t.Fatalf("expected usable saturated-wing fit: %#v", fits)
			}
			if fits[0].CoreRadius < tc.plateau {
				t.Fatalf("core radius %v does not cover clipped plateau radius %v", fits[0].CoreRadius, tc.plateau)
			}
		})
	}
}

func TestFitStarTreatmentRejectsLowAmplitudeWingsDespiteExtrapolatedProfile(t *testing.T) {
	const width = 101
	const center = 50
	pixels := make([]float32, width*width)
	for y := 0; y < width; y++ {
		for x := 0; x < width; x++ {
			dx, dy := float64(x-center), float64(y-center)
			// The star is a valid low-amplitude Gaussian, but the annulus and
			// measured wings carry substantially larger deterministic noise.
			noise := .18 * float64((x+2*y)%2*2-1)
			value := .1 + 1.0*math.Exp(-(dx*dx+dy*dy)/(2*2.0*2.0)) + noise
			if value > 1 {
				value = 1
			}
			pixels[y*width+x] = float32(value)
		}
	}
	starMap := &StarMap{Width: width, Height: width, Sources: []StarMapSource{{
		ID: 76, X: center, Y: center, FWHM: 4.71, Radius: 8, Status: "accepted", Saturated: true,
	}}}
	fits, err := FitStarTreatment(context.Background(), starMap, pixels, width, width, StarTreatmentOptions{})
	if err != nil {
		t.Fatalf("FitStarTreatment failed: %v", err)
	}
	if len(fits) != 1 || fits[0].Usable {
		t.Fatalf("low observed wing SNR must be rejected even when the Gaussian extrapolates inward: %#v", fits)
	}
}

func TestFitStarTreatmentSaturatedWingsResistBrightAnnularOutlier(t *testing.T) {
	pixels, starMap := syntheticSaturatedWingField(101, 5, 2, false)
	// The primary's background annulus is roughly r=22..30. Inject an isolated
	// hot pixel there; robust background fitting must reject it as a contaminant.
	pixels[40*101+65] = 50
	fits, err := FitStarTreatment(context.Background(), starMap, pixels, 101, 101, StarTreatmentOptions{})
	if err != nil {
		t.Fatalf("FitStarTreatment failed: %v", err)
	}
	if len(fits) != 1 || !fits[0].Usable {
		t.Fatalf("bright annular outlier must not invalidate good wings: %#v", fits)
	}
	if math.Abs(fits[0].Background-.1) > .02 {
		t.Fatalf("annular outlier biased fitted background: got %v", fits[0].Background)
	}
}

func TestFitStarTreatmentSaturatedWingsExcludeCataloguedNeighborFromBackground(t *testing.T) {
	pixels, starMap := syntheticSaturatedWingField(101, 5, 2, false)
	starMap.Sources = append(starMap.Sources, StarMapSource{
		ID: 77, X: 74, Y: 50, FWHM: 5, Radius: 6, Status: "accepted",
	})
	for y := 44; y <= 56; y++ {
		for x := 68; x <= 80; x++ {
			dx, dy := float64(x-74), float64(y-50)
			v := .1 + 20*math.Exp(-(dx*dx+dy*dy)/(2*2.5*2.5))
			if v > 1 {
				v = 1
			}
			pixels[y*101+x] = float32(v)
		}
	}
	fits, err := FitStarTreatment(context.Background(), starMap, pixels, 101, 101, StarTreatmentOptions{})
	if err != nil {
		t.Fatalf("FitStarTreatment failed: %v", err)
	}
	if len(fits) != 2 || !fits[0].Usable {
		t.Fatalf("primary saturated source should remain usable with catalogued neighbor excluded: %#v", fits)
	}
	if math.Abs(fits[0].Background-.1) > .02 {
		t.Fatalf("catalogued neighbor biased primary background: got %v", fits[0].Background)
	}
}

func TestBuildStarTreatmentMaskCoversValidatedSaturatedCoreAndFeathers(t *testing.T) {
	f := StarTreatmentFit{
		SourceID: 1, X: 20, Y: 20, Signal: 5, Sigma: 2, OuterRadius: 12,
		CoreRadius: 10, Noise: .01, Saturated: true, WingValidated: true, Usable: true,
	}
	mask, err := BuildStarTreatmentMask(context.Background(), []StarTreatmentFit{f}, 41, 41)
	if err != nil {
		t.Fatalf("BuildStarTreatmentMask failed: %v", err)
	}
	if mask[20*41+20] != 1 {
		t.Fatalf("saturated core must be covered at full strength: %v", mask[20*41+20])
	}
	if mask[20*41+29] != 1 || mask[20*41+30] != 1 {
		t.Fatalf("full mask must cover the complete saturated core through radius 10: r9=%v r10=%v", mask[20*41+29], mask[20*41+30])
	}
	if mask[20*41+32] != 0 {
		t.Fatalf("mask must end at outer radius: %v", mask[20*41+32])
	}
	if mask[20*41+31] <= 0 || mask[20*41+31] >= 1 {
		t.Fatalf("outer wing should have a feathered mask value: %v", mask[20*41+31])
	}
}

func TestApplyGentlerStarStretchLeavesInvalidSamplesInsideValidatedSaturatedCore(t *testing.T) {
	// A NaN inside the footprint keeps its ordinary rendering; the rest of the
	// star is still treated and nothing is reconstructed in its place.
	linear := []float32{float32(math.NaN()), .9, .9}
	fit := StarTreatmentFit{
		SourceID: 9, X: 1, Y: 0, Background: 0, Signal: 1, Sigma: 1,
		OuterRadius: 2, CoreRadius: .5, Noise: .01, Saturated: true,
		WingValidated: true, Usable: true,
	}
	meta := models.LoadedImage{Mode: stretch.Linear, Background: 0, Peak: 1, ScaledPeak: 1}
	out, err := ApplyGentlerStarStretch(context.Background(), linear, 3, 1, meta, []StarTreatmentFit{fit}, []float32{1, 1, 1}, StarStretchOptions{Strength: .5})
	if err != nil {
		t.Fatal(err)
	}
	normal := DiskStretchPreviewValue(linear[0], meta)
	if out[0] != normal {
		t.Fatalf("invalid sample was altered: got %v want %v", out[0], normal)
	}
	if out[1] >= DiskStretchPreviewValue(linear[1], meta) {
		t.Fatalf("valid core sample was not treated: %v", out[1])
	}
}

func TestBuildStarTreatmentMaskRejectsUnvalidatedSaturatedFit(t *testing.T) {
	fit := StarTreatmentFit{SourceID: 10, X: 5, Y: 5, Signal: 1, Sigma: 1, OuterRadius: 4, Saturated: true, Usable: true}
	if _, err := BuildStarTreatmentMask(context.Background(), []StarTreatmentFit{fit}, 11, 11); err == nil {
		t.Fatal("saturated fit without validated wings must not be rendered")
	}
}
