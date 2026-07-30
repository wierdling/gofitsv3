package processing

import (
	"context"
	"math"
	"testing"
	"time"

	"gofitsv3/internal/models"
)

type cancelAfterChecks struct {
	done   chan struct{}
	checks int
}

func (c *cancelAfterChecks) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *cancelAfterChecks) Done() <-chan struct{} {
	c.checks++
	if c.checks >= 7 {
		select {
		case <-c.done:
		default:
			close(c.done)
		}
	}
	return c.done
}
func (c *cancelAfterChecks) Err() error {
	select {
	case <-c.done:
		return context.Canceled
	default:
		return nil
	}
}
func (c *cancelAfterChecks) Value(any) any { return nil }

func TestEstimateBackgroundRejectsSourcesAndPreservesInput(t *testing.T) {
	p := make([]float32, 64)
	for i := range p {
		p[i] = 10
	}
	p[0], p[1], p[2] = 1000, float32(math.NaN()), float32(math.Inf(1))
	before := append([]float32(nil), p...)
	r, err := EstimateBackgroundPlane(context.Background(), BackgroundPlane{Pixels: p, Width: 8, Height: 8, StarMask: []bool{true}}, BackgroundConfig{MinSamples: 8, TileSize: 4})
	if err != nil || r.Status != models.CalibrationValid || math.Abs(r.Transform.Offset-10) > 1e-6 {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	for i := range p {
		if math.Float32bits(p[i]) != math.Float32bits(before[i]) {
			t.Fatal("input mutated")
		}
	}
}

func TestEstimateBackgroundRefusesGradientAndSupportsROI(t *testing.T) {
	p := make([]float32, 100)
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			p[y*10+x] = float32(x)
		}
	}
	r, err := EstimateBackgroundPlane(context.Background(), BackgroundPlane{Pixels: p, Width: 10, Height: 10}, BackgroundConfig{MinSamples: 8, TileSize: 2, TileUniformityThreshold: .1})
	if err != nil || r.Status != models.CalibrationUnsupported || r.RejectionReason == "" {
		t.Fatalf("gradient result=%+v err=%v", r, err)
	}
	for i := range p {
		p[i] = 7
	}
	r, err = EstimateBackgroundPlane(context.Background(), BackgroundPlane{Pixels: p, Width: 10, Height: 10, ROI: &models.CalibrationROI{X: 2, Y: 2, Width: 4, Height: 4}}, BackgroundConfig{MinSamples: 8, TileSize: 2})
	if err != nil || r.Status != models.CalibrationValid || r.Transform.Offset != 7 {
		t.Fatalf("roi result=%+v err=%v", r, err)
	}
}

func TestEstimateBackgroundCancellationAndInsufficient(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := EstimateBackgroundPlane(ctx, BackgroundPlane{Pixels: make([]float32, 100), Width: 10, Height: 10}, BackgroundConfig{MinSamples: 8})
	if r.Status != models.CalibrationCancelled || err == nil {
		t.Fatalf("cancel result=%+v err=%v", r, err)
	}
	r, err = EstimateBackgroundPlane(context.Background(), BackgroundPlane{Pixels: []float32{1, 2}, Width: 2, Height: 1}, BackgroundConfig{MinSamples: 8})
	if err != nil || r.Status != models.CalibrationUnsupported || r.Accepted != 0 {
		t.Fatalf("insufficient result=%+v err=%v", r, err)
	}
}

func TestStreamingCalibrationFingerprintMatchesCanonical(t *testing.T) {
	p := []float32{1, 2, 3, 4, 5, 6}
	valid := []bool{true, false, true, true, true, false}
	row := func(y int, dst []float32) error { copy(dst, p[y*3:(y+1)*3]); return nil }
	vrow := func(y int, dst []bool) error { copy(dst, valid[y*3:(y+1)*3]); return nil }
	in := CalibrationInput{SourceIdentity: "x", Width: 3, Height: 2, Pixels: p, Valid: valid, Alignment: "a", Background: models.LinearTransform{Offset: 2, Gain: 1}}
	settings := CalibrationSettings{NeutralizeBackground: true}
	want, err := CalculateCalibration([]CalibrationInput{in}, settings)
	if err != nil {
		t.Fatal(err)
	}
	got, err := CalculateCalibrationStreaming(context.Background(), []CalibrationStreamInput{{SourceIdentity: in.SourceIdentity, Width: 3, Height: 2, ReadRow: row, ReadValidRow: vrow, Alignment: in.Alignment, Background: in.Background}}, settings)
	if err != nil {
		t.Fatal(err)
	}
	if got.SourceFingerprint != want.SourceFingerprint {
		t.Fatalf("stream fingerprint %s, canonical %s", got.SourceFingerprint, want.SourceFingerprint)
	}
}

func TestEstimateBackgroundStreamHonorsValidAndROI(t *testing.T) {
	p := []float32{10, 10, 100, 10, 10, 10, 100, 10, 10, 10, 10, 10}
	valid := make([]bool, len(p))
	for i := range valid {
		valid[i] = true
	}
	valid[2] = false
	valid[6] = false
	in := CalibrationStreamInput{Width: 4, Height: 3, ReadRow: func(y int, dst []float32) error { copy(dst, p[y*4:(y+1)*4]); return nil }, ReadValidRow: func(y int, dst []bool) error { copy(dst, valid[y*4:(y+1)*4]); return nil }}
	r, err := EstimateBackgroundStream(context.Background(), in, &models.CalibrationROI{X: 0, Y: 0, Width: 4, Height: 3}, BackgroundConfig{MinSamples: 4, TileSize: 2})
	if err != nil || r.Status != models.CalibrationValid || r.Transform.Offset != 10 {
		t.Fatalf("estimate=%+v err=%v", r, err)
	}
}

func TestEstimateBackgroundUsesAcceptedSamplesForTiles(t *testing.T) {
	p := make([]float32, 64)
	for i := range p {
		p[i] = 10
	}
	p[0] = 10000 // unmasked source; zero-MAD clipping must remove it
	r, err := EstimateBackgroundPlane(context.Background(), BackgroundPlane{Pixels: p, Width: 8, Height: 8}, BackgroundConfig{MinSamples: 8, TileSize: 4})
	if err != nil || r.Status != models.CalibrationValid || r.Rejected == 0 || r.TileSpread != 0 {
		t.Fatalf("result=%+v err=%v", r, err)
	}
}

func TestEstimateBackgroundGradientThresholdBoundaryAndDeterminism(t *testing.T) {
	p := make([]float32, 64)
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			p[y*8+x] = float32(100 + x)
		}
	}
	accept, err := EstimateBackgroundPlane(context.Background(), BackgroundPlane{Pixels: p, Width: 8, Height: 8}, BackgroundConfig{MinSamples: 8, TileSize: 4, TileUniformityThreshold: .04})
	if err != nil || accept.Status != models.CalibrationValid {
		t.Fatalf("boundary accept=%+v err=%v", accept, err)
	}
	refuse, err := EstimateBackgroundPlane(context.Background(), BackgroundPlane{Pixels: p, Width: 8, Height: 8}, BackgroundConfig{MinSamples: 8, TileSize: 4, TileUniformityThreshold: .03})
	if err != nil || refuse.Status != models.CalibrationUnsupported {
		t.Fatalf("boundary refuse=%+v err=%v", refuse, err)
	}
	again, err := EstimateBackgroundPlane(context.Background(), BackgroundPlane{Pixels: p, Width: 8, Height: 8}, BackgroundConfig{MinSamples: 8, TileSize: 4, TileUniformityThreshold: .04})
	if err != nil || accept.Transform != again.Transform || accept.TileSpread != again.TileSpread {
		t.Fatalf("non-deterministic: %+v vs %+v", accept, again)
	}
}

func TestEstimateBackgroundShortMaskAndPartialROI(t *testing.T) {
	p := make([]float32, 36)
	for i := range p {
		p[i] = 3
	}
	valid := []bool{false, true} // safely treats unspecified entries as invalid
	r, err := EstimateBackgroundPlane(context.Background(), BackgroundPlane{Pixels: p, Width: 6, Height: 6, Valid: valid, ROI: &models.CalibrationROI{X: 4, Y: 4, Width: 4, Height: 4}}, BackgroundConfig{MinSamples: 2, TileSize: 2})
	if err != nil || r.Status != models.CalibrationUnsupported || r.RejectionReason == "" {
		t.Fatalf("short mask/ROI result=%+v err=%v", r, err)
	}
}

func TestEstimateBackgroundROIChangesOffsetAndRejectsInvalidROI(t *testing.T) {
	p := make([]float32, 8*4)
	for y := 0; y < 4; y++ {
		for x := 0; x < 8; x++ {
			if x < 4 {
				p[y*8+x] = 10
			} else {
				p[y*8+x] = 20
			}
		}
	}
	left, err := EstimateBackgroundPlane(context.Background(), BackgroundPlane{Pixels: p, Width: 8, Height: 4, ROI: &models.CalibrationROI{X: 0, Y: 0, Width: 4, Height: 4}}, BackgroundConfig{MinSamples: 4, TileSize: 2})
	if err != nil || left.Status != models.CalibrationValid || left.Transform.Offset != 10 {
		t.Fatalf("left ROI = %+v, err=%v", left, err)
	}
	right, err := EstimateBackgroundPlane(context.Background(), BackgroundPlane{Pixels: p, Width: 8, Height: 4, ROI: &models.CalibrationROI{X: 4, Y: 0, Width: 4, Height: 4}}, BackgroundConfig{MinSamples: 4, TileSize: 2})
	if err != nil || right.Status != models.CalibrationValid || right.Transform.Offset != 20 {
		t.Fatalf("right ROI = %+v, err=%v", right, err)
	}
	invalid, err := EstimateBackgroundPlane(context.Background(), BackgroundPlane{Pixels: p, Width: 8, Height: 4, ROI: &models.CalibrationROI{X: 0, Y: 0, Width: 0, Height: 4}}, BackgroundConfig{MinSamples: 4, TileSize: 2})
	if err != nil || invalid.Status != models.CalibrationUnsupported || invalid.RejectionReason == "" {
		t.Fatalf("invalid ROI = %+v, err=%v", invalid, err)
	}
}

func TestEstimateBackgroundIgnoresInvalidAlignedFillMargins(t *testing.T) {
	plane := BackgroundPlane{Pixels: []float32{10, 10, 10, 10, 10, 10, 0, 0}, Valid: []bool{true, true, true, true, true, true, false, false}, Width: 8, Height: 1}
	got, err := EstimateBackgroundPlane(context.Background(), plane, BackgroundConfig{MinSamples: 2, TileSize: 4})
	if err != nil || got.Status != models.CalibrationValid || got.Transform.Offset != 10 {
		t.Fatalf("aligned fill affected background: %+v, err=%v", got, err)
	}
}

func TestEstimateBackgroundCancellationDuringZeroDispersionClipping(t *testing.T) {
	p := make([]float32, 4096)
	for i := range p {
		p[i] = 10
	}
	p[len(p)-1] = 1000
	ctx := &cancelAfterChecks{done: make(chan struct{})}
	r, err := EstimateBackgroundPlane(ctx, BackgroundPlane{Pixels: p, Width: 4096, Height: 1}, BackgroundConfig{MinSamples: 32, TileSize: 1_000_000})
	if r.Status != models.CalibrationCancelled || err != context.Canceled {
		t.Fatalf("result=%+v err=%v", r, err)
	}
}

func TestApplyLinearTransformOffsetBeforeGainWithoutMutation(t *testing.T) {
	got, err := ApplyLinearTransform(2, models.LinearTransform{Offset: 5, Gain: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got != -6 {
		t.Fatalf("got %v, want -6", got)
	}
	if _, err := ApplyLinearTransform(1, models.LinearTransform{Offset: 0, Gain: 0}); err == nil {
		t.Fatal("zero gain accepted")
	}
	if _, err := ApplyLinearTransform(1, models.LinearTransform{Offset: math.Inf(1), Gain: 1}); err == nil {
		t.Fatal("infinite offset accepted")
	}
	if _, err := ApplyLinearTransform(1, models.LinearTransform{Gain: math.MaxFloat64}); err == nil {
		t.Fatal("float32 overflow accepted")
	}
}

func TestCopyCalibrationResultDetachesDiagnosticsAndOverlays(t *testing.T) {
	in := CalibrationResult{
		Status:      models.CalibrationValid,
		Overlays:    []models.OverlayCalibrationState{{Mode: models.OverlayCalibratedLinear, Strength: 1}},
		Diagnostics: models.CalibrationDiagnostics{Warnings: []string{"warning"}},
	}
	out := CopyCalibrationResult(in)
	out.Overlays[0].Strength = 2
	out.Diagnostics.Warnings[0] = "changed"
	if in.Overlays[0].Strength != 1 || in.Diagnostics.Warnings[0] != "warning" {
		t.Fatalf("copy shares mutable state: in=%+v out=%+v", in, out)
	}
}

func TestEffectiveOverlayLegacyDefaultsToArtisticDisabled(t *testing.T) {
	got := EffectiveOverlay(models.OverlayCalibrationState{})
	if got.Mode != models.OverlayArtistic || got.Status != models.CalibrationDisabled {
		t.Fatalf("got %+v", got)
	}
}

func TestNormalizeCalibrationGainsGeometricMean(t *testing.T) {
	got, err := NormalizeCalibrationGains([3]float64{2, 8, 2})
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range [3]float64{0.6299605249474366, 2.5198420997897464, 0.6299605249474366} {
		if math.Abs(got[i]-want) > 1e-12 {
			t.Fatalf("gain[%d]=%v want %v", i, got[i], want)
		}
	}
}

func TestCalculateCalibrationOrderingAndFingerprints(t *testing.T) {
	inputs := []CalibrationInput{
		{SourceIdentity: "r", Width: 1, Height: 1, Pixels: []float32{1}, Background: models.LinearTransform{Offset: 2, Gain: 1}, Photometry: &InstrumentPhotometry{Gain: 2}},
		{SourceIdentity: "g", Width: 1, Height: 1, Pixels: []float32{2}, Background: models.LinearTransform{Offset: 1, Gain: 1}, Photometry: &InstrumentPhotometry{Gain: 8}},
		{SourceIdentity: "b", Width: 1, Height: 1, Pixels: []float32{3}, Background: models.LinearTransform{Offset: 0, Gain: 1}, Photometry: &InstrumentPhotometry{Gain: 2}},
	}
	settings := CalibrationSettings{NeutralizeBackground: true, AlgorithmVersion: "v1"}
	result, err := CalculateCalibration(inputs, settings)
	if err != nil {
		t.Fatal(err)
	}
	if result.Base[0].Offset != 2 || result.Base[1].Offset != 1 {
		t.Fatalf("offset ordering lost: %+v", result.Base)
	}
	if result.SourceFingerprint == "" || result.SettingsFingerprint == "" {
		t.Fatal("missing fingerprints")
	}
	if CalibrationResultStale(result, inputs, settings) {
		t.Fatal("equal inputs reported stale")
	}
	inputs[0].Pixels[0] = math.Float32frombits(math.Float32bits(inputs[0].Pixels[0]) + 1)
	if !CalibrationResultStale(result, inputs, settings) {
		t.Fatal("pixel change did not stale result")
	}
}

func TestCalibrationFingerprintsIgnoreMapOrderingAndUIState(t *testing.T) {
	a := CalibrationInput{SourceIdentity: "same", Width: 1, Height: 1, Pixels: []float32{1}}
	b := a
	settingsA := CalibrationSettings{AlgorithmVersion: "v1", Overlays: []models.OverlayCalibrationState{{Mode: models.OverlayArtistic, Strength: 1}}}
	settingsB := settingsA
	fa, sa := CalibrationFingerprints([]CalibrationInput{a}, settingsA)
	fb, sb := CalibrationFingerprints([]CalibrationInput{b}, settingsB)
	if fa != fb || sa != sb {
		t.Fatal("equivalent fingerprints differ")
	}
	settingsB.RuntimeRevision = "new-runtime"
	if _, changed := CalibrationFingerprints([]CalibrationInput{b}, settingsB); changed == sb {
		t.Fatal("runtime revision not fingerprinted")
	}
}

func TestCalculateCalibrationRejectsInvalidPhotometry(t *testing.T) {
	_, err := CalculateCalibration([]CalibrationInput{{Width: 1, Height: 1, Pixels: []float32{1}, Photometry: &InstrumentPhotometry{Gain: math.NaN()}}}, CalibrationSettings{})
	if err == nil {
		t.Fatal("NaN gain accepted")
	}
}

func TestCalibrationRejectsNonFiniteSettingsAndExtraChannels(t *testing.T) {
	_, err := CalculateCalibration([]CalibrationInput{{Width: 1, Height: 1, Pixels: []float32{1}}}, CalibrationSettings{LinkedStretch: models.CalibrationStretchSettings{Black: math.Inf(1)}})
	if err == nil {
		t.Fatal("infinite stretch setting accepted")
	}
	inputs := make([]CalibrationInput, 4)
	for i := range inputs {
		inputs[i] = CalibrationInput{Width: 1, Height: 1, Pixels: []float32{1}}
	}
	_, err = CalculateCalibration(inputs, CalibrationSettings{})
	if err == nil {
		t.Fatal("extra base channel accepted")
	}
}

func TestCalibrationStarMaskChangesFingerprint(t *testing.T) {
	in := CalibrationInput{SourceIdentity: "x", Width: 1, Height: 1, Pixels: []float32{1}, StarMask: []bool{false}}
	settings := CalibrationSettings{}
	a, _ := CalculateCalibration([]CalibrationInput{in}, settings)
	in.StarMask[0] = true
	if !CalibrationResultStale(a, []CalibrationInput{in}, settings) {
		t.Fatal("star mask change did not stale result")
	}
}

func TestCalibrationMaskFieldsAreLengthDelimited(t *testing.T) {
	settings := CalibrationSettings{}
	a := CalibrationInput{Width: 1, Height: 1, Pixels: []float32{1}, Valid: []bool{true}, StarMask: []bool{false}}
	b := CalibrationInput{Width: 1, Height: 1, Pixels: []float32{1}, Valid: []bool{true, false}}
	fa, _ := CalibrationFingerprints([]CalibrationInput{a}, settings)
	fb, _ := CalibrationFingerprints([]CalibrationInput{b}, settings)
	if fa == fb {
		t.Fatal("different mask fields collided in fingerprint")
	}
}

func TestCalibrationFewerChannelsUseIdentityForUnavailable(t *testing.T) {
	result, err := CalculateCalibration([]CalibrationInput{{Width: 1, Height: 1, Pixels: []float32{1}, Photometry: &InstrumentPhotometry{Gain: 4}}}, CalibrationSettings{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Base[1].Gain != 1 || result.Base[2].Gain != 1 {
		t.Fatalf("unavailable channels not identity: %+v", result.Base)
	}
}
