package processing

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"gofitsv3/internal/catalog/gaia"
	"gofitsv3/internal/models"
)

func TestGaiaCalibrationRejectsInvalidSyntheticPhotometryInputs(t *testing.T) {
	if _, err := IntegrateXPSpectrum(gaia.XPSpectrum{Wavelengths: []float64{500, 400}, Flux: []float64{1, 1}}, GaiaPassband{MinWavelengthNm: 400, MaxWavelengthNm: 600}); err == nil {
		t.Fatal("descending XP grid accepted")
	}
	if _, err := IntegrateXPSpectrum(gaia.XPSpectrum{Wavelengths: []float64{400, 500}, Flux: []float64{1, math.NaN()}}, GaiaPassband{MinWavelengthNm: 400, MaxWavelengthNm: 600}); err == nil {
		t.Fatal("non-finite XP flux accepted")
	}
	if _, err := IntegrateXPSpectrum(gaia.XPSpectrum{Wavelengths: []float64{400, 500}, Flux: []float64{1, 1}}, GaiaPassband{MinWavelengthNm: 400, MaxWavelengthNm: 600, Throughput: []float64{0, -1}, ThroughputWavelengths: []float64{400, 500}}); err == nil {
		t.Fatal("negative throughput accepted")
	}
	if _, err := IntegrateXPSpectrum(gaia.XPSpectrum{Wavelengths: []float64{400, 500}, Flux: []float64{1, 1}}, GaiaPassband{MinWavelengthNm: 400, MaxWavelengthNm: 600, Throughput: []float64{0, 1}, ThroughputWavelengths: []float64{500, 400}}); err == nil {
		t.Fatal("non-monotonic passband grid accepted")
	}
}

func TestGaiaCalibrationRejectsMoreThanThreePassbands(t *testing.T) {
	s := GaiaCalibrationSettings{Release: "DR3", XPRepresentation: "xp-v1", AlgorithmVersion: "test", Passbands: []string{"G", "BP", "RP", "F435W"}, MagnitudeLimit: 18, MatchRadiusArcsec: 2, ObservationEpoch: 2024}
	if err := s.Validate(); err == nil {
		t.Fatal("more than three passbands accepted")
	}
}

type fakeGaiaProvider struct {
	sources                         []gaia.Source
	spectra                         []gaia.XPSpectrum
	reverse                         bool
	requestedIDs                    *[]uint64
	provErr, sourceErr, spectrumErr error
}

func (f fakeGaiaProvider) Provenance(context.Context) (gaia.Provenance, error) {
	if f.provErr != nil {
		return gaia.Provenance{}, f.provErr
	}
	return gaia.Provenance{Release: "DR3", XPRepresentation: "xp-v1", Provider: "fake", ProviderVersion: "1"}, nil
}
func (f fakeGaiaProvider) Mode() gaia.AccessMode { return gaia.AccessCacheOnly }
func (f fakeGaiaProvider) DiscoverSources(context.Context, gaia.FieldQuery) ([]gaia.Source, error) {
	if f.sourceErr != nil {
		return nil, f.sourceErr
	}
	out := append([]gaia.Source(nil), f.sources...)
	if f.reverse {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out, nil
}
func (f fakeGaiaProvider) RetrieveXPSpectra(_ context.Context, _ string, _ string, ids []uint64) ([]gaia.XPSpectrum, error) {
	if f.spectrumErr != nil {
		return nil, f.spectrumErr
	}
	if f.requestedIDs != nil {
		*f.requestedIDs = append((*f.requestedIDs)[:0], ids...)
	}
	wanted := make(map[uint64]struct{}, len(ids))
	for _, id := range ids {
		wanted[id] = struct{}{}
	}
	out := make([]gaia.XPSpectrum, 0, len(f.spectra))
	for _, spectrum := range f.spectra {
		if _, ok := wanted[spectrum.SourceID]; ok {
			out = append(out, spectrum)
		}
	}
	if f.reverse {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out, nil
}

func TestPrepareGaiaCalibrationMapsProviderCancellation(t *testing.T) {
	settings := GaiaCalibrationSettings{Release: "DR3", XPRepresentation: "xp-v1", Passbands: []string{"F606W"}, AlgorithmVersion: "v1", MagnitudeLimit: 18, MatchRadiusArcsec: 2, ObservationEpoch: 2024}
	query := gaia.FieldQuery{Footprint: gaia.Footprint{Center: gaia.Coordinate{RA: 10, Dec: 1}, RadiusDeg: 1}}
	validSource := gaia.Source{Release: "DR3", SourceID: 1, RA: 10, Dec: 1, ReferenceEpoch: 2016, PositionError: .1, ProperMotionErrorRA: .1, ProperMotionErrorDec: .1, G: 12, BP: 12, RP: 12, GError: .1, BPError: .1, RPError: .1}
	for _, provider := range []fakeGaiaProvider{{provErr: context.Canceled}, {sourceErr: context.Canceled}, {spectrumErr: context.Canceled, sources: []gaia.Source{validSource}}} {
		got, err := PrepareGaiaCalibration(context.Background(), GaiaCalibrationRequest{Provider: provider, Query: query, Settings: settings, Measurements: []GaiaChannelMeasurement{{SourceID: 1, Flux: [3]float64{1, 1, 1}, SNR: 10}}})
		if !errors.Is(err, context.Canceled) || got.Status != models.CalibrationCancelled {
			t.Fatalf("status=%v err=%v", got.Status, err)
		}
	}
}

func TestPrepareGaiaCalibrationReportsNoDetectedStars(t *testing.T) {
	provider := fakeGaiaProvider{sources: []gaia.Source{step12Source(1, 10, 1)}}
	_, err := PrepareGaiaCalibration(context.Background(), GaiaCalibrationRequest{
		Provider: provider, Query: step12Query(), Settings: step12Settings(),
	})
	if err == nil || !strings.Contains(err.Error(), "no detected stars") {
		t.Fatalf("error=%v, want no-detected-stars diagnostic", err)
	}
}

func TestPrepareGaiaCalibrationRejectsUnsortedHandoffSources(t *testing.T) {
	sources := []gaia.Source{step12Source(2, 10, 1), step12Source(1, 10, 1)}
	_, err := PrepareGaiaCalibration(context.Background(), GaiaCalibrationRequest{
		Provider: fakeGaiaProvider{sourceErr: context.Canceled}, Query: step12Query(), Settings: step12Settings(), Sources: sources,
	})
	if err == nil || !strings.Contains(err.Error(), "normalized and sorted") {
		t.Fatalf("error=%v, want normalized-source rejection without discovery", err)
	}
}

func TestPrepareGaiaCalibrationReportsNoGaiaCrossmatches(t *testing.T) {
	provider := fakeGaiaProvider{sources: []gaia.Source{step12Source(1, 10, 1)}}
	_, err := PrepareGaiaCalibration(context.Background(), GaiaCalibrationRequest{
		Provider: provider, Query: step12Query(), Settings: step12Settings(),
		DetectedStars: []Star{{X: 4, Y: 5}}, PixelToSky: func(float64, float64) (gaia.Coordinate, error) {
			return gaia.Coordinate{RA: 30, Dec: 30}, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "no Gaia crossmatches") {
		t.Fatalf("error=%v, want no-crossmatches diagnostic", err)
	}
}

func TestPrepareGaiaCalibrationIsProviderOrderIndependent(t *testing.T) {
	source := func(id uint64) gaia.Source {
		return gaia.Source{Release: "DR3", SourceID: id, RA: 10, Dec: 1, ReferenceEpoch: 2016, PositionError: .1, ProperMotionErrorRA: .1, ProperMotionErrorDec: .1, G: 12, BP: 12.2, RP: 11.8, GError: .1, BPError: .1, RPError: .1}
	}
	xp := func(id uint64) gaia.XPSpectrum {
		return gaia.XPSpectrum{Release: "DR3", SourceID: id, RepresentationVersion: "xp-v1", Wavelengths: []float64{400, 500, 600}, Flux: []float64{1, 2 + float64(id), 1}, FluxErrors: []float64{.1, .1, .1}}
	}
	source3 := source(3)
	settings := GaiaCalibrationSettings{Release: "DR3", XPRepresentation: "xp-v1", Passbands: []string{"F606W", "F435W"}, AlgorithmVersion: "gaia-contract-v1", MagnitudeLimit: 18, MatchRadiusArcsec: 2, ObservationEpoch: 2024}
	query := gaia.FieldQuery{Footprint: gaia.Footprint{Center: gaia.Coordinate{RA: 10, Dec: 1}, RadiusDeg: 1}}
	measurements := []GaiaChannelMeasurement{{SourceID: 1, Flux: [3]float64{1, 1, 1}, SNR: 100}, {SourceID: 2, Flux: [3]float64{1, 1, 1}, SNR: 100}, {SourceID: 3, Flux: [3]float64{1, 1, 1}, SNR: 100}}
	a, err := PrepareGaiaCalibration(context.Background(), GaiaCalibrationRequest{Provider: fakeGaiaProvider{sources: []gaia.Source{source3, source(2), source(1)}, spectra: []gaia.XPSpectrum{xp(3), xp(2), xp(1)}}, Query: query, Settings: settings, Measurements: measurements})
	if err != nil {
		t.Fatal(err)
	}
	b, err := PrepareGaiaCalibration(context.Background(), GaiaCalibrationRequest{Provider: fakeGaiaProvider{sources: []gaia.Source{source(1), source(2), source3}, spectra: []gaia.XPSpectrum{xp(1), xp(2), xp(3)}, reverse: true}, Query: query, Settings: settings, Measurements: measurements})
	if err != nil {
		t.Fatal(err)
	}
	if a.SourceFingerprint != b.SourceFingerprint || a.SettingsFingerprint != b.SettingsFingerprint || a.Diagnostics.SourceIDs[0] != 1 {
		t.Fatalf("order affected result: %+v vs %+v", a, b)
	}
}

func TestGaiaSettingsRejectUnsupportedPassbandAndFingerprintVersion(t *testing.T) {
	s := GaiaCalibrationSettings{Release: "DR3", XPRepresentation: "xp-v1", Passbands: []string{"not-a-passband"}, AlgorithmVersion: "v1", MagnitudeLimit: 18, MatchRadiusArcsec: 2, ObservationEpoch: 2024}
	if err := s.Validate(); err == nil {
		t.Fatal("unsupported passband accepted")
	}
	s.Passbands = []string{"F606W"}
	a := GaiaSettingsFingerprint(s)
	s.XPRepresentation = "xp-v2"
	if a == GaiaSettingsFingerprint(s) {
		t.Fatal("XP version did not change fingerprint")
	}
}

func TestGaiaSourceFingerprintIncludesPhotometryFlagsAndXPUncertainties(t *testing.T) {
	s := gaia.Source{Release: "DR3", SourceID: 1, RA: 10, Dec: 1, ReferenceEpoch: 2016, PositionError: .1, ProperMotionErrorRA: .1, ProperMotionErrorDec: .1, G: 12, BP: 12, RP: 12, GError: .1, BPError: .1, RPError: .1}
	x := gaia.XPSpectrum{Release: "DR3", SourceID: 1, RepresentationVersion: "xp-v1", CalibrationVersion: "cal-v1", Wavelengths: []float64{400, 500}, Flux: []float64{1, 2}, FluxErrors: []float64{.1, .1}}
	base := GaiaSourceFingerprint([]gaia.Source{s}, []gaia.XPSpectrum{x})
	mutations := []func(){func() { s.G += .1 }, func() { s.QualityFlags = []string{"q"} }, func() { x.FluxErrors[0] += .1 }, func() { x.CalibrationVersion = "cal-v2" }}
	for _, mutate := range mutations {
		mutate()
		if GaiaSourceFingerprint([]gaia.Source{s}, []gaia.XPSpectrum{x}) == base {
			t.Fatal("source mutation did not change fingerprint")
		}
		s.G = 12
		s.QualityFlags = nil
		x.FluxErrors[0] = .1
		x.CalibrationVersion = "cal-v1"
	}
}

func TestPrepareGaiaCalibrationRejectsIncompleteSpectrumSets(t *testing.T) {
	settings := GaiaCalibrationSettings{Release: "DR3", XPRepresentation: "xp-v1", Passbands: []string{"F606W"}, AlgorithmVersion: "v1", MagnitudeLimit: 18, MatchRadiusArcsec: 2, ObservationEpoch: 2024}
	query := gaia.FieldQuery{Footprint: gaia.Footprint{Center: gaia.Coordinate{RA: 10, Dec: 1}, RadiusDeg: 1}}
	source := gaia.Source{Release: "DR3", SourceID: 1, RA: 10, Dec: 1, ReferenceEpoch: 2016, PositionError: .1, ProperMotionErrorRA: .1, ProperMotionErrorDec: .1, G: 12, BP: 12, RP: 12, GError: .1, BPError: .1, RPError: .1}
	xp := gaia.XPSpectrum{Release: "DR3", SourceID: 1, RepresentationVersion: "xp-v1", Wavelengths: []float64{400, 500}, Flux: []float64{1, 2}, FluxErrors: []float64{.1, .1}}
	source2 := source
	source2.SourceID = 2
	cases := []fakeGaiaProvider{{}, {sources: []gaia.Source{source}}, {sources: []gaia.Source{source, source2}, spectra: []gaia.XPSpectrum{xp}}, {sources: []gaia.Source{source}, spectra: []gaia.XPSpectrum{{Release: "DR3", SourceID: 2, RepresentationVersion: "xp-v1", Wavelengths: []float64{400, 500}, Flux: []float64{1, 2}, FluxErrors: []float64{.1, .1}}}}, {sources: []gaia.Source{source}, spectra: []gaia.XPSpectrum{xp, xp}}}
	for i, provider := range cases {
		got, err := PrepareGaiaCalibration(context.Background(), GaiaCalibrationRequest{Provider: provider, Query: query, Settings: settings})
		if err == nil || got.Status != models.CalibrationUnsupported {
			t.Fatalf("case %d status=%v err=%v", i, got.Status, err)
		}
	}
}

func TestPrepareGaiaCalibrationRetrievesOnlyMeasuredSources(t *testing.T) {
	var requested []uint64
	sources := []gaia.Source{step12Source(1, 10, 1), step12Source(2, 10, 1), step12Source(3, 10, 1), step12Source(4, 10, 1)}
	spectra := []gaia.XPSpectrum{step12XP(1, 1), step12XP(2, 2), step12XP(3, 3), step12XP(4, 4)}
	measurements := []GaiaChannelMeasurement{
		{SourceID: 1, Flux: [3]float64{1, 1, 1}, SNR: 20},
		{SourceID: 2, Flux: [3]float64{1, 1, 1}, SNR: 20},
		{SourceID: 3, Flux: [3]float64{1, 1, 1}, SNR: 20},
	}
	provider := fakeGaiaProvider{sources: sources, spectra: spectra, requestedIDs: &requested}
	result, err := PrepareGaiaCalibration(context.Background(), GaiaCalibrationRequest{Provider: provider, Query: step12Query(), Settings: step12Settings(), Measurements: measurements})
	if err != nil || result.Status != models.CalibrationValid {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(requested) != 3 || requested[0] != 1 || requested[1] != 2 || requested[2] != 3 {
		t.Fatalf("requested IDs=%v", requested)
	}
	if len(result.Provenance.SourceIDs) != 3 || result.Provenance.SourceIDs[0] != 1 || result.Provenance.SourceIDs[2] != 3 {
		t.Fatalf("provenance IDs=%v", result.Provenance.SourceIDs)
	}
}

func TestPrepareGaiaCalibrationFailsWhenSelectedSpectrumMissing(t *testing.T) {
	sources := []gaia.Source{step12Source(1, 10, 1), step12Source(2, 10, 1), step12Source(3, 10, 1)}
	measurements := []GaiaChannelMeasurement{{SourceID: 1, Flux: [3]float64{1, 1, 1}, SNR: 20}, {SourceID: 2, Flux: [3]float64{1, 1, 1}, SNR: 20}, {SourceID: 3, Flux: [3]float64{1, 1, 1}, SNR: 20}}
	result, err := PrepareGaiaCalibration(context.Background(), GaiaCalibrationRequest{Provider: fakeGaiaProvider{sources: sources, spectra: []gaia.XPSpectrum{step12XP(1, 1), step12XP(2, 2)}}, Query: step12Query(), Settings: step12Settings(), Measurements: measurements})
	if err == nil || result.Status != models.CalibrationUnsupported {
		t.Fatalf("status=%v err=%v", result.Status, err)
	}
}

func TestPrepareGaiaCalibrationExcludesUnusableMeasurementsFromPrefetch(t *testing.T) {
	var requested []uint64
	sources := []gaia.Source{step12Source(1, 10, 1), step12Source(2, 10, 1), step12Source(3, 10, 1), step12Source(4, 10, 1), step12Source(5, 10, 1), step12Source(6, 10, 1)}
	spectra := []gaia.XPSpectrum{step12XP(1, 1), step12XP(2, 2), step12XP(3, 3), step12XP(4, 4), step12XP(5, 5), step12XP(6, 6)}
	measurements := []GaiaChannelMeasurement{{SourceID: 1, Flux: [3]float64{1, 1, 1}, SNR: 20}, {SourceID: 2, Flux: [3]float64{1, 1, 1}, SNR: 2}, {SourceID: 3, Flux: [3]float64{1, 0, 1}, SNR: 20}, {SourceID: 4, Flux: [3]float64{1, 1, 1}, SNR: 20, Saturated: true}, {SourceID: 5, Flux: [3]float64{1, 1, 1}, SNR: 20}, {SourceID: 6, Flux: [3]float64{1, 1, 1}, SNR: 20}}
	provider := fakeGaiaProvider{sources: sources, spectra: spectra, requestedIDs: &requested}
	result, err := PrepareGaiaCalibration(context.Background(), GaiaCalibrationRequest{Provider: provider, Query: step12Query(), Settings: step12Settings(), Measurements: measurements})
	if err != nil || result.Status != models.CalibrationValid {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(requested) != 3 || requested[0] != 1 || requested[1] != 5 || requested[2] != 6 {
		t.Fatalf("requested IDs=%v", requested)
	}
	if result.Diagnostics.RejectedByReason["low_snr"] != 1 || result.Diagnostics.RejectedByReason["invalid_flux"] != 1 || result.Diagnostics.RejectedByReason["saturation"] != 1 {
		t.Fatalf("diagnostics=%+v", result.Diagnostics.RejectedByReason)
	}
}

func TestFitGaiaGainsRejectsDuplicateMeasurementIDs(t *testing.T) {
	expected := map[uint64][3]float64{1: {1, 2, 3}, 2: {2, 3, 4}, 3: {3, 4, 5}}
	measurements := []GaiaChannelMeasurement{{SourceID: 1, Flux: [3]float64{1, 1, 1}, SNR: 20}, {SourceID: 1, Flux: [3]float64{1, 1, 1}, SNR: 20}, {SourceID: 2, Flux: [3]float64{1, 1, 1}, SNR: 20}}
	result, err := FitGaiaGains(context.Background(), measurements, expected, 3)
	if err == nil || result.Status != models.CalibrationUnsupported || result.Diagnostics.RejectedByReason["duplicate"] != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestPrepareGaiaCalibrationRejectsDuplicateMeasurementsBeforePrefetch(t *testing.T) {
	var requested []uint64
	provider := fakeGaiaProvider{
		sources:      []gaia.Source{step12Source(1, 10, 1), step12Source(2, 10, 1), step12Source(3, 10, 1)},
		spectra:      []gaia.XPSpectrum{step12XP(1, 1), step12XP(2, 2), step12XP(3, 3)},
		requestedIDs: &requested,
	}
	measurements := []GaiaChannelMeasurement{{SourceID: 1, Flux: [3]float64{1, 1, 1}, SNR: 20}, {SourceID: 1, Flux: [3]float64{1, 1, 1}, SNR: 20}, {SourceID: 2, Flux: [3]float64{1, 1, 1}, SNR: 20}}
	result, err := PrepareGaiaCalibration(context.Background(), GaiaCalibrationRequest{Provider: provider, Query: step12Query(), Settings: step12Settings(), Measurements: measurements})
	if err == nil || result.Status != models.CalibrationUnsupported {
		t.Fatalf("status=%v err=%v", result.Status, err)
	}
	if len(requested) != 0 {
		t.Fatalf("duplicate measurements triggered spectrum request: %v", requested)
	}
}

func TestGaiaCalibrationFingerprintIncludesProviderIdentity(t *testing.T) {
	settings := step12Settings()
	a := GaiaCalibrationFingerprint(settings, gaia.Provenance{Provider: "a", ProviderVersion: "1", EndpointSemantics: "x"})
	b := GaiaCalibrationFingerprint(settings, gaia.Provenance{Provider: "b", ProviderVersion: "1", EndpointSemantics: "x"})
	c := GaiaCalibrationFingerprint(settings, gaia.Provenance{Provider: "a", ProviderVersion: "1", EndpointSemantics: "y"})
	if a == b || a == c {
		t.Fatalf("provider provenance did not alter fingerprint: %q %q %q", a, b, c)
	}
}

func TestGaiaFingerprintSeparatesFlagDomainsAndListBoundaries(t *testing.T) {
	base := gaia.Source{Release: "DR3", SourceID: 1, RA: 10, Dec: 1, ReferenceEpoch: 2016, PositionError: .1, ProperMotionErrorRA: .1, ProperMotionErrorDec: .1, G: 12, BP: 12, RP: 12, GError: .1, BPError: .1, RPError: .1}
	a := base
	a.QualityFlags = []string{"a"}
	a.VariabilityFlags = []string{"b"}
	b := base
	b.QualityFlags = []string{"a", "b"}
	if GaiaSourceFingerprint([]gaia.Source{a}, nil) == GaiaSourceFingerprint([]gaia.Source{b}, nil) {
		t.Fatal("flag domains/list boundaries collide")
	}
}
