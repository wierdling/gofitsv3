package ui

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"fyne.io/fyne/v2/test"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
	"gofitsv3/internal/starstretchpreview"
	"gofitsv3/internal/stretch"
)

func starTreatmentTestImage(t *testing.T) (*models.LoadedImage, *processing.StarTreatmentModel) {
	t.Helper()
	const w, h = 80, 80
	pixels := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x-40), float64(y-40)
			pixels[y*w+x] = float32(.05 + math.Exp(-.5*(dx*dx+dy*dy)/(1.5*1.5)))
		}
	}
	meta := models.LoadedImage{Mode: stretch.Asinh, Background: 0, Peak: 1, ScaledPeak: 10, AsinhScale: 1}
	m := &processing.StarMap{Width: w, Height: h, Sources: []processing.StarMapSource{{ID: 1, X: 40, Y: 40, FWHM: 3.5, Radius: 6, Status: "accepted"}}}
	fits, err := processing.FitStarTreatment(context.Background(), m, pixels, w, h, processing.StarTreatmentOptions{})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := processing.PrepareStarStretchFits(context.Background(), pixels, w, h, meta, fits, m.Sources)
	if err != nil {
		t.Fatal(err)
	}
	model, err := processing.NewStarTreatmentModel(prepared, w, h, meta, .5)
	if err != nil {
		t.Fatal(err)
	}
	img := meta
	img.Path = "synthetic.fits"
	img.HDU.Data = fitsio.ImageData{Width: w, Height: h, Pixels: pixels}
	img.StarStretch = models.StarStretchState{Enabled: true, Strength: .5}
	return &img, model
}

func TestComposeStarTreatmentsSyncAttachesAndInvalidates(t *testing.T) {
	win := test.NewWindow(nil)
	defer win.Close()
	img, model := starTreatmentTestImage(t)
	c := newComposeStarTreatments(win, func() bool { return false })
	// Pretend a job already prepared this model.
	c.entries[img] = &composeStarTreatmentEntry{model: model, busy: true} // busy blocks a real job in this test
	imgs := []*models.LoadedImage{nil, img, nil}

	c.sync(imgs, false)
	if img.StarTreatment != model {
		t.Fatal("current model was not attached")
	}
	img.Peak = 2 // stretch changed: model is stale
	c.sync(imgs, false)
	if img.StarTreatment != nil {
		t.Fatal("stale model stayed attached")
	}
	img.Peak = 1
	img.StarStretch.Strength = .9 // strength changed: also stale
	c.sync(imgs, false)
	if img.StarTreatment != nil {
		t.Fatal("model with a different strength stayed attached")
	}
	img.StarStretch.Strength = .5
	c.sync(imgs, false)
	if img.StarTreatment != model {
		t.Fatal("model not reattached after settings returned")
	}
	img.StarStretch.Enabled = false
	c.sync(imgs, false)
	if img.StarTreatment != nil {
		t.Fatal("disabled source kept its treatment")
	}
	// A source that left the workspace is forgotten.
	c.sync([]*models.LoadedImage{nil, nil, nil}, false)
	if _, ok := c.entries[img]; ok {
		t.Fatal("entry for an unloaded source was retained")
	}
}

func TestComposeStarTreatmentsRefusesRotatedOrPathlessSource(t *testing.T) {
	win := test.NewWindow(nil)
	defer win.Close()
	img, _ := starTreatmentTestImage(t)
	c := newComposeStarTreatments(win, func() bool { return false })
	img.Rotation90 = 1
	c.sync([]*models.LoadedImage{img}, false)
	if img.StarTreatment != nil || c.statusFor(img) == "" || c.entries[img].busy {
		t.Fatalf("rotated source should be refused without a job: %q", c.statusFor(img))
	}
	img.Rotation90 = 0
	img.Path = ""
	c.sync([]*models.LoadedImage{img}, false)
	if img.StarTreatment != nil || c.entries[img].busy {
		t.Fatalf("pathless source should be refused without a job: %q", c.statusFor(img))
	}
}

func TestStarStretchStateRoundTripsThroughChannelState(t *testing.T) {
	img := &models.LoadedImage{Mode: stretch.Asinh, StarStretch: models.StarStretchState{Enabled: true, Strength: .6}}
	state := channelStateFromImage(img)
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var decoded models.ChannelState
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	restored := &models.LoadedImage{}
	applyChannelStateToImage(restored, decoded)
	if restored.StarStretch != img.StarStretch {
		t.Fatalf("round trip lost the setting: %+v", restored.StarStretch)
	}
	// Disabled settings are omitted so untouched projects do not change.
	off := channelStateFromImage(&models.LoadedImage{})
	if off.StarStretch != nil {
		t.Fatal("disabled setting was persisted")
	}
	data, _ = json.Marshal(off)
	if strings.Contains(string(data), "starStretch") {
		t.Fatalf("disabled setting appears in JSON: %s", data)
	}
}

func TestComposeStarTreatmentsSharedGeometryWaitsForReference(t *testing.T) {
	win := test.NewWindow(nil)
	defer win.Close()
	ref, refModel := starTreatmentTestImage(t)
	other, _ := starTreatmentTestImage(t)
	other.Path = "other.fits"
	c := newComposeStarTreatments(win, func() bool { return false })
	c.geometryRef = func() *models.LoadedImage { return ref }
	imgs := []*models.LoadedImage{other, ref}
	// Reference not prepared yet: the other source waits, no job starts.
	c.entries[ref] = &composeStarTreatmentEntry{busy: true}
	c.sync(imgs, false)
	if other.StarTreatment != nil || c.entries[other].busy || !strings.Contains(c.statusFor(other), "waiting") {
		t.Fatalf("other source should wait for the reference: %q", c.statusFor(other))
	}
	// Reference prepared: a model derived from it is current; one fitted on its
	// own map is not.
	c.entries[ref] = &composeStarTreatmentEntry{model: refModel, fits: &starstretchpreview.TreatmentFits{}}
	c.entries[other] = &composeStarTreatmentEntry{model: refModel, derivedFrom: nil, busy: true}
	c.sync(imgs, false)
	if other.StarTreatment != nil {
		t.Fatal("own-map model accepted under shared geometry")
	}
	c.entries[other] = &composeStarTreatmentEntry{model: refModel, derivedFrom: refModel, busy: true}
	c.sync(imgs, false)
	if other.StarTreatment != refModel {
		t.Fatal("derived model not attached")
	}
	// The reference itself renders through its own model.
	if ref.StarTreatment != refModel {
		t.Fatal("reference did not use its own model")
	}
}
