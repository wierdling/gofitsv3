package processing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/render"
	"gofitsv3/internal/stretch"
)

// TestColorCalibrationProjectSaveLoadRenderGate exercises the Phase 1 contract
// at the project boundary: a calculation is persisted, reloaded, and consumed
// by the canonical renderer without mutating source planes. It intentionally
// uses tiny deterministic planes so this remains a fast regression fixture.
func TestColorCalibrationProjectSaveLoadRenderGate(t *testing.T) {
	planes := [3]AlignedPlane{
		{Pixels: []float32{10, 10, 10, 10}, Valid: []bool{true, true, true, true}, Width: 2, Height: 2},
		{Pixels: []float32{20, 20, 20, 20}, Valid: []bool{true, true, true, true}, Width: 2, Height: 2},
		{Pixels: []float32{30, 30, 30, 30}, Valid: []bool{true, true, true, true}, Width: 2, Height: 2},
	}
	photometry := make([]*InstrumentPhotometry, 3)
	roi := models.CalibrationROI{X: 0, Y: 0, Width: 2, Height: 2}
	for i, filter := range []string{"F435W", "F606W", "F814W"} {
		p, err := ParseInstrumentPhotometry(InstrumentMetadata{
			Telescope: "HST", Instrument: "ACS", Detector: "WFC", Filter: filter,
			Reference: ReferenceFnu,
			SCI: fitsio.Header{Cards: map[string]string{
				"BUNIT": "ELECTRONS/S", "PHOTFLAM": "1e-19", "PHOTPLAM": "6000",
			}},
		})
		if err != nil {
			t.Fatalf("parse instrument metadata for %s: %v", filter, err)
		}
		photometry[i] = &p
	}

	inputs := make([]CalibrationInput, 3)
	for i := range planes {
		background, err := EstimateBackgroundPlane(context.Background(), BackgroundPlane{
			Pixels: planes[i].Pixels, Valid: planes[i].Valid, Width: planes[i].Width, Height: planes[i].Height, ROI: &roi,
		}, BackgroundConfig{MinSamples: 4, TileSize: 1})
		if err != nil || background.Status != models.CalibrationValid {
			t.Fatalf("background[%d] = %+v, err=%v", i, background, err)
		}
		background.Transform.Gain = 1 // the estimator publishes an offset-only transform
		inputs[i] = CalibrationInput{
			SourceIdentity: fmt.Sprintf("channel-%d", i), Width: planes[i].Width, Height: planes[i].Height,
			Pixels: planes[i].Pixels, Valid: planes[i].Valid, Background: background.Transform,
			Photometry: photometry[i], Alignment: "green-reference-v1",
			Metadata: InstrumentMetadata{Telescope: "HST", Instrument: "ACS", Detector: "WFC", Filter: photometry[i].Filter, Reference: ReferenceFnu},
		}
	}
	settings := CalibrationSettings{
		PhotometricMode: models.PhotometricInstrument, NeutralizeBackground: true,
		BackgroundSelection: models.BackgroundROI, BackgroundROI: roi, WhiteReference: models.WhiteReferenceFlatFnu,
		LinkedStretch:    models.CalibrationStretchSettings{Mode: "linear", Linked: true},
		AlgorithmVersion: "phase1-fixture-v1", ReferenceVersion: "hst-header-v1",
	}
	result, err := CalculateCalibration(inputs, settings)
	if err != nil || result.Status != models.CalibrationValid {
		t.Fatalf("calculation = %+v, err=%v", result, err)
	}

	baseState := models.ColorCalibrationState{
		Version: 1, PhotometricMode: models.PhotometricInstrument, NeutralizeBackground: true,
		BackgroundSelection: models.BackgroundROI, BackgroundROI: roi, WhiteReference: models.WhiteReferenceFlatFnu,
		LinkedStretch:  settings.LinkedStretch,
		BaseTransforms: result.Base, Status: result.Status, Diagnostics: result.Diagnostics,
		Provenance: result.Provenance, SourceFingerprint: result.SourceFingerprint,
		SettingsFingerprint: result.SettingsFingerprint,
		Overlays: []models.OverlayCalibrationState{
			{Mode: models.OverlayArtistic, Status: models.CalibrationDisabled},
			{Mode: models.OverlayCalibratedLinear, Status: models.CalibrationValid, Transform: models.LinearTransform{Gain: 1}, Strength: 1},
		},
	}
	project := models.ComposeProject{ColorCalibration: &baseState}
	encoded, err := json.Marshal(project)
	if err != nil {
		t.Fatal(err)
	}
	var reloaded models.ComposeProject
	if err := json.Unmarshal(encoded, &reloaded); err != nil {
		t.Fatal(err)
	}
	if reloaded.ColorCalibration == nil || reloaded.ColorCalibration.Status != models.CalibrationValid {
		t.Fatalf("reloaded calibration = %+v", reloaded.ColorCalibration)
	}
	if reloaded.ColorCalibration.SourceFingerprint != result.SourceFingerprint {
		t.Fatal("save/load changed source fingerprint")
	}
	if reloaded.ColorCalibration.BackgroundSelection != models.BackgroundROI || reloaded.ColorCalibration.BackgroundROI != roi {
		t.Fatalf("save/load changed background ROI: %+v", reloaded.ColorCalibration)
	}

	images := make([]*models.LoadedImage, 3)
	for i := range images {
		images[i] = &models.LoadedImage{HDU: fitsio.HDU{Data: fitsio.ImageData{Width: 2, Height: 2, Pixels: append([]float32(nil), planes[i].Pixels...)}}, Background: 0, Peak: 40, ScaledPeak: 1}
	}
	overlayImage := &models.LoadedImage{HDU: fitsio.HDU{Data: fitsio.ImageData{Width: 2, Height: 2, Pixels: []float32{1, 1, 1, 1}}}, Background: 0, Peak: 1, ScaledPeak: 1}
	overlays := []OverlayLayer{
		{Image: overlayImage, Settings: models.OrangeLayerState{ColorR: 255, Opacity: 0.25}},
		{Image: overlayImage, Settings: models.OrangeLayerState{ColorG: 255, Opacity: 0.25}},
	}
	valid, err := ComposeRender(context.Background(), ComposeRenderRequest{Images: images, Planes: planes, Calibration: reloaded.ColorCalibration, Overlays: overlays})
	if err != nil || valid.Status != models.CalibrationValid {
		t.Fatalf("valid render = %+v, err=%v", valid, err)
	}
	if len(valid.Preview) == 0 || valid.OverlayStatus[0] != models.CalibrationDisabled || valid.OverlayStatus[1] != models.CalibrationValid {
		t.Fatalf("render statuses/preview = %v/%v", valid.OverlayStatus, valid.Preview)
	}
	exportedPreview := render.ComposeRGB(valid.R, valid.G, valid.B, valid.Width, valid.Height, stretch.Linear, stretch.Linear, stretch.Linear)
	if !bytes.Equal(valid.Preview, exportedPreview) {
		t.Fatal("preview and export representations diverged")
	}

	off, err := ComposeRender(context.Background(), ComposeRenderRequest{Images: images})
	if err != nil || off.Status != models.CalibrationDisabled {
		t.Fatalf("off render = %+v, err=%v", off, err)
	}

	mutatedInputs := append([]CalibrationInput(nil), inputs...)
	mutatedInputs[0].Pixels = append([]float32(nil), inputs[0].Pixels...)
	mutatedInputs[0].Pixels[0]++
	if !CalibrationResultStale(result, mutatedInputs, settings) {
		t.Fatal("pixel mutation did not mark calibration stale")
	}
	staleState := *reloaded.ColorCalibration
	staleState.Status = models.CalibrationStale
	validBase, err := ComposeRender(context.Background(), ComposeRenderRequest{Images: images, Planes: planes, Calibration: reloaded.ColorCalibration})
	if err != nil {
		t.Fatal(err)
	}
	stale, err := ComposeRender(context.Background(), ComposeRenderRequest{Images: images, Planes: planes, Calibration: &staleState})
	if err != nil || stale.Status != models.CalibrationStale {
		t.Fatalf("stale render = %+v, err=%v", stale, err)
	}
	if stale.R[0] == validBase.R[0] {
		t.Fatal("stale calibration transform was applied")
	}
}
