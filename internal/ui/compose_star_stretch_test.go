package ui

import "testing"

func TestParseStarStretchPreviewOptions(t *testing.T) {
	got, err := parseStarStretchPreviewOptions("0.35", "8", "")
	if err != nil {
		t.Fatalf("valid options returned error: %v", err)
	}
	if got.Strength != 0.35 || got.Limit != 8 || got.SourceID != 0 {
		t.Fatalf("options = %+v", got)
	}
	got, err = parseStarStretchPreviewOptions("1", "24", "17")
	if err != nil || got.SourceID != 17 {
		t.Fatalf("upper-bound options = %+v, err=%v", got, err)
	}
}

func TestParseStarStretchPreviewOptionsRejectsOutOfRangeValues(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    string
		l    string
		id   string
	}{
		{"negative strength", "-0.1", "8", ""},
		{"strength over one", "1.1", "8", ""},
		{"bad strength", "x", "8", ""},
		{"nan strength", "NaN", "8", ""},
		{"infinite strength", "+Inf", "8", ""},
		{"zero limit", "0.3", "0", ""},
		{"limit over max", "0.3", "25", ""},
		{"bad limit", "0.3", "x", ""},
		{"negative source", "0.3", "8", "-1"},
		{"bad source", "0.3", "8", "x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseStarStretchPreviewOptions(tc.s, tc.l, tc.id); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
