package processing

import (
	"math"
	"testing"

	"gofitsv3/internal/fitsio"
)

func photHeader(values map[string]string) fitsio.Header { return fitsio.Header{Cards: values} }

func TestParseInstrumentPhotometrySupportedCases(t *testing.T) {
	tests := []struct {
		name, telescope, instrument, detector, filter, unit string
		ref                                                 PhotometricReference
		wantKind                                            PhotometricInputKind
		wantGain                                            float64
	}{
		{"hst count rate flambda", "HST", "ACS", "WFC", "F606W", "ELECTRONS/S", ReferenceFlambda, InputCountRate, 1.5e-19},
		{"hst counts fnu", "HST", "WFC3", "IR", "F160W", "COUNTS", ReferenceFnu, InputCounts, 1e-19 * 1.6e4 * 1.6e4 / speedOfLightAngstrom * 1e23 / 10},
		{"jwst mJy sr", "JWST", "NIRCAM", "NRCA1", "F200W", "MJy/sr", ReferenceFnu, InputSurfaceBrightness, 5e5},
		{"calibrated fnu", "JWST", "MIRI", "MIRIMAGE", "F770W", "Jy", ReferenceFlambda, InputFnu, speedOfLightAngstrom / (7700 * 7700) * 1e-23},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := map[string]string{"BUNIT": tc.unit, "PHOTPLAM": "7700", "PHOTFLAM": "1.5e-19", "PHOTMJSR": "2", "PIXAR_SR": "0.5"}
			if tc.filter == "F160W" {
				h["PHOTPLAM"] = "16000"
				h["PHOTFLAM"] = "1e-19"
				h["EXPTIME"] = "10"
			}
			got, err := ParseInstrumentPhotometry(InstrumentMetadata{Telescope: tc.telescope, Instrument: tc.instrument, Detector: tc.detector, Filter: tc.filter, SCI: photHeader(h), Reference: tc.ref})
			if err != nil {
				t.Fatal(err)
			}
			if got.InputKind != tc.wantKind || math.Abs(got.Gain-tc.wantGain) > math.Abs(tc.wantGain)*1e-12 {
				t.Fatalf("got kind=%s gain=%g, want %s %g", got.InputKind, got.Gain, tc.wantKind, tc.wantGain)
			}
		})
	}
}

func TestInstrumentPhotometrySCIOverridesPrimary(t *testing.T) {
	got, err := ParseInstrumentPhotometry(InstrumentMetadata{Telescope: "HST", Instrument: "ACS", Detector: "WFC", Filter: "F606W", Reference: ReferenceFlambda, Primary: photHeader(map[string]string{"BUNIT": "COUNTS/S", "PHOTFLAM": "9e-19", "PHOTPLAM": "6000"}), SCI: photHeader(map[string]string{"BUNIT": "COUNTS/S", "PHOTFLAM": "1e-19", "PHOTPLAM": "6000"})})
	if err != nil {
		t.Fatal(err)
	}
	if got.Gain != 1e-19 {
		t.Fatalf("gain=%g, SCI PHOTFLAM should win", got.Gain)
	}
}

func TestInstrumentPhotometryRejectsInvalidMetadata(t *testing.T) {
	tests := []map[string]string{
		{"PHOTPLAM": "6000", "PHOTFLAM": "1e-19"},
		{"BUNIT": "COUNTS", "PHOTPLAM": "6000", "PHOTFLAM": "1e-19", "EXPTIME": "0"},
		{"BUNIT": "COUNTS/S", "PHOTPLAM": "NaN", "PHOTFLAM": "1e-19"},
		{"BUNIT": "watts", "PHOTPLAM": "6000", "PHOTFLAM": "1e-19"},
	}
	for i, h := range tests {
		if _, err := ParseInstrumentPhotometry(InstrumentMetadata{Telescope: "HST", Instrument: "ACS", Detector: "WFC", Filter: "F606W", SCI: photHeader(h)}); !IsUnsupportedPhotometry(err) {
			t.Errorf("case %d error=%v, want typed unsupported", i, err)
		}
	}
}

func TestInstrumentPhotometryRejectsUnknownAndContradictory(t *testing.T) {
	if _, err := ParseInstrumentPhotometry(InstrumentMetadata{Telescope: "HST", Instrument: "ACS", Detector: "WFC", Filter: "F999W", SCI: photHeader(map[string]string{"BUNIT": "COUNTS/S", "PHOTPLAM": "6000", "PHOTFLAM": "1e-19"})}); !IsUnsupportedPhotometry(err) {
		t.Fatalf("unknown filter error=%v", err)
	}
	_, err := ParseInstrumentPhotometry(InstrumentMetadata{Telescope: "HST", Instrument: "ACS", Detector: "WFC", Filter: "F606W", SCI: photHeader(map[string]string{"BUNIT": "COUNTS/S", "PHOTPLAM": "6000", "PHOTFLAM": "1e-19", "PHOTFNU": "9e-20"})})
	if !IsUnsupportedPhotometry(err) {
		t.Fatalf("contradictory calibration error=%v", err)
	}
}

func TestInstrumentPhotometryIdentityAndJWSTCountRejections(t *testing.T) {
	base := map[string]string{"BUNIT": "ELECTRONS/S", "PHOTPLAM": "6000", "PHOTFLAM": "1e-19"}
	for _, detector := range []string{"", "IR"} {
		if _, err := ParseInstrumentPhotometry(InstrumentMetadata{Telescope: "HST", Instrument: "ACS", Detector: detector, Filter: "F606W", SCI: photHeader(base)}); !IsUnsupportedPhotometry(err) {
			t.Fatalf("detector %q should be rejected", detector)
		}
	}
	if _, err := ParseInstrumentPhotometry(InstrumentMetadata{Telescope: "JWST", Instrument: "NIRCAM", Detector: "NRCA1", Filter: "F200W", SCI: photHeader(base)}); !IsUnsupportedPhotometry(err) {
		t.Fatal("JWST count rate should be rejected")
	}
	flam := map[string]string{"BUNIT": "FLAM", "PHOTPLAM": "20000"}
	if _, err := ParseInstrumentPhotometry(InstrumentMetadata{Telescope: "JWST", Instrument: "NIRCAM", Detector: "NRCA1", Filter: "F200W", SCI: photHeader(flam)}); !IsUnsupportedPhotometry(err) {
		t.Fatal("JWST Flambda should be rejected")
	}
}

func TestInstrumentPhotometrySurfaceBrightnessFlambdaAndPIXAR(t *testing.T) {
	h := map[string]string{"BUNIT": "MJy/sr", "PHOTPLAM": "20000", "PIXAR_SR": "0.5"}
	got, err := ParseInstrumentPhotometry(InstrumentMetadata{Telescope: "JWST", Instrument: "NIRCAM", Detector: "NRCA1", Filter: "F200W", Reference: ReferenceFlambda, SCI: photHeader(h)})
	if err != nil {
		t.Fatal(err)
	}
	want := 0.5 * 1e6 * speedOfLightAngstrom / (20000 * 20000) * 1e-23
	if math.Abs(got.Gain-want) > want*1e-12 {
		t.Fatalf("gain=%g want %g", got.Gain, want)
	}
	for _, pixar := range []string{"", "NaN", "0"} {
		bad := map[string]string{"BUNIT": "MJy/sr", "PHOTPLAM": "20000"}
		if pixar != "" {
			bad["PIXAR_SR"] = pixar
		}
		if _, err := ParseInstrumentPhotometry(InstrumentMetadata{Telescope: "JWST", Instrument: "NIRCAM", Detector: "NRCA1", Filter: "F200W", SCI: photHeader(bad)}); !IsUnsupportedPhotometry(err) {
			t.Errorf("PIXAR_SR=%q should be rejected", pixar)
		}
	}
}

func TestInstrumentPhotometryCountsContradictoryCalibration(t *testing.T) {
	h := map[string]string{"BUNIT": "COUNTS", "EXPTIME": "10", "PHOTPLAM": "6000", "PHOTFLAM": "1e-19", "PHOTFNU": "9e-20"}
	if _, err := ParseInstrumentPhotometry(InstrumentMetadata{Telescope: "HST", Instrument: "ACS", Detector: "WFC", Filter: "F606W", SCI: photHeader(h)}); !IsUnsupportedPhotometry(err) {
		t.Fatal("contradictory count calibration should be rejected")
	}
}
