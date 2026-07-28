package processing

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"gofitsv3/internal/catalog/gaia"
	"gofitsv3/internal/debuglog"
	"gofitsv3/internal/models"
)

func step12Source(id uint64, ra, dec float64) gaia.Source {
	return gaia.Source{Release: "DR3", SourceID: id, RA: ra, Dec: dec, ReferenceEpoch: 2016, PositionError: .1, ProperMotionErrorRA: .1, ProperMotionErrorDec: .1, G: 12, BP: 12.2, RP: 11.8, GError: .1, BPError: .1, RPError: .1}
}

func step12XP(id uint64, blue float64) gaia.XPSpectrum {
	return gaia.XPSpectrum{Release: "DR3", SourceID: id, RepresentationVersion: "xp-v1", Wavelengths: []float64{400, 500, 600, 800}, Flux: []float64{1, blue, 1, 1}, FluxErrors: []float64{.1, .1, .1, .1}}
}

func step12Settings() GaiaCalibrationSettings {
	return GaiaCalibrationSettings{Release: "DR3", XPRepresentation: "xp-v1", Passbands: []string{"F435W", "F606W", "F814W"}, AlgorithmVersion: "step12-test", MagnitudeLimit: 18, MatchRadiusArcsec: 2, ObservationEpoch: 2024}
}

func step12Query() gaia.FieldQuery {
	return gaia.FieldQuery{Footprint: gaia.Footprint{Center: gaia.Coordinate{RA: 10, Dec: 1}, RadiusDeg: 1}}
}

func TestGaiaDebugLogReportsCrossmatchSummary(t *testing.T) {
	var lines []string
	debuglog.SetHandler(func(line string) { lines = append(lines, line) })
	defer debuglog.SetHandler(nil)
	s := step12Source(1, 10, 1)
	if _, err := CrossmatchGaiaStarsWithWCS([]Star{{X: 0, Y: 0}}, []gaia.Source{s}, 2, 2016, func(float64, float64) (gaia.Coordinate, error) {
		return gaia.Coordinate{RA: 10, Dec: 1}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "Gaia WCS crossmatch") || !strings.Contains(lines[0], "matched=1") || !strings.Contains(lines[0], "matched_distance_samples") || !strings.Contains(lines[0], "nearest_catalog_samples") {
		t.Fatalf("unexpected Gaia debug log: %v", lines)
	}
}

func TestGaiaDebugLogReportsNearestCatalogDistanceOnMiss(t *testing.T) {
	var lines []string
	debuglog.SetHandler(func(line string) { lines = append(lines, line) })
	defer debuglog.SetHandler(nil)
	s := step12Source(1, 10+3.0/3600, 1)
	if matches, err := CrossmatchGaiaStarsWithWCS([]Star{{X: 0, Y: 0}}, []gaia.Source{s}, 2, 2016, func(float64, float64) (gaia.Coordinate, error) {
		return gaia.Coordinate{RA: 10, Dec: 1}, nil
	}); err != nil {
		t.Fatal(err)
	} else if len(matches) != 0 {
		t.Fatalf("matches = %d, want zero", len(matches))
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "nearest_catalog_samples=[") || strings.Contains(lines[0], "nearest_catalog_samples=[]") {
		t.Fatalf("nearest miss distance missing from Gaia debug log: %v", lines)
	}
}

func TestMeasureApertureAnnulusSubtractsBackgroundAndFlagsSaturation(t *testing.T) {
	const w, h = 21, 21
	p := make([]float32, w*h)
	for i := range p {
		p[i] = 10
	}
	for y := 7; y <= 13; y++ {
		for x := 7; x <= 13; x++ {
			if math.Hypot(float64(x-10), float64(y-10)) <= 2 {
				p[y*w+x] += 5
			}
		}
	}
	p[10*w+10] = 100
	f, err := MeasureApertureAnnulus(p, w, h, 10, 10, 2, 4, 6, 90)
	if err != nil {
		t.Fatal(err)
	}
	if !f.Saturated || f.Background != 10 || f.Flux <= 0 || f.SNR <= 0 {
		t.Fatalf("bad aperture result: %+v", f)
	}
}

func TestSyntheticPhotometryUsesThroughputSamples(t *testing.T) {
	s := gaia.XPSpectrum{Wavelengths: []float64{400, 500, 600}, Flux: []float64{1, 1, 1}}
	p := GaiaPassband{MinWavelengthNm: 400, MaxWavelengthNm: 600, Throughput: []float64{0, 1, 0}, ThroughputWavelengths: []float64{400, 500, 600}}
	v, err := SyntheticPhotometry(s, p)
	if err != nil || math.Abs(v-0.5) > 1e-9 {
		t.Fatalf("v=%v err=%v", v, err)
	}
}

func TestFitGaiaGainsRejectsOutlierAndNormalizesScale(t *testing.T) {
	expected := map[uint64][3]float64{}
	ms := make([]GaiaChannelMeasurement, 0, 5)
	for i := uint64(1); i <= 5; i++ {
		expected[i] = [3]float64{10, 20 + float64(i), 30}
		ms = append(ms, GaiaChannelMeasurement{SourceID: i, Flux: [3]float64{5, (20 + float64(i)) / 2, 15}, SNR: 20})
	}
	ms[4].Flux = [3]float64{50, 1, 15}
	r, err := FitGaiaGains(context.Background(), ms, expected, 3)
	if err != nil {
		t.Fatal(err)
	}
	if r.Diagnostics.AcceptedStars != 4 || r.Diagnostics.RejectedByReason["outlier"] != 1 {
		t.Fatalf("diagnostics=%+v", r.Diagnostics)
	}
	for _, g := range r.Diagnostics.Gains {
		if math.Abs(g-1) > 1e-9 {
			t.Fatalf("gains=%v", r.Diagnostics.Gains)
		}
	}
}

func TestFitGaiaGainsMasksMultipleOutliersToStableSet(t *testing.T) {
	expected := map[uint64][3]float64{}
	ms := make([]GaiaChannelMeasurement, 0, 8)
	for i := uint64(1); i <= 8; i++ {
		expected[i] = [3]float64{10, 20 + float64(i), 30}
		ms = append(ms, GaiaChannelMeasurement{SourceID: i, Flux: [3]float64{5, (20 + float64(i)) / 2, 15}, SNR: 20})
	}
	ms[6].Flux = [3]float64{100, 1, 15}
	ms[7].Flux = [3]float64{1, 200, 1}
	r, err := FitGaiaGains(context.Background(), ms, expected, 3)
	if err != nil || r.Diagnostics.AcceptedStars != 6 || r.Diagnostics.RejectedByReason["outlier"] != 2 {
		t.Fatalf("result=%+v err=%v", r.Diagnostics, err)
	}
}

func TestPrepareGaiaCalibrationEndToEndApertureWCS(t *testing.T) {
	sources := []gaia.Source{step12Source(1, 10+5.0/3600, 1+5.0/3600), step12Source(2, 10+15.0/3600, 1+5.0/3600), step12Source(3, 10+5.0/3600, 1+15.0/3600), step12Source(4, 10+25.0/3600, 1+25.0/3600)}
	spectra := []gaia.XPSpectrum{step12XP(1, 1), step12XP(2, 2), step12XP(3, 4), step12XP(4, 8)}
	var requested []uint64
	const w, h = 32, 32
	planes := [3]GaiaPlane{}
	for c := range planes {
		planes[c] = GaiaPlane{Pixels: make([]float32, w*h), Width: w, Height: h}
		for i := range planes[c].Pixels {
			planes[c].Pixels[i] = 1
		}
		for _, p := range [][2]int{{5, 5}, {15, 5}, {5, 15}} {
			planes[c].Pixels[p[1]*w+p[0]] = 20
		}
	}
	stars := []Star{{X: 5, Y: 5}, {X: 15, Y: 5}, {X: 5, Y: 15}}
	toSky := func(x, y float64) (gaia.Coordinate, error) {
		return gaia.Coordinate{RA: 10 + x/3600, Dec: 1 + y/3600}, nil
	}
	got, err := PrepareGaiaCalibration(context.Background(), GaiaCalibrationRequest{Provider: fakeGaiaProvider{sources: sources, spectra: spectra, requestedIDs: &requested}, Query: step12Query(), Settings: step12Settings(), DetectedStars: stars, Planes: planes, PixelToSky: toSky, Aperture: GaiaApertureConfig{Radius: 1, AnnulusInner: 2, AnnulusOuter: 3, Saturation: 100}})
	if err != nil || got.Status != models.CalibrationValid || got.Diagnostics.AcceptedStars != 3 {
		t.Fatalf("result=%+v err=%v", got, err)
	}
	if len(requested) != 3 || requested[0] != 1 || requested[1] != 2 || requested[2] != 3 {
		t.Fatalf("requested IDs=%v", requested)
	}
}

func TestPrepareGaiaCalibrationWithoutMeasurementsIsUnsupported(t *testing.T) {
	s := step12Source(1, 10, 1)
	x := step12XP(1, 2)
	got, err := PrepareGaiaCalibration(context.Background(), GaiaCalibrationRequest{Provider: fakeGaiaProvider{sources: []gaia.Source{s}, spectra: []gaia.XPSpectrum{x}}, Query: step12Query(), Settings: step12Settings()})
	if err == nil || got.Status != models.CalibrationUnsupported {
		t.Fatalf("status=%v err=%v", got.Status, err)
	}
}

func TestCrossmatchWCSProperMotionRotationAndAmbiguity(t *testing.T) {
	s := step12Source(1, 10, 1)
	s.ProperMotionRA = 3600
	s.ProperMotionDec = 1800
	got, err := CrossmatchGaiaStarsWithWCS([]Star{{X: 1, Y: 2}}, []gaia.Source{s}, 2, 2024, func(float64, float64) (gaia.Coordinate, error) { return PropagateGaiaPosition(s, 2024), nil })
	if err != nil || len(got) != 1 {
		t.Fatalf("proper motion match=%v err=%v", got, err)
	}
	rot, err := CrossmatchGaiaStarsWithWCS([]Star{{X: 1, Y: 0}, {X: 0, Y: 1}}, []gaia.Source{step12Source(2, 10+1.0/3600, 1+1.0/3600), step12Source(3, 10+1.0/3600, 1-1.0/3600)}, 2, 2016, func(x, y float64) (gaia.Coordinate, error) {
		return gaia.Coordinate{RA: 10 + (x+y)/3600, Dec: 1 + (x-y)/3600}, nil
	})
	if err != nil || len(rot) != 2 {
		t.Fatalf("rotation match=%v err=%v", rot, err)
	}
	amb, err := CrossmatchGaiaStarsWithWCS([]Star{{X: 0, Y: 0}}, []gaia.Source{step12Source(4, 10, 1), step12Source(5, 10+0.00001, 1)}, 2, 2016, func(float64, float64) (gaia.Coordinate, error) { return gaia.Coordinate{RA: 10, Dec: 1}, nil })
	if err != nil || len(amb) != 0 {
		t.Fatalf("ambiguous match=%v err=%v", amb, err)
	}
}

func TestFitGaiaGainsRejectsGeneratedQualityAndInsufficientColor(t *testing.T) {
	e := map[uint64][3]float64{1: {1, 2, 3}, 2: {1, 3, 2}, 3: {1, 4, 1}}
	m := []GaiaChannelMeasurement{{SourceID: 1, Flux: [3]float64{1, 1, 1}, SNR: 10, Variable: true}, {SourceID: 2, Flux: [3]float64{1, 1, 1}, SNR: 10, Contaminated: true}, {SourceID: 3, Flux: [3]float64{1, 1, 1}, SNR: 10, Blended: true}}
	if got, err := FitGaiaGains(context.Background(), m, e, 3); err == nil || got.Diagnostics.RejectedByReason["quality"] != 3 {
		t.Fatalf("quality got=%+v err=%v", got, err)
	}
	flat := map[uint64][3]float64{1: {1, 1, 1}, 2: {2, 2, 2}, 3: {3, 3, 3}}
	clean := []GaiaChannelMeasurement{{SourceID: 1, Flux: [3]float64{1, 1, 1}, SNR: 10}, {SourceID: 2, Flux: [3]float64{1, 1, 1}, SNR: 10}, {SourceID: 3, Flux: [3]float64{1, 1, 1}, SNR: 10}}
	if _, err := FitGaiaGains(context.Background(), clean, flat, 3); err == nil {
		t.Fatal("insufficient color span accepted")
	}
}

func TestStep12PassbandGoldenAndOverlayCoverage(t *testing.T) {
	p, _ := GaiaPassbandByName("F435W")
	v, err := SyntheticPhotometry(gaia.XPSpectrum{Wavelengths: []float64{380, 435, 500}, Flux: []float64{2, 2, 2}}, p)
	if err != nil || math.Abs(v-1) > 1e-9 {
		t.Fatalf("golden=%v err=%v", v, err)
	}
	if _, err := GaiaOverlayScalar("F435W", gaia.XPSpectrum{Wavelengths: []float64{400, 450}, Flux: []float64{1, 1}}, 1); err == nil {
		t.Fatal("incomplete overlay coverage accepted")
	}
}

func TestPrepareGaiaCalibrationCancellationLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := step12Source(1, 10, 1)
	got, err := PrepareGaiaCalibration(ctx, GaiaCalibrationRequest{Provider: fakeGaiaProvider{sources: []gaia.Source{s}, spectra: []gaia.XPSpectrum{step12XP(1, 2)}}, Query: step12Query(), Settings: step12Settings()})
	if !errors.Is(err, context.Canceled) || got.Status != models.CalibrationCancelled {
		t.Fatalf("status=%v err=%v", got.Status, err)
	}
}

func TestPrepareGaiaCalibrationQualityDiagnosticsPreservePipelineCounts(t *testing.T) {
	sources := []gaia.Source{step12Source(1, 10, 1), step12Source(2, 10, 1), step12Source(3, 10, 1), step12Source(4, 10, 1)}
	spectra := []gaia.XPSpectrum{step12XP(1, 1), step12XP(2, 2), step12XP(3, 3), step12XP(4, 4)}
	ms := []GaiaChannelMeasurement{{SourceID: 1, Flux: [3]float64{1, 1, 1}, SNR: 20}, {SourceID: 2, Flux: [3]float64{1, 1, 1}, SNR: 20}, {SourceID: 3, Flux: [3]float64{1, 1, 1}, SNR: 20}, {SourceID: 4, Flux: [3]float64{1, 1, 1}, SNR: 20, Variable: true}}
	r, err := PrepareGaiaCalibration(context.Background(), GaiaCalibrationRequest{Provider: fakeGaiaProvider{sources: sources, spectra: spectra}, Query: step12Query(), Settings: step12Settings(), Measurements: ms})
	if err != nil || r.Diagnostics.DetectedStars != 4 || r.Diagnostics.MatchedStars != 4 || r.Diagnostics.AcceptedStars != 3 || r.Diagnostics.RejectedByReason["variable"] != 1 {
		t.Fatalf("result=%+v err=%v", r.Diagnostics, err)
	}
}

func TestPrepareGaiaCalibrationCancellationDuringLocalApertureLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sources := []gaia.Source{step12Source(1, 10+1.0/3600, 1+1.0/3600), step12Source(2, 10+2.0/3600, 1+2.0/3600), step12Source(3, 10+3.0/3600, 1+3.0/3600)}
	spectra := []gaia.XPSpectrum{step12XP(1, 1), step12XP(2, 2), step12XP(3, 3)}
	planes := [3]GaiaPlane{}
	for c := range planes {
		planes[c] = GaiaPlane{Pixels: make([]float32, 32*32), Width: 32, Height: 32}
	}
	calls := 0
	toSky := func(x, y float64) (gaia.Coordinate, error) {
		calls++
		if calls == 1 {
			cancel()
		}
		return gaia.Coordinate{RA: 10 + x/3600, Dec: 1 + y/3600}, nil
	}
	stars := []Star{{X: 1, Y: 1}, {X: 2, Y: 2}, {X: 3, Y: 3}}
	r, err := PrepareGaiaCalibration(ctx, GaiaCalibrationRequest{Provider: fakeGaiaProvider{sources: sources, spectra: spectra}, Query: step12Query(), Settings: step12Settings(), DetectedStars: stars, PixelToSky: toSky, Planes: planes, Aperture: GaiaApertureConfig{Radius: 1, AnnulusInner: 2, AnnulusOuter: 3, Saturation: 100}})
	if !errors.Is(err, context.Canceled) || r.Status != models.CalibrationCancelled {
		t.Fatalf("status=%v err=%v", r.Status, err)
	}
	if calls != 1 {
		t.Fatalf("cancellation did not stop matching promptly: projection calls=%d", calls)
	}
}
