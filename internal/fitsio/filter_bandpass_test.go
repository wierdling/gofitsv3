package fitsio

import "testing"

func bandHeader(values map[string]string) Header { return Header{Cards: values} }

func TestResolveFilterBandpassPhotplamPrecedenceAndConversion(t *testing.T) {
	primary := bandHeader(map[string]string{"FILTER": "'F445W'", "PHOTPLAM": "5500 / Angstroms"})
	selected := bandHeader(map[string]string{"FILTER": "'F600W'", "PHOTPLAM": "6000"})
	got := ResolveFilterBandpass(primary, selected)
	if got.Name != "F600W" || got.WavelengthNm != 550 || got.Origin != "PHOTPLAM" {
		t.Fatalf("combined result = %#v, want selected F600W/primary 550/PHOTPLAM", got)
	}
	got = ResolveFilterBandpass(bandHeader(map[string]string{"FILTER": "'F600W'"}), selected)
	if got.WavelengthNm != 600 || got.Origin != "selected-HDU PHOTPLAM" {
		t.Fatalf("selected result = %#v, want 600 nm selected PHOTPLAM", got)
	}
	got = ResolveFilterBandpass(bandHeader(map[string]string{"FILTER": "'F150W2'", "TELESCOP": "'JWST'", "INSTRUME": "'NIRCam'"}), bandHeader(map[string]string{"PUPIL": "'F162M'", "TELESCOP": "'JWST'", "INSTRUME": "'NIRCam'"}))
	if got.Name != "F162M" || got.WavelengthNm != 1620 || got.Class != FilterBandMedium {
		t.Fatalf("selected-HDU PUPIL result = %#v, want F162M/1620/medium", got)
	}
}

func TestResolveFilterBandpassRejectsInvalidPhotplam(t *testing.T) {
	for _, value := range []string{"bad", "NaN", "Inf", "0", "-10"} {
		got := ResolveFilterBandpass(bandHeader(map[string]string{"FILTER": "'F445W'", "PHOTPLAM": value, "TELESCOP": "'HST'", "INSTRUME": "'ACS'"}), Header{})
		if got.WavelengthNm != 445 || got.UnresolvedReason != "" {
			t.Fatalf("PHOTPLAM %q result = %#v, want name-derived 445 nm", value, got)
		}
	}
	if got := ResolveFilterBandpass(bandHeader(map[string]string{"PHOTPLAM": "NaN"}), Header{}); got.UnresolvedReason == "" {
		t.Fatal("missing filter name with invalid PHOTPLAM should explain manual correction")
	}
	if _, ok := validPhotplam(bandHeader(map[string]string{"PHOTPLAM": "NaN"})); ok {
		t.Fatal("invalid PHOTPLAM unexpectedly accepted")
	}
}

func TestResolveFilterBandpassMissionConventions(t *testing.T) {
	tests := []struct {
		name, telescope, instrument string
		want                        float64
		class                       FilterBandClass
	}{
		{"F162M", "JWST", "NIRCam", 1620, FilterBandMedium},
		{"F770W", "JWST", "MIRI", 7700, FilterBandWide},
		{"F445W", "HST", "ACS", 445, FilterBandWide},
		{"F550W", "HST", "ACS", 550, FilterBandWide},
		{"F600W", "HST", "ACS", 600, FilterBandWide},
		{"F160W", "HST", "WFC3/IR", 1600, FilterBandWide},
	}
	for _, test := range tests {
		header := bandHeader(map[string]string{"FILTER": "'" + test.name + "'", "TELESCOP": "'" + test.telescope + "'", "INSTRUME": "'" + test.instrument + "'"})
		got := ResolveFilterBandpass(header, Header{})
		if got.WavelengthNm != test.want || got.Class != test.class {
			t.Errorf("%s result = %#v, want %.0f nm/%s", test.name, got, test.want, test.class)
		}
	}
	got := ResolveFilterBandpass(bandHeader(map[string]string{"PUPIL": "'F162M'", "FILTER": "'F150W2'", "TELESCOP": "'JWST'", "INSTRUME": "'NIRCam'"}), Header{})
	if got.Name != "F162M" || got.WavelengthNm != 1620 {
		t.Fatalf("PUPIL result = %#v, want F162M/1620", got)
	}
}

func TestResolveFilterBandpassClassesCaseAndLongpass(t *testing.T) {
	for _, test := range []struct {
		name  string
		class FilterBandClass
	}{
		{"f100w", FilterBandWide}, {"F100W2", FilterBandWide}, {"F100M", FilterBandMedium},
		{"F100N", FilterBandNarrow}, {"F100L", FilterBandLongpass}, {"F100LP", FilterBandLongpass},
	} {
		got := ResolveFilterBandpass(bandHeader(map[string]string{"FILTER": "'" + test.name + "'", "TELESCOP": "'JWST'"}), Header{})
		if got.Class != test.class {
			t.Errorf("%s class = %s, want %s", test.name, got.Class, test.class)
		}
		if test.name == "f100w" && got.WavelengthNm != 1000 {
			t.Errorf("%s wavelength = %.0f, want 1000 nm", test.name, got.WavelengthNm)
		}
		if test.class == FilterBandLongpass && got.WavelengthNm != 0 || test.class == FilterBandLongpass && got.UnresolvedReason == "" {
			t.Errorf("%s long-pass result = %#v, want unresolved wavelength", test.name, got)
		}
	}
}

func TestResolveFilterBandpassUnknownAndMalformed(t *testing.T) {
	for _, name := range []string{"F445W", "not-a-filter", "F123X", "F12"} {
		got := ResolveFilterBandpass(bandHeader(map[string]string{"FILTER": "'" + name + "'", "TELESCOP": "'UNKNOWN'"}), Header{})
		if got.WavelengthNm != 0 || got.UnresolvedReason == "" {
			t.Errorf("%s result = %#v, want actionable unresolved result", name, got)
		}
	}
	got := ResolveFilterBandpass(bandHeader(map[string]string{"FILTER": "'F445W'", "TELESCOP": "'UNKNOWN'", "DETECTOR": "'IR'"}), Header{})
	if got.WavelengthNm != 0 || got.UnresolvedReason == "" {
		t.Fatalf("unknown instrument with IR detector = %#v, want unresolved", got)
	}
}
