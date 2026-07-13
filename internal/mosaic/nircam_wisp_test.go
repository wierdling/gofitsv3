package mosaic

import (
	"math"
	"path/filepath"
	"testing"

	"gofitsv3/internal/fitsio"
)

func TestApplyNIRCamWispCorrectionRecoversAutoScaleWithBrightSource(t *testing.T) {
	width, height := 24, 24
	template := syntheticWispTemplate(width, height)
	scale := 2.5
	base := make([]float32, width*height)
	sci := make([]float32, width*height)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			idx := y*width + x
			b := 100 + 0.02*float64(x) + 0.03*float64(y)
			base[idx] = float32(b)
			sci[idx] = float32(b + scale*float64(template[idx]))
		}
	}
	for y := 10; y <= 12; y++ {
		for x := 10; x <= 12; x++ {
			sci[y*width+x] += 5000
			base[y*width+x] += 5000
		}
	}
	sci[3] = float32(math.NaN())

	dir := t.TempDir()
	writeWispTemplate(t, dir, "nrcb4", "f200w", width, height, template)
	p := wispPlannedInput(width, height, sci, "NRCB4", "F200W", false)
	if err := applyNIRCamWispCorrection(p, sci, SkysubOptions{
		NIRCamWisp:            true,
		NIRCamWispTemplateDir: dir,
		NIRCamWispAutoScale:   true,
	}); err != nil {
		t.Fatalf("applyNIRCamWispCorrection error = %v", err)
	}
	if !math.IsNaN(float64(sci[3])) {
		t.Fatalf("NaN pixel changed to %v", sci[3])
	}

	var sumAbs float64
	count := 0
	for i := range sci {
		if i == 3 || (i/width >= 10 && i/width <= 12 && i%width >= 10 && i%width <= 12) {
			continue
		}
		sumAbs += math.Abs(float64(sci[i] - base[i]))
		count++
	}
	if got := sumAbs / float64(count); got > 0.08 {
		t.Fatalf("mean residual after wisp correction = %.4f, want <= 0.08", got)
	}
}

func TestPrepareFramePixelsAppliesFixedWispScaleAndDoesNotMutateInput(t *testing.T) {
	width, height := 8, 8
	template := syntheticWispTemplate(width, height)
	original := filledPixels(width, height, 10)
	sci := append([]float32(nil), original...)
	for i := range sci {
		sci[i] += 3 * template[i]
	}
	dir := t.TempDir()
	writeWispTemplate(t, dir, "nrca3", "f162m", width, height, template)
	p := plannedInput{input: Input{
		PrimaryHeader: fitsio.Header{Cards: map[string]string{
			"INSTRUME": "'NIRCAM'",
			"DETECTOR": "'NRCA3'",
			"FILTER":   "'F150W2'",
			"PUPIL":    "'F162M'",
		}},
		HDU: fitsio.HDU{Data: fitsio.ImageData{Width: width, Height: height, Pixels: sci}},
	}}

	got, _, _, err := prepareFramePixels(p, Options{Skysub: SkysubOptions{
		NIRCamWisp:            true,
		NIRCamWispTemplateDir: dir,
		NIRCamWispScale:       3,
	}}, 0, skyPlane{})
	if err != nil {
		t.Fatalf("prepareFramePixels error = %v", err)
	}
	for i := range got {
		if math.Abs(float64(got[i]-original[i])) > 1e-5 {
			t.Fatalf("corrected pixel[%d] = %v, want %v", i, got[i], original[i])
		}
	}
	if p.input.HDU.Data.Pixels[0] != sci[0] {
		t.Fatal("prepareFramePixels mutated borrowed input pixels")
	}
}

func TestApplyNIRCamWispCorrectionNoOps(t *testing.T) {
	width, height := 8, 8
	template := syntheticWispTemplate(width, height)
	dir := t.TempDir()
	writeWispTemplate(t, dir, "nrcb4", "f200w", width, height, template)

	tests := []struct {
		name string
		p    plannedInput
		opts SkysubOptions
	}{
		{
			name: "disabled",
			p:    wispPlannedInput(width, height, filledPixels(width, height, 1), "NRCB4", "F200W", false),
			opts: SkysubOptions{NIRCamWispTemplateDir: dir, NIRCamWispAutoScale: true},
		},
		{
			name: "non-NIRCam",
			p: plannedInput{input: Input{
				PrimaryHeader: fitsio.Header{Cards: map[string]string{"INSTRUME": "'WFC3'", "DETECTOR": "'IR'", "FILTER": "'F200W'"}},
				HDU:           fitsio.HDU{Data: fitsio.ImageData{Width: width, Height: height, Pixels: filledPixels(width, height, 1)}},
			}},
			opts: SkysubOptions{NIRCamWisp: true, NIRCamWispTemplateDir: dir, NIRCamWispAutoScale: true},
		},
		{
			name: "unsupported detector",
			p:    wispPlannedInput(width, height, filledPixels(width, height, 1), "NRCA1", "F200W", false),
			opts: SkysubOptions{NIRCamWisp: true, NIRCamWispTemplateDir: dir, NIRCamWispAutoScale: true},
		},
		{
			name: "reference only",
			p:    wispPlannedInput(width, height, filledPixels(width, height, 1), "NRCB4", "F200W", true),
			opts: SkysubOptions{NIRCamWisp: true, NIRCamWispTemplateDir: dir, NIRCamWispAutoScale: true},
		},
		{
			name: "absent template",
			p:    wispPlannedInput(width, height, filledPixels(width, height, 1), "NRCB4", "F356W", false),
			opts: SkysubOptions{NIRCamWisp: true, NIRCamWispTemplateDir: dir, NIRCamWispAutoScale: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := append([]float32(nil), tt.p.input.HDU.Data.Pixels...)
			if err := applyNIRCamWispCorrection(tt.p, tt.p.input.HDU.Data.Pixels, tt.opts); err != nil {
				t.Fatalf("applyNIRCamWispCorrection error = %v", err)
			}
			for i := range before {
				if tt.p.input.HDU.Data.Pixels[i] != before[i] {
					t.Fatalf("pixel[%d] changed from %v to %v", i, before[i], tt.p.input.HDU.Data.Pixels[i])
				}
			}
		})
	}
}

func TestApplyNIRCamWispCorrectionTemplateDimensionMismatchErrors(t *testing.T) {
	width, height := 8, 8
	dir := t.TempDir()
	writeWispTemplate(t, dir, "nrcb4", "f200w", width+1, height, syntheticWispTemplate(width+1, height))
	p := wispPlannedInput(width, height, filledPixels(width, height, 1), "NRCB4", "F200W", false)
	err := applyNIRCamWispCorrection(p, p.input.HDU.Data.Pixels, SkysubOptions{
		NIRCamWisp:            true,
		NIRCamWispTemplateDir: dir,
		NIRCamWispAutoScale:   true,
	})
	if err == nil {
		t.Fatal("applyNIRCamWispCorrection error = nil, want dimension mismatch")
	}
}

func wispPlannedInput(width, height int, pixels []float32, detector, filter string, referenceOnly bool) plannedInput {
	return plannedInput{input: Input{
		PrimaryHeader: fitsio.Header{Cards: map[string]string{
			"INSTRUME": "'NIRCAM'",
			"DETECTOR": "'" + detector + "'",
			"FILTER":   "'" + filter + "'",
		}},
		HDU:           fitsio.HDU{Data: fitsio.ImageData{Width: width, Height: height, Pixels: pixels}},
		ReferenceOnly: referenceOnly,
	}}
}

func syntheticWispTemplate(width, height int) []float32 {
	out := make([]float32, width*height)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			out[y*width+x] = float32(math.Sin(float64(x)*0.7) + 0.5*math.Cos(float64(y)*0.4) + 0.02*float64(x-y))
		}
	}
	return out
}

func writeWispTemplate(t *testing.T, dir, detector, filter string, width, height int, pixels []float32) {
	t.Helper()
	path := filepath.Join(dir, "nircam_wisp_"+detector+"_"+filter+".fits")
	if err := fitsio.WriteFloat32Image(path, fitsio.Header{Cards: map[string]string{}}, fitsio.ImageData{Width: width, Height: height, Pixels: pixels}); err != nil {
		t.Fatalf("WriteFloat32Image error = %v", err)
	}
}
