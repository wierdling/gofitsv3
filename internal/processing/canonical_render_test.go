package processing

import (
	"context"
	"math"
	"testing"

	"gofitsv3/internal/models"
	"gofitsv3/internal/stretch"
)

func TestComposeRenderAppliesOffsetBeforeGainAndKeepsInputs(t *testing.T) {
	planes := [3]AlignedPlane{}
	for i := range planes {
		planes[i] = AlignedPlane{Pixels: []float32{2, -1}, Valid: []bool{true, true}, Width: 2, Height: 1}
	}
	state := &models.ColorCalibrationState{Status: models.CalibrationValid, BaseTransforms: [3]models.LinearTransform{{Offset: 1, Gain: 2}, {Offset: 1, Gain: 2}, {Offset: 1, Gain: 2}}}
	got, err := ComposeRender(context.Background(), ComposeRenderRequest{Planes: planes, Calibration: state})
	if err != nil {
		t.Fatal(err)
	}
	if got.R[0] != 1 || got.R[1] != 0 {
		t.Fatalf("render = %v, want transformed nonnegative output", got.R)
	}
	if planes[0].Pixels[0] != 2 || planes[0].Pixels[1] != -1 {
		t.Fatalf("input mutated: %v", planes[0].Pixels)
	}
}

func TestComposeRenderValidityExcludesFill(t *testing.T) {
	p := AlignedPlane{Pixels: []float32{10, 0}, Valid: []bool{true, false}, Width: 2, Height: 1}
	planes := [3]AlignedPlane{p, p, p}
	got, err := ComposeRender(context.Background(), ComposeRenderRequest{Planes: planes, Calibration: &models.ColorCalibrationState{Status: models.CalibrationValid, BaseTransforms: [3]models.LinearTransform{{Gain: 1}, {Gain: 1}, {Gain: 1}}}})
	if err != nil {
		t.Fatal(err)
	}
	if got.R[1] != 0 || got.G[1] != 0 || got.B[1] != 0 {
		t.Fatalf("invalid fill contributed: %v %v %v", got.R, got.G, got.B)
	}
}

func TestComposeRenderStaleSkipsTransform(t *testing.T) {
	p := AlignedPlane{Pixels: []float32{2}, Valid: []bool{true}, Width: 1, Height: 1}
	got, err := ComposeRender(context.Background(), ComposeRenderRequest{Planes: [3]AlignedPlane{p, p, p}, Calibration: &models.ColorCalibrationState{Status: models.CalibrationStale, BaseTransforms: [3]models.LinearTransform{{Offset: 2, Gain: 10}, {Offset: 2, Gain: 10}, {Offset: 2, Gain: 10}}}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.CalibrationStale {
		t.Fatalf("status = %q", got.Status)
	}
	if math.IsNaN(float64(got.R[0])) {
		t.Fatal("unexpected NaN")
	}
}

func TestComposeRenderOffMatchesLegacyPreview(t *testing.T) {
	imgs := make([]*models.LoadedImage, 3)
	for i := range imgs {
		imgs[i] = &models.LoadedImage{}
		imgs[i].HDU.Data.Width, imgs[i].HDU.Data.Height = 2, 1
		imgs[i].HDU.Data.Pixels = []float32{float32(i + 1), float32(i + 2)}
		imgs[i].Background, imgs[i].Peak, imgs[i].ScaledPeak = 0, 4, 1
	}
	imgs[0].Mode = stretch.Log
	imgs[1].Mode = stretch.Sqrt
	imgs[2].Mode = stretch.MTF
	got, err := ComposeRender(context.Background(), ComposeRenderRequest{Images: imgs})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{229, 180, 82, 255, 255, 220, 149, 255}
	if string(got.Preview) != string(want) {
		t.Fatalf("off preview differs from fixture: %v != %v", got.Preview, want)
	}
}

func TestComposeRenderRejectsMalformedPlanes(t *testing.T) {
	p := AlignedPlane{Pixels: []float32{1}, Valid: []bool{}, Width: 2, Height: 1}
	_, err := ComposeRender(context.Background(), ComposeRenderRequest{Planes: [3]AlignedPlane{p, p, p}})
	if err == nil {
		t.Fatal("expected malformed plane error")
	}
}

func TestComposeRenderAlignsBeforeCalibration(t *testing.T) {
	imgs := make([]*models.LoadedImage, 3)
	for i := range imgs {
		imgs[i] = &models.LoadedImage{}
		imgs[i].HDU.Data.Width, imgs[i].HDU.Data.Height = 3, 1
		imgs[i].HDU.Data.Pixels = []float32{0, 2, 4}
		imgs[i].Background, imgs[i].Peak, imgs[i].ScaledPeak = 0, 2, 1
	}
	imgs[2].HDU.Data.Width, imgs[2].HDU.Data.Pixels = 2, []float32{0, 4}
	got, err := ComposeRender(context.Background(), ComposeRenderRequest{Images: imgs, Calibration: &models.ColorCalibrationState{Status: models.CalibrationValid, BaseTransforms: [3]models.LinearTransform{{Offset: 1, Gain: 1}, {Gain: 1}, {Gain: 1}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.R) != 3 || got.R[0] != 0 || got.R[1] != 0 || got.R[2] != 1 {
		t.Fatalf("aligned/calibrated red = %v", got.R)
	}
}

func TestComposeRenderPlaneOnlyCalibrationSkipsImageOverlaySafely(t *testing.T) {
	p := AlignedPlane{Pixels: []float32{1}, Valid: []bool{true}, Width: 1, Height: 1}
	state := &models.ColorCalibrationState{Status: models.CalibrationValid, BaseTransforms: [3]models.LinearTransform{{Gain: 1}, {Gain: 1}, {Gain: 1}}, Overlays: []models.OverlayCalibrationState{{Mode: models.OverlayCalibratedLinear, Transform: models.LinearTransform{Gain: 1}, Strength: 1, Status: models.CalibrationValid}}}
	overlay := &models.LoadedImage{}
	overlay.HDU.Data.Width, overlay.HDU.Data.Height, overlay.HDU.Data.Pixels = 2, 1, []float32{1, 1}
	if _, err := ComposeRender(context.Background(), ComposeRenderRequest{Planes: [3]AlignedPlane{p, p, p}, Calibration: state, Overlays: []OverlayLayer{{Image: overlay, Settings: models.OrangeLayerState{Opacity: 1}}}}); err == nil {
		t.Fatal("expected unsupported plane-only overlay")
	}
}

func TestComposeRenderResultBackedOverlayContributes(t *testing.T) {
	p := AlignedPlane{Pixels: []float32{0}, Valid: []bool{true}, Width: 1, Height: 1}
	overlay := &models.LoadedImage{}
	overlay.HDU.Data.Width, overlay.HDU.Data.Height, overlay.HDU.Data.Pixels = 1, 1, []float32{1}
	result := &CalibrationResult{Status: models.CalibrationValid, Base: [3]models.LinearTransform{{Gain: 1}, {Gain: 1}, {Gain: 1}}, Overlays: []models.OverlayCalibrationState{{Mode: models.OverlayCalibratedLinear, Transform: models.LinearTransform{Gain: 1}, Strength: 1, Status: models.CalibrationValid}}}
	got, err := ComposeRender(context.Background(), ComposeRenderRequest{Planes: [3]AlignedPlane{p, p, p}, Result: result, Overlays: []OverlayLayer{{Image: overlay, Settings: models.OrangeLayerState{Opacity: 1, ColorR: 255}}}})
	if err != nil || got.R[0] <= got.G[0] {
		t.Fatalf("result-backed overlay not applied: %+v %v", got.R, err)
	}
}

func TestCalibratedOverlayAppliesOffsetScaleThenTintAndStrength(t *testing.T) {
	planes := []AlignedPlane{
		{Pixels: []float32{1}, Valid: []bool{true}, Width: 1, Height: 1},
		{Pixels: []float32{1}, Valid: []bool{true}, Width: 1, Height: 1},
		{Pixels: []float32{1}, Valid: []bool{true}, Width: 1, Height: 1},
	}
	state := models.OverlayCalibrationState{Transform: models.LinearTransform{Offset: 1, Gain: 2}, Strength: 3, NeutralizeBackground: true}
	settings := models.OrangeLayerState{ColorR: 128, ColorG: 64, ColorB: 0}
	applyCalibratedOverlayPixels(context.Background(), planes, []float32{5}, []bool{true}, state, settings)
	if math.Abs(float64(planes[0].Pixels[0]-13.047059)) > 1e-5 || math.Abs(float64(planes[1].Pixels[0]-7.0235295)) > 1e-5 || planes[2].Pixels[0] != 1 {
		t.Fatalf("calibrated overlay = %v/%v/%v, want 13.047059/7.0235295/1", planes[0].Pixels[0], planes[1].Pixels[0], planes[2].Pixels[0])
	}
}

func TestUnsupportedCalibratedOverlayFallsBackToArtisticDeterministically(t *testing.T) {
	imgs := make([]*models.LoadedImage, 3)
	for i := range imgs {
		imgs[i] = &models.LoadedImage{}
		imgs[i].HDU.Data.Width, imgs[i].HDU.Data.Height = 1, 1
		imgs[i].HDU.Data.Pixels = []float32{0.2}
		imgs[i].Background, imgs[i].Peak, imgs[i].ScaledPeak = 0, 1, 1
	}
	overlay := &models.LoadedImage{}
	overlay.HDU.Data.Width, overlay.HDU.Data.Height, overlay.HDU.Data.Pixels = 1, 1, []float32{1}
	layer := OverlayLayer{Image: overlay, Settings: models.OrangeLayerState{ColorR: 255, Opacity: 1}}
	base := &models.ColorCalibrationState{Status: models.CalibrationValid, BaseTransforms: [3]models.LinearTransform{{Gain: 1}, {Gain: 1}, {Gain: 1}}}
	unsupported := *base
	unsupported.Overlays = []models.OverlayCalibrationState{{Mode: models.OverlayCalibratedLinear, Status: models.CalibrationUnsupported, Transform: models.LinearTransform{Gain: 2}}}
	artistic := *base
	artistic.Overlays = []models.OverlayCalibrationState{{Mode: models.OverlayArtistic, Status: models.CalibrationDisabled}}
	a, err := ComposeRender(context.Background(), ComposeRenderRequest{Images: imgs, Calibration: &unsupported, Overlays: []OverlayLayer{layer}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := ComposeRender(context.Background(), ComposeRenderRequest{Images: imgs, Calibration: &artistic, Overlays: []OverlayLayer{layer}})
	if err != nil {
		t.Fatal(err)
	}
	if string(a.Preview) != string(b.Preview) {
		t.Fatalf("unsupported fallback differs: %v != %v", a.Preview, b.Preview)
	}
}

func TestCalibratedOverlayStrengthZeroIsPreserved(t *testing.T) {
	planes := []AlignedPlane{{Pixels: []float32{1}, Valid: []bool{true}, Width: 1, Height: 1}, {Pixels: []float32{1}, Valid: []bool{true}, Width: 1, Height: 1}, {Pixels: []float32{1}, Valid: []bool{true}, Width: 1, Height: 1}}
	state := models.OverlayCalibrationState{Transform: models.LinearTransform{Gain: 2}, Strength: 0}
	applyCalibratedOverlayPixels(context.Background(), planes, []float32{5}, []bool{true}, state, models.OrangeLayerState{ColorR: 255})
	if planes[0].Pixels[0] != 1 {
		t.Fatalf("zero strength changed overlay: %v", planes[0].Pixels[0])
	}
}

func TestOverlayRenderFingerprintIncludesOrderAndSourceContent(t *testing.T) {
	a := &models.LoadedImage{}
	a.HDU.Data.Width, a.HDU.Data.Height, a.HDU.Data.Pixels = 1, 1, []float32{1}
	b := &models.LoadedImage{}
	b.HDU.Data.Width, b.HDU.Data.Height, b.HDU.Data.Pixels = 1, 1, []float32{2}
	state := []models.OverlayCalibrationState{{Mode: models.OverlayArtistic, Passband: "F606W"}, {Mode: models.OverlayCalibratedLinear, Passband: "F814W"}}
	one := []OverlayLayer{{Image: a, Settings: models.OrangeLayerState{ColorR: 255}}, {Image: b, Settings: models.OrangeLayerState{ColorG: 255}}}
	two := []OverlayLayer{{Image: b, Settings: models.OrangeLayerState{ColorG: 255}}, {Image: a, Settings: models.OrangeLayerState{ColorR: 255}}}
	if OverlayRenderFingerprint(one, state) == OverlayRenderFingerprint(two, state) {
		t.Fatal("overlay reorder did not invalidate fingerprint")
	}
	before := OverlayRenderFingerprint(one, state)
	b.HDU.Data.Pixels[0] = 3
	if before == OverlayRenderFingerprint(one, state) {
		t.Fatal("overlay source content did not invalidate fingerprint")
	}
	mutations := []func(*models.OverlayCalibrationState){
		func(s *models.OverlayCalibrationState) { s.Passband = "F160W" },
		func(s *models.OverlayCalibrationState) { s.Strength = 0.5 },
		func(s *models.OverlayCalibrationState) { s.Mode = models.OverlayCalibratedLinear },
		func(s *models.OverlayCalibrationState) { s.NeutralizeBackground = true },
		func(s *models.OverlayCalibrationState) { s.Provenance.AlgorithmVersion = "v2" },
	}
	for i, mutate := range mutations {
		base := append([]models.OverlayCalibrationState(nil), state...)
		want := OverlayRenderFingerprint(one, base)
		mutate(&base[0])
		if want == OverlayRenderFingerprint(one, base) {
			t.Fatalf("overlay mutation %d did not invalidate fingerprint", i)
		}
	}
	tinted := append([]OverlayLayer(nil), one...)
	base := OverlayRenderFingerprint(tinted, state)
	tinted[0].Settings.ColorR = 12
	if base == OverlayRenderFingerprint(tinted, state) {
		t.Fatal("tint mutation did not invalidate fingerprint")
	}
}

func TestMixedArtisticAndCalibratedOverlaysRemainIndependent(t *testing.T) {
	imgs := make([]*models.LoadedImage, 3)
	for i := range imgs {
		imgs[i] = &models.LoadedImage{}
		imgs[i].HDU.Data.Width, imgs[i].HDU.Data.Height = 1, 1
		imgs[i].HDU.Data.Pixels = []float32{0.2}
		imgs[i].Background, imgs[i].Peak, imgs[i].ScaledPeak = 0, 1, 1
	}
	a, b := &models.LoadedImage{}, &models.LoadedImage{}
	a.HDU.Data.Width, a.HDU.Data.Height, a.HDU.Data.Pixels = 1, 1, []float32{1}
	b.HDU.Data.Width, b.HDU.Data.Height, b.HDU.Data.Pixels = 1, 1, []float32{2}
	state := &models.ColorCalibrationState{Status: models.CalibrationValid, BaseTransforms: [3]models.LinearTransform{{Gain: 1}, {Gain: 1}, {Gain: 1}}, Overlays: []models.OverlayCalibrationState{{Mode: models.OverlayArtistic, Status: models.CalibrationDisabled}, {Mode: models.OverlayCalibratedLinear, Status: models.CalibrationValid, Transform: models.LinearTransform{Gain: 1}, Strength: 1}}}
	rendered, err := ComposeRender(context.Background(), ComposeRenderRequest{Images: imgs, Calibration: state, Overlays: []OverlayLayer{{Image: a, Settings: models.OrangeLayerState{ColorR: 255, Opacity: 1}}, {Image: b, Settings: models.OrangeLayerState{ColorG: 255, Opacity: 1}}}})
	if err != nil || len(rendered.OverlayStatus) != 2 {
		t.Fatalf("mixed overlay render = %+v, %v", rendered, err)
	}
	if rendered.OverlayStatus[0] != models.CalibrationDisabled || rendered.OverlayStatus[1] != models.CalibrationValid {
		t.Fatalf("mixed statuses = %v", rendered.OverlayStatus)
	}
	artOnly := *state
	artOnly.Overlays = []models.OverlayCalibrationState{{Mode: models.OverlayArtistic, Status: models.CalibrationDisabled}}
	calOnly := *state
	calOnly.Overlays = []models.OverlayCalibrationState{{Mode: models.OverlayCalibratedLinear, Status: models.CalibrationValid, Transform: models.LinearTransform{Gain: 1}, Strength: 1}}
	baseOnly, _ := ComposeRender(context.Background(), ComposeRenderRequest{Images: imgs, Calibration: &models.ColorCalibrationState{Status: models.CalibrationValid, BaseTransforms: state.BaseTransforms}})
	art, _ := ComposeRender(context.Background(), ComposeRenderRequest{Images: imgs, Calibration: &artOnly, Overlays: []OverlayLayer{{Image: a, Settings: models.OrangeLayerState{ColorR: 255, Opacity: 1}}}})
	cal, _ := ComposeRender(context.Background(), ComposeRenderRequest{Images: imgs, Calibration: &calOnly, Overlays: []OverlayLayer{{Image: b, Settings: models.OrangeLayerState{ColorG: 255, Opacity: 1}}}})
	if string(rendered.Preview) == string(baseOnly.Preview) || string(art.Preview) == string(baseOnly.Preview) || string(cal.Preview) == string(baseOnly.Preview) || string(rendered.Preview) == string(art.Preview) || string(rendered.Preview) == string(cal.Preview) {
		t.Fatalf("overlay contributions missing: base=%v art=%v cal=%v mixed=%v", baseOnly.Preview, art.Preview, cal.Preview, rendered.Preview)
	}
}

func TestArtisticOverlayByteRegressionWithValidBaseCalibration(t *testing.T) {
	imgs := make([]*models.LoadedImage, 3)
	for i := range imgs {
		imgs[i] = &models.LoadedImage{}
		imgs[i].HDU.Data.Width, imgs[i].HDU.Data.Height = 1, 1
		imgs[i].HDU.Data.Pixels = []float32{1}
		imgs[i].Background, imgs[i].Peak, imgs[i].ScaledPeak = 0, 1, 1
	}
	overlay := &models.LoadedImage{}
	overlay.HDU.Data.Width, overlay.HDU.Data.Height, overlay.HDU.Data.Pixels = 1, 1, []float32{1}
	state := &models.ColorCalibrationState{Status: models.CalibrationValid, BaseTransforms: [3]models.LinearTransform{{Gain: 1}, {Gain: 1}, {Gain: 1}}, Overlays: []models.OverlayCalibrationState{{Mode: models.OverlayArtistic, Status: models.CalibrationDisabled}}}
	layer := OverlayLayer{Image: overlay, Settings: models.OrangeLayerState{ColorR: 255, Opacity: 1}}
	a, err := ComposeRender(context.Background(), ComposeRenderRequest{Images: imgs, Calibration: state, Overlays: []OverlayLayer{layer}})
	if err != nil {
		t.Fatal(err)
	}
	legacy, _, _, _ := ComposeRGBWithOverlays(context.Background(), imgs, []OverlayLayer{layer})
	if string(a.Preview) != string(legacy) {
		t.Fatalf("artistic overlay differs from legacy path: %v != %v", a.Preview, legacy)
	}
}

func TestCalibratedOverlayHueIsIndependentOfBaseGains(t *testing.T) {
	settings := models.OrangeLayerState{ColorR: 200, ColorG: 80, ColorB: 20}
	state := models.OverlayCalibrationState{Status: models.CalibrationValid, Transform: models.LinearTransform{Gain: 2}, Strength: 1}
	makePlanes := func() []AlignedPlane {
		return []AlignedPlane{{Pixels: []float32{10}, Valid: []bool{true}, Width: 1, Height: 1}, {Pixels: []float32{3}, Valid: []bool{true}, Width: 1, Height: 1}, {Pixels: []float32{-4}, Valid: []bool{true}, Width: 1, Height: 1}}
	}
	a, b := makePlanes(), makePlanes()
	b[0].Pixels[0] *= 7
	b[1].Pixels[0] *= 0.25
	b[2].Pixels[0] *= 3
	applyCalibratedOverlayPixels(context.Background(), a, []float32{2}, []bool{true}, state, settings)
	applyCalibratedOverlayPixels(context.Background(), b, []float32{2}, []bool{true}, state, settings)
	want := []float32{float32(4 * 200.0 / 255), float32(4 * 80.0 / 255), float32(4 * 20.0 / 255)}
	for i := range a {
		if math.Abs(float64(a[i].Pixels[0]-[]float32{10, 3, -4}[i]-want[i])) > 1e-4 || math.Abs(float64(b[i].Pixels[0]-[]float32{70, .75, -12}[i]-want[i])) > 1e-4 {
			t.Fatalf("base gains changed overlay hue contribution: a=%v b=%v want=%v", a[i].Pixels[0], b[i].Pixels[0], want[i])
		}
	}
}
