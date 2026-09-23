package processing

import (
	"context"
	"math"
	"reflect"
	"testing"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/stretch"
)

func footprintValidationMeta(w, h int) models.LoadedImage {
	return models.LoadedImage{
		HDU:  fitsio.HDU{Data: fitsio.ImageData{Width: w, Height: h}},
		Mode: stretch.Linear, Background: 0, Peak: 2,
		ScaledPeak: 1, AsinhScale: 1, MTFMidtone: .5,
	}
}

func runFootprintPipeline(t *testing.T, pixels []float32, w, h int, m *StarMap) ([]StarTreatmentFit, []float32, []float32) {
	t.Helper()
	fits, err := FitStarTreatment(context.Background(), m, pixels, w, h, StarTreatmentOptions{})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareStarStretchFits(context.Background(), pixels, w, h, footprintValidationMeta(w, h), fits, m.Sources)
	if err != nil {
		t.Fatal(err)
	}
	mask, err := BuildStarTreatmentMask(context.Background(), prepared, w, h)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := ApplyGentlerStarStretch(context.Background(), pixels, w, h, footprintValidationMeta(w, h), prepared, mask, StarStretchOptions{Strength: 1})
	if err != nil {
		t.Fatal(err)
	}
	return prepared, mask, rendered
}

func TestStarStretchFootprintValidationHandlesFaintSubpixelSource(t *testing.T) {
	const w, h = 61, 61
	pixels := make([]float32, w*h)
	const cx, cy = 30.35, 29.6
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			background := .35 + .003*dx + .002*dy
			noise := .003 * math.Sin(float64(17*x+11*y))
			pixels[y*w+x] = float32(background + noise + .35*math.Exp(-.5*(dx*dx+dy*dy)/(.82*.82)))
		}
	}
	m := &StarMap{Width: w, Height: h, Sources: []StarMapSource{{ID: 11, X: cx, Y: cy, FWHM: 1.9, Radius: 4, Status: "accepted"}}}
	fits, mask, rendered := runFootprintPipeline(t, pixels, w, h, m)
	if len(fits) != 1 || !fits[0].Usable {
		t.Fatalf("faint subpixel source was skipped: %+v", fits)
	}
	if fits[0].OuterRadius <= fits[0].InnerRadius || fits[0].InnerRadius <= 0 {
		t.Fatalf("invalid measured footprint: %+v", fits[0])
	}
	center := int(math.Floor(cy))*w + int(math.Floor(cx))
	normalCenter := DiskStretchPreviewValue(pixels[center], footprintValidationMeta(w, h))
	if !(rendered[center] < normalCenter) {
		t.Fatalf("stellar contribution was not reduced: normal=%v rendered=%v", normalCenter, rendered[center])
	}
	for _, xy := range [][2]int{{2, 2}, {58, 30}, {30, 58}} {
		i := xy[1]*w + xy[0]
		normal := DiskStretchPreviewValue(pixels[i], footprintValidationMeta(w, h))
		if mask[i] != 0 || math.Abs(float64(rendered[i]-normal)) > 1e-7 {
			t.Fatalf("distant background changed at %v: mask=%v normal=%v rendered=%v", xy, mask[i], normal, rendered[i])
		}
	}
}

func TestStarStretchFootprintValidationFeathersBroadHaloWithoutRing(t *testing.T) {
	const w, h = 81, 81
	pixels := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x-40), float64(y-40)
			pixels[y*w+x] = float32(.2 + 5*math.Exp(-.5*(dx*dx+dy*dy)/(3.1*3.1)))
		}
	}
	m := &StarMap{Width: w, Height: h, Sources: []StarMapSource{{ID: 12, X: 40, Y: 40, FWHM: 7.3, Radius: 11, Status: "accepted"}}}
	fits, mask, rendered := runFootprintPipeline(t, pixels, w, h, m)
	if !fits[0].Usable {
		t.Fatalf("broad halo was skipped: %+v", fits[0])
	}
	previous := rendered[40*w+40]
	for r := 1; r < int(math.Floor(fits[0].OuterRadius)); r++ {
		inside := rendered[40*w+40+r]
		if inside > previous+1e-5 {
			t.Fatalf("ring or hollow core at radius %d: %v > %v", r, inside, previous)
		}
		previous = inside
	}
	boundary := 40*w + 40 + int(math.Ceil(fits[0].OuterRadius))
	normal := DiskStretchPreviewValue(pixels[boundary], footprintValidationMeta(w, h))
	if mask[boundary] != 0 || math.Abs(float64(rendered[boundary]-normal)) > 1e-7 {
		t.Fatalf("footprint did not end cleanly: radius=%v mask=%v normal=%v rendered=%v", fits[0].OuterRadius, mask[boundary], normal, rendered[boundary])
	}
	// The catalog radius is 11 pixels, but the measured Gaussian wings extend
	// beyond it. Verify that the added support still carries a measurable,
	// smoothly declining correction rather than being decorative geometry.
	if fits[0].OuterRadius <= 1.25*11 {
		t.Fatalf("footprint did not validate wings beyond catalog radius: %+v", fits[0])
	}
	wing := 40*w + 40 + 12
	wingNormal := DiskStretchPreviewValue(pixels[wing], footprintValidationMeta(w, h))
	if correction := float64(wingNormal - rendered[wing]); correction <= 1e-6 {
		t.Fatalf("validated outer wing carried no measurable correction: %v", correction)
	}
}

func TestStarStretchFootprintValidationSeparatesCurvedNebulaFromStar(t *testing.T) {
	const w, h = 81, 81
	pixels := make([]float32, w*h)
	trueBackground := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x-40), float64(y-40)
			// A broad nebular knot sits inside the footprint, where a plane fit
			// cannot represent its curvature. Its contribution is known, so the
			// treatment must not mistake it for stellar excess.
			nebula := .18 * math.Exp(-.5*((dx-4)*(dx-4)+dy*dy)/(3.8*3.8))
			background := .25 + .0015*dx + .0008*dy + nebula
			trueBackground[y*w+x] = background
			pixels[y*w+x] = float32(background + 4*math.Exp(-.5*(dx*dx+dy*dy)/(1.5*1.5)))
		}
	}
	m := &StarMap{Width: w, Height: h, Sources: []StarMapSource{{ID: 13, X: 40, Y: 40, FWHM: 3.5, Radius: 7, Status: "accepted"}}}
	fits, err := FitStarTreatment(context.Background(), m, pixels, w, h, StarTreatmentOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !fits[0].Usable {
		t.Fatalf("curved-background source was skipped during fitting: %+v", fits[0])
	}
	originalFits := append([]StarTreatmentFit(nil), fits...)
	originalPixels := append([]float32(nil), pixels...)
	meta := footprintValidationMeta(w, h)
	prepared, err := PrepareStarStretchFits(context.Background(), pixels, w, h, meta, fits, m.Sources)
	if err != nil {
		t.Fatal(err)
	}
	if !prepared[0].Usable {
		wantReason := "structured local residual; stellar footprint is ambiguous"
		if prepared[0].Reason != wantReason {
			t.Fatalf("unexpected unsafe-footprint reason: %q", prepared[0].Reason)
		}
		if !reflect.DeepEqual(fits, originalFits) {
			t.Fatalf("unsafe footprint rejection mutated fits: before=%+v after=%+v", originalFits, fits)
		}
		if !reflect.DeepEqual(pixels, originalPixels) {
			t.Fatal("unsafe footprint rejection mutated source pixels")
		}
		return
	}
	mask, err := BuildStarTreatmentMask(context.Background(), prepared, w, h)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := ApplyGentlerStarStretch(context.Background(), pixels, w, h, meta, prepared, mask, StarStretchOptions{Strength: 1})
	if err != nil {
		t.Fatal(err)
	}
	// At radius 5 the true stellar tail is small, while the nebular knot is
	// still substantial. Bound treatment by the oracle stellar contribution;
	// a larger correction is background leakage.
	for _, x := range []int{44, 45, 46} {
		i := 40*w + x
		if mask[i] == 0 {
			continue
		}
		normal := float64(DiskStretchPreviewValue(pixels[i], meta))
		correction := normal - float64(rendered[i])
		oracle := float64(DiskStretchPreviewValue(float32(trueBackground[i]+4*math.Exp(-.5*float64((x-40)*(x-40))/(1.5*1.5))), meta)) - float64(DiskStretchPreviewValue(float32(trueBackground[i]), meta))
		if correction > 1.5*oracle+0.002 {
			t.Fatalf("nebular background leaked into stellar correction at x=%d: correction=%g oracle=%g fit=%+v", x, correction, oracle, prepared[0])
		}
	}
}
