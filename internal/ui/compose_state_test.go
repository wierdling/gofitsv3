package ui

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"fyne.io/fyne/v2/driver/desktop"
	fynetest "fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/histogram"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
	"gofitsv3/internal/stretch"
)

func TestComposeCalibrationJobRejectsLateResult(t *testing.T) {
	var job composeCalibrationJob
	if _, ok := job.begin(1); !ok {
		t.Fatal("begin rejected")
	}
	state := models.ColorCalibrationState{Status: models.CalibrationCalculating}
	result := processing.CalibrationResult{Status: models.CalibrationValid, Base: [3]models.LinearTransform{{Gain: 1}, {Gain: 2}, {Gain: 3}}}
	if composeCalibrationResultForUI(&job, 1, 2, &state, nil, result, nil) {
		t.Fatal("late result was accepted")
	}
	if state.Status != models.CalibrationCalculating {
		t.Fatalf("late result changed state: %s", state.Status)
	}
}

func TestComposeCalibrationJobSuccessAndFailure(t *testing.T) {
	var job composeCalibrationJob
	state := models.ColorCalibrationState{Status: models.CalibrationCalculating}
	ctx, ok := job.begin(3)
	if !ok || ctx == nil {
		t.Fatal("begin failed")
	}
	result := processing.CalibrationResult{Status: models.CalibrationValid, Base: [3]models.LinearTransform{{Gain: 1}, {Gain: 2}, {Gain: 3}}, SourceFingerprint: "source"}
	if !composeCalibrationResultForUI(&job, 3, 3, &state, nil, result, nil) || state.Status != models.CalibrationValid || state.SourceFingerprint != "source" {
		t.Fatalf("success not published: %+v", state)
	}
	state.BaseTransforms = [3]models.LinearTransform{}
	if _, ok := job.begin(4); !ok {
		t.Fatal("second begin failed")
	}
	if !composeCalibrationResultForUI(&job, 4, 4, &state, nil, processing.CalibrationResult{}, errors.New("bad metadata")) || state.Status != models.CalibrationFailed {
		t.Fatalf("failure not published: %+v", state)
	}
	state = models.ColorCalibrationState{Status: models.CalibrationStale, BaseTransforms: [3]models.LinearTransform{{Gain: 1}, {Gain: 1}, {Gain: 1}}}
	prior := state
	if _, ok := job.begin(5); !ok {
		t.Fatal("third begin failed")
	}
	if !composeCalibrationResultForUI(&job, 5, 5, &state, &prior, processing.CalibrationResult{}, errors.New("temporary")) || state.Status != models.CalibrationStale {
		t.Fatalf("prior stale result was not retained after failure: %+v", state)
	}
	state = models.ColorCalibrationState{Status: models.CalibrationCalculating, BaseTransforms: prior.BaseTransforms}
	if _, ok := job.begin(6); !ok {
		t.Fatal("unsupported begin failed")
	}
	if !composeCalibrationResultForUI(&job, 6, 6, &state, &prior, processing.CalibrationResult{}, &processing.UnsupportedPhotometryError{Reason: "missing filter"}) || state.Status != models.CalibrationUnsupported {
		t.Fatalf("unsupported result did not disable application: %+v", state)
	}
	state = models.ColorCalibrationState{Status: models.CalibrationCalculating, BaseTransforms: prior.BaseTransforms}
	if _, ok := job.begin(7); !ok {
		t.Fatal("unsupported background begin failed")
	}
	if !composeCalibrationResultForUI(&job, 7, 7, &state, &prior, processing.CalibrationResult{}, &processing.UnsupportedCalibrationError{Reason: "invalid ROI"}) || state.Status != models.CalibrationUnsupported {
		t.Fatalf("unsupported background result did not publish: %+v", state)
	}
}

func TestComposeCalibrationResultPersistsGaiaProvenanceAndFingerprints(t *testing.T) {
	var job composeCalibrationJob
	if _, ok := job.begin(11); !ok {
		t.Fatal("begin failed")
	}
	prov := models.CalibrationProvenance{CatalogVersion: "custom-release", ProviderVersion: "provider-x", SourceIDs: []uint64{7, 9}}
	result := processing.CalibrationResult{Status: models.CalibrationValid, Provenance: prov, SourceFingerprint: "src-fp", SettingsFingerprint: "settings-fp"}
	state := models.ColorCalibrationState{Status: models.CalibrationCalculating}
	if !composeCalibrationResultForUI(&job, 11, 11, &state, nil, result, nil) {
		t.Fatal("result rejected")
	}
	if state.Provenance.CatalogVersion != prov.CatalogVersion || state.Provenance.ProviderVersion != prov.ProviderVersion || state.SettingsFingerprint != result.SettingsFingerprint || state.SourceFingerprint != result.SourceFingerprint || len(state.Provenance.SourceIDs) != 2 {
		t.Fatalf("Gaia result was not persisted verbatim: %+v", state)
	}
}

func TestComposeCalibrationSnapshotDisablePreservesLiveState(t *testing.T) {
	state := models.ColorCalibrationState{Status: models.CalibrationValid, BaseTransforms: [3]models.LinearTransform{{Gain: 2}}, Provenance: models.CalibrationProvenance{CatalogVersion: "DR3"}, Overlays: []models.OverlayCalibrationState{{Status: models.CalibrationValid, Transform: models.LinearTransform{Gain: 3}}}}
	snapshot := composeCalibrationSnapshot(state, true)
	if snapshot.Status != models.CalibrationDisabled || state.Status != models.CalibrationValid {
		t.Fatalf("disable changed wrong state: snapshot=%v live=%v", snapshot.Status, state.Status)
	}
	if snapshot.BaseTransforms != state.BaseTransforms || snapshot.Provenance.CatalogVersion != state.Provenance.CatalogVersion || snapshot.Overlays[0].Transform != state.Overlays[0].Transform {
		t.Fatal("snapshot lost calibration transforms or provenance")
	}
	snapshot = composeCalibrationSnapshot(state, false)
	if snapshot.Status != models.CalibrationValid {
		t.Fatalf("re-enabled snapshot status = %v", snapshot.Status)
	}
}

func TestComposeCalibrationPreviewOverrideDoesNotChangeExportGate(t *testing.T) {
	state := models.ColorCalibrationState{Status: models.CalibrationValid, BaseTransforms: [3]models.LinearTransform{{Gain: 2}, {Gain: 1}, {Gain: 1}}}
	before := false
	preview := composeCalibrationPreviewSnapshot(state, true, &before)
	if preview.Status != models.CalibrationDisabled {
		t.Fatalf("Before preview status=%v, want disabled", preview.Status)
	}
	export := composeCalibrationSnapshot(state, false)
	if export.Status != models.CalibrationValid {
		t.Fatalf("export snapshot status=%v, want valid while Save is enabled", export.Status)
	}
	export = composeCalibrationSnapshot(state, true)
	if export.Status != models.CalibrationDisabled {
		t.Fatalf("export snapshot status=%v, want disabled while Save is disabled", export.Status)
	}
}

func TestUpdateComposeGaiaCachePathPreservesValidCalibration(t *testing.T) {
	state := models.ColorCalibrationState{Status: models.CalibrationValid, BaseTransforms: [3]models.LinearTransform{{Gain: 1}, {Gain: 1}, {Gain: 1}}}
	updateComposeGaiaCachePath(&state, "cache.db")
	if state.Status != models.CalibrationValid || state.Gaia.CachePath != "cache.db" {
		t.Fatalf("cache path edit altered calibration state: %+v", state)
	}
}

func TestRetainComposeProjectGaiaCachePathDoesNotPinFallback(t *testing.T) {
	project := models.GaiaCalibrationSettings{}
	resolved := models.GaiaCalibrationSettings{CachePath: "A.db", Release: "DR3"}
	accepted := retainComposeProjectGaiaCachePath(project, resolved)
	if accepted.CachePath != "" || accepted.Release != "DR3" {
		t.Fatalf("resolved settings were not separated from project path: %+v", accepted)
	}
	project.CachePath = "explicit.db"
	accepted = retainComposeProjectGaiaCachePath(project, resolved)
	if accepted.CachePath != "explicit.db" {
		t.Fatalf("explicit project path was not retained: %q", accepted.CachePath)
	}
	project.CachePath = "B.db"
	accepted = retainComposeProjectGaiaCachePath(project, resolved)
	if accepted.CachePath != "B.db" {
		t.Fatalf("live in-flight project path was not retained: %q", accepted.CachePath)
	}
}

func TestComposeCalibrationCancelAndStale(t *testing.T) {
	var job composeCalibrationJob
	if _, ok := job.begin(1); !ok || !job.cancelJob() || job.cancelJob() {
		t.Fatal("cancel state transition incorrect")
	}
	state := models.ColorCalibrationState{Status: models.CalibrationValid}
	markComposeCalibrationStale(&state)
	if state.Status != models.CalibrationStale {
		t.Fatalf("expected stale, got %s", state.Status)
	}
	state.Status = models.CalibrationDisabled
	markComposeCalibrationStale(&state)
	if state.Status != models.CalibrationDisabled {
		t.Fatal("disabled state should remain disabled")
	}
}

func TestComposeCalibrationStatusText(t *testing.T) {
	got := composeCalibrationStatusText(models.ColorCalibrationState{Status: models.CalibrationStale})
	if got == "" || got == "Calibration: stale" {
		t.Fatalf("missing actionable status: %q", got)
	}
}

func TestClearComposeOrigPixels(t *testing.T) {
	orig := [][]float32{
		{1},
		{2},
		{3},
	}

	clearComposeOrigPixels(&orig, -1, 0, 2, 3)

	if orig[0] != nil {
		t.Fatalf("orig[0] = %#v, want nil", orig[0])
	}
	if orig[1] == nil || len(orig[1]) != 1 || orig[1][0] != 2 {
		t.Fatalf("orig[1] = %#v, want preserved slice", orig[1])
	}
	if orig[2] != nil {
		t.Fatalf("orig[2] = %#v, want nil", orig[2])
	}
}

func TestDefaultRGBLevelsAndModeLabelRoundTrip(t *testing.T) {
	levels := defaultRGBLevels()
	if levels.Min != [3]float64{0, 0, 0} {
		t.Fatalf("Min = %v, want zeros", levels.Min)
	}
	if levels.Max != [3]float64{255, 255, 255} {
		t.Fatalf("Max = %v, want 255s", levels.Max)
	}

	tests := []struct {
		mode  stretch.Mode
		label string
	}{
		{mode: stretch.Linear, label: "Linear"},
		{mode: stretch.Log, label: "Log"},
		{mode: stretch.Asinh, label: "Asinh"},
		{mode: stretch.Sqrt, label: "Sqrt"},
		{mode: stretch.HistEq, label: "HistEq"},
		{mode: stretch.MTF, label: "MTF"},
		{mode: stretch.GHS, label: "GHS"},
	}
	for _, tt := range tests {
		if got := modeToLabel(tt.mode); got != tt.label {
			t.Fatalf("modeToLabel(%v) = %q, want %q", tt.mode, got, tt.label)
		}
		if got := labelToMode(tt.label); got != tt.mode {
			t.Fatalf("labelToMode(%q) = %v, want %v", tt.label, got, tt.mode)
		}
	}

	if got := labelToMode("unknown"); got != stretch.Linear {
		t.Fatalf("labelToMode(unknown) = %v, want Linear", got)
	}
	if got := modeToLabel(stretch.Mode(999)); got != "Linear" {
		t.Fatalf("modeToLabel(unknown) = %q, want Linear", got)
	}
}

func TestChannelStateRoundTripAndApplyChannelState(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	img := &models.LoadedImage{
		Path:       "channel1.fits",
		Mode:       stretch.Log,
		Black:      1.5,
		White:      9.5,
		Background: 2.5,
		Peak:       8.5,
		ScaledPeak: 7.5,
		MTFMidtone: 0.37,
		ShowClip:   true,
	}
	state := channelStateFromImage(img)
	if state.Path != "channel1.fits" || state.Mode != "Log" || !state.ShowClip {
		t.Fatalf("channelStateFromImage = %+v", state)
	}
	if state.MTFMidtone != 0.37 {
		t.Fatalf("channelStateFromImage MTFMidtone = %v, want 0.37", state.MTFMidtone)
	}

	target := &models.LoadedImage{}
	imgs := []*models.LoadedImage{target}
	views := []*viewport{{
		blackBox: NewNumberEntry(0.001, 3),
		whiteBox: NewNumberEntry(0.001, 3),
	}}
	modeSelect := widget.NewSelect([]string{"Linear", "Log", "Asinh", "Sqrt", "HistEq"}, nil)
	background := &stubNumberField{}
	peak := &stubNumberField{}
	scaledPeak := &stubNumberField{}
	showClip := &stubCheckField{}
	controls := []*models.ChannelControl{{
		ModeSelect:      modeSelect,
		BackgroundEntry: background,
		PeakEntry:       peak,
		ScaledPeakEntry: scaledPeak,
		ShowClip:        showClip,
	}}

	applyChannelState(0, state, imgs, views, controls)

	if target.Mode != stretch.Log || target.Black != 1.5 || target.White != 9.5 {
		t.Fatalf("applied image state = %+v", target)
	}
	if target.Background != 2.5 || target.Peak != 8.5 || target.ScaledPeak != 7.5 || !target.ShowClip {
		t.Fatalf("applied image scalar state = %+v", target)
	}
	if target.MTFMidtone != 0.37 {
		t.Fatalf("applied MTFMidtone = %v, want 0.37", target.MTFMidtone)
	}
	if modeSelect.Selected != "Log" {
		t.Fatalf("ModeSelect.Selected = %q, want Log", modeSelect.Selected)
	}
	if background.val != 2.5 || peak.val != 8.5 || scaledPeak.val != 7.5 {
		t.Fatalf("control values = (%v,%v,%v), want (2.5,8.5,7.5)", background.val, peak.val, scaledPeak.val)
	}
	if !showClip.checked {
		t.Fatal("ShowClip should be set true")
	}
	if views[0].blackBox.Value() != 1.5 || views[0].whiteBox.Value() != 9.5 {
		t.Fatalf("viewport levels = (%v,%v), want (1.5,9.5)", views[0].blackBox.Value(), views[0].whiteBox.Value())
	}
}

func TestBuildComposePreviewDataUsesOverrideAndHandlesMissingChannels(t *testing.T) {
	levels := defaultRGBLevels()

	missing := buildComposePreviewData(context.Background(), make([]*models.LoadedImage, 3), false, true, levels, nil)
	for i := 0; i < 4; i++ {
		if missing.Views[i].Image == nil {
			t.Fatalf("missing.Views[%d].Image is nil", i)
		}
	}
	if missing.Views[3].OrigW != 0 || missing.Views[3].OrigH != 0 {
		t.Fatalf("missing compose size = %dx%d, want 0x0", missing.Views[3].OrigW, missing.Views[3].OrigH)
	}

	imgs := []*models.LoadedImage{
		makeLoadedImageForUITest(1, 2, []float32{0, 1}),
		makeLoadedImageForUITest(1, 2, []float32{0.25, 0.75}),
		makeLoadedImageForUITest(1, 2, []float32{1, 0}),
	}
	data := buildComposePreviewData(context.Background(), imgs, false, true, levels, func(context.Context) ([]byte, int, int, [3]histogram.Stats, error) {
		return []byte{
			1, 2, 3, 255,
			10, 20, 30, 255,
		}, 1, 2, [3]histogram.Stats{{Mean: 1}, {Mean: 2}, {Mean: 3}}, nil
	})
	if data.Views[3].OrigW != 1 || data.Views[3].OrigH != 2 {
		t.Fatalf("compose size = %dx%d, want 1x2", data.Views[3].OrigW, data.Views[3].OrigH)
	}
	if data.RGBStats[0].Mean != 1 || data.RGBStats[1].Mean != 2 || data.RGBStats[2].Mean != 3 {
		t.Fatalf("RGBStats = %+v, want override stats", data.RGBStats)
	}
	if got := data.Views[3].Image.RGBAAt(0, 0); got.R != 1 || got.G != 2 || got.B != 3 {
		t.Fatalf("compose first pixel = %#v, want R=1 G=2 B=3", got)
	}
	if data.Views[3].Bins[18] != 1 {
		t.Fatalf("composite luminance bin 18 = %d, want 1", data.Views[3].Bins[18])
	}
	if data.Views[3].StatsText == "Sky --  μ --  σ --" || data.Views[3].StatsText == "" {
		t.Fatalf("composite StatsText = %q, want luminance stats", data.Views[3].StatsText)
	}
	if !strings.Contains(data.Views[0].StatsText, "Sky ") ||
		!strings.Contains(data.Views[0].StatsText, "μ ") ||
		!strings.Contains(data.Views[0].StatsText, "σ ") {
		t.Fatalf("channel StatsText = %q, want sky, mean, and sigma labels", data.Views[0].StatsText)
	}
	if got := data.Views[0].OrigH; got != 2 {
		t.Fatalf("channel preview height = %d, want 2", got)
	}
}

func TestRotateComposeChannel90CWUpdatesPixelsDimensionsAndState(t *testing.T) {
	img := makeLoadedImageForUITest(2, 3, []float32{1, 2, 3, 4, 5, 6})
	rotateComposeChannel90CW(img)
	if img.HDU.Data.Width != 3 || img.HDU.Data.Height != 2 || img.Rotation90 != 1 {
		t.Fatalf("rotation state = %dx%d turns=%d, want 3x2 turns=1", img.HDU.Data.Width, img.HDU.Data.Height, img.Rotation90)
	}
	want := []float32{5, 3, 1, 6, 4, 2}
	for i, value := range want {
		if img.HDU.Data.Pixels[i] != value {
			t.Fatalf("Pixels[%d] = %v, want %v", i, img.HDU.Data.Pixels[i], value)
		}
	}
}

func TestClearComposeChannelAlignment(t *testing.T) {
	img := makeLoadedImageForUITest(1, 1, []float32{1})
	img.HasAlignTransform = true
	img.AlignA, img.AlignB, img.AlignC = 1, 2, 3
	img.AlignD, img.AlignE, img.AlignF = 4, 5, 6

	clearComposeChannelAlignment(img)
	if img.HasAlignTransform || img.AlignA != 0 || img.AlignB != 0 || img.AlignC != 0 || img.AlignD != 0 || img.AlignE != 0 || img.AlignF != 0 {
		t.Fatalf("alignment was not cleared: %+v", img)
	}
}

func TestReplaceComposeChannelImagePreservesRotation(t *testing.T) {
	previous := makeLoadedImageForUITest(2, 3, []float32{1, 2, 3, 4, 5, 6})
	rotateComposeChannel90CW(previous)
	imgs := []*models.LoadedImage{previous}
	replacement := makeLoadedImageForUITest(2, 3, []float32{10, 20, 30, 40, 50, 60})

	replaceComposeChannelImage(imgs, 0, replacement)
	if imgs[0] != replacement || replacement.Rotation90 != 1 {
		t.Fatalf("replacement rotation = %d, want 1", replacement.Rotation90)
	}
	if replacement.HDU.Data.Width != 3 || replacement.HDU.Data.Height != 2 {
		t.Fatalf("replacement size = %dx%d, want 3x2", replacement.HDU.Data.Width, replacement.HDU.Data.Height)
	}
	want := []float32{50, 30, 10, 60, 40, 20}
	for i, value := range want {
		if replacement.HDU.Data.Pixels[i] != value {
			t.Fatalf("Pixels[%d] = %v, want %v", i, replacement.HDU.Data.Pixels[i], value)
		}
	}
}

func TestBuildComposePreviewDataSharedHistogramScaleRebinsChannels(t *testing.T) {
	levels := defaultRGBLevels()
	imgs := []*models.LoadedImage{
		makeLoadedImageForUITest(1, 3, []float32{0, 0, 0.05}),
		makeLoadedImageForUITest(1, 3, []float32{0.5, 0.55, 0.55}),
		makeLoadedImageForUITest(1, 3, []float32{0.95, 1, 1}),
	}

	data := buildComposePreviewData(context.Background(), imgs, true, true, levels, nil)

	for i := 0; i < 3; i++ {
		if data.Views[i].HistMax != 2 {
			t.Fatalf("Views[%d].HistMax = %d, want shared max 2", i, data.Views[i].HistMax)
		}
	}
	if data.Views[0].Bins[255] != 0 {
		t.Fatalf("blue high value stayed in per-channel max bin; got bin255=%d, want 0", data.Views[0].Bins[255])
	}
	if data.Views[2].Bins[255] != 2 {
		t.Fatalf("red top shared bin = %d, want 2", data.Views[2].Bins[255])
	}
}

func TestBuildComposePreviewDataSkipsCompositeWhenDisabled(t *testing.T) {
	levels := defaultRGBLevels()
	imgs := []*models.LoadedImage{
		makeLoadedImageForUITest(1, 1, []float32{0.1}),
		makeLoadedImageForUITest(1, 1, []float32{0.2}),
		makeLoadedImageForUITest(1, 1, []float32{0.3}),
	}
	called := false

	data := buildComposePreviewData(context.Background(), imgs, false, false, levels, func(context.Context) ([]byte, int, int, [3]histogram.Stats, error) {
		called = true
		return nil, 0, 0, [3]histogram.Stats{}, nil
	})

	if called {
		t.Fatal("composeRGB should not be called when composite preview is disabled")
	}
	if data.Views[3].OrigW != 0 || data.Views[3].OrigH != 0 {
		t.Fatalf("disabled composite size = %dx%d, want 0x0", data.Views[3].OrigW, data.Views[3].OrigH)
	}
	if data.Views[3].StatsText != "Composite: off" {
		t.Fatalf("disabled composite StatsText = %q, want Composite: off", data.Views[3].StatsText)
	}
}

func TestComposeBlinkPairExcludesSelectedFilter(t *testing.T) {
	tests := []struct {
		excluded int
		wantA    int
		wantB    int
	}{
		{excluded: 0, wantA: 1, wantB: 2},
		{excluded: 1, wantA: 0, wantB: 2},
		{excluded: 2, wantA: 0, wantB: 1},
		{excluded: 99, wantA: 1, wantB: 2},
	}
	for _, tt := range tests {
		gotA, gotB := composeBlinkPair(tt.excluded)
		if gotA != tt.wantA || gotB != tt.wantB {
			t.Fatalf("composeBlinkPair(%d) = (%d,%d), want (%d,%d)", tt.excluded, gotA, gotB, tt.wantA, tt.wantB)
		}
	}
}

func TestComposePixelValueAtReturnsRawChannelValue(t *testing.T) {
	img := makeLoadedImageForUITest(3, 2, []float32{
		1, 2, 3,
		4, 5, 6,
	})

	got, ok := composePixelValueAt(img, imagePoint{X: 1, Y: 1})
	if !ok || got != 5 {
		t.Fatalf("composePixelValueAt = (%v,%v), want (5,true)", got, ok)
	}
	if _, ok := composePixelValueAt(img, imagePoint{X: 3, Y: 0}); ok {
		t.Fatal("composePixelValueAt out-of-bounds ok = true, want false")
	}
}

func TestComposeRegionMedianAtIgnoresSinglePixelOutlier(t *testing.T) {
	// 5x5 uniform background with one hot pixel at the click point. A single-pixel
	// read returns the outlier; the region median must reject it so the picked
	// level is stable regardless of exactly which pixel is clicked.
	pixels := make([]float32, 25)
	for i := range pixels {
		pixels[i] = 10
	}
	pixels[2*5+2] = 1000 // hot pixel at (2,2)
	img := makeLoadedImageForUITest(5, 5, pixels)

	if got, ok := composePixelValueAt(img, imagePoint{X: 2, Y: 2}); !ok || got != 1000 {
		t.Fatalf("composePixelValueAt hot pixel = (%v,%v), want (1000,true)", got, ok)
	}
	got, ok := composeRegionMedianAt(img, imagePoint{X: 2, Y: 2}, composePickRadius)
	if !ok || got != 10 {
		t.Fatalf("composeRegionMedianAt = (%v,%v), want (10,true)", got, ok)
	}

	// Clamping at a corner still returns the median of the in-bounds region.
	if got, ok := composeRegionMedianAt(img, imagePoint{X: 0, Y: 0}, composePickRadius); !ok || got != 10 {
		t.Fatalf("composeRegionMedianAt corner = (%v,%v), want (10,true)", got, ok)
	}
	if _, ok := composeRegionMedianAt(img, imagePoint{X: 5, Y: 0}, composePickRadius); ok {
		t.Fatal("composeRegionMedianAt out-of-bounds ok = true, want false")
	}
}

func TestMatchComposeChannelStretchCopiesModeAndScalesPeak(t *testing.T) {
	refPixels := make([]float32, 101)
	targetPixels := make([]float32, 101)
	for i := range refPixels {
		refPixels[i] = float32(i)
		targetPixels[i] = float32(i * 5)
	}
	ref := makeLoadedImageForUITest(101, 1, refPixels)
	ref.Mode = stretch.Asinh
	ref.Background = 10
	ref.Peak = 80
	ref.Black = 10
	ref.White = 80
	ref.ScaledPeak = 10

	target := makeLoadedImageForUITest(101, 1, targetPixels)
	target.Mode = stretch.Linear
	target.Background = 0
	target.Peak = 1
	target.Black = 0
	target.White = 1
	target.ScaledPeak = 1

	if err := matchComposeChannelStretch(ref, target, false); err != nil {
		t.Fatalf("matchComposeChannelStretch error = %v", err)
	}
	if target.Mode != stretch.Asinh {
		t.Fatalf("target.Mode = %v, want %v", target.Mode, stretch.Asinh)
	}
	if target.ScaledPeak != ref.ScaledPeak {
		t.Fatalf("target.ScaledPeak = %v, want %v", target.ScaledPeak, ref.ScaledPeak)
	}
	if math.Abs(target.Background-50) > 1e-6 || math.Abs(target.Peak-400) > 1e-6 {
		t.Fatalf("target levels = bg %.6f peak %.6f, want 50 and 400", target.Background, target.Peak)
	}
	if target.Black != target.Background || target.White != target.Peak {
		t.Fatalf("black/white = (%v,%v), want paired with background/peak (%v,%v)", target.Black, target.White, target.Background, target.Peak)
	}
}

func TestViewerInteractionLayerUsesCrosshairForPicker(t *testing.T) {
	layer := newViewerInteractionLayer()
	if got := layer.Cursor(); got != desktop.PointerCursor {
		t.Fatalf("default cursor = %v, want pointer", got)
	}
	layer.pickerActive = true
	if got := layer.Cursor(); got != desktop.CrosshairCursor {
		t.Fatalf("picker cursor = %v, want crosshair", got)
	}
}

func makeLoadedImageForUITest(w, h int, pixels []float32) *models.LoadedImage {
	return &models.LoadedImage{
		HDU: fitsio.HDU{
			Data: fitsio.ImageData{
				Width:  w,
				Height: h,
				Pixels: append([]float32(nil), pixels...),
			},
		},
		Mode:       stretch.Linear,
		Black:      0,
		White:      1,
		Background: 0,
		Peak:       1,
		ScaledPeak: 1,
	}
}

type stubNumberField struct {
	widget.Label
	val float64
}

func (s *stubNumberField) SetValue(v float64) { s.val = v }
func (s *stubNumberField) Value() float64     { return s.val }

type stubCheckField struct {
	checked bool
}

func (s *stubCheckField) SetChecked(v bool) { s.checked = v }
