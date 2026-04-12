package utils

import (
	"reflect"
	"testing"

	"gofitsv3/internal/fitsio"
)

func TestClamp01(t *testing.T) {
	tests := []struct {
		input float64
		want  float64
	}{
		{input: -1, want: 0},
		{input: 0.4, want: 0.4},
		{input: 2, want: 1},
	}

	for _, tt := range tests {
		if got := Clamp01(tt.input); got != tt.want {
			t.Fatalf("Clamp01(%v) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestParseFloat(t *testing.T) {
	got, err := ParseFloat("  3.25 \n")
	if err != nil {
		t.Fatalf("ParseFloat returned error: %v", err)
	}
	if got != 3.25 {
		t.Fatalf("ParseFloat = %v, want 3.25", got)
	}

	if _, err := ParseFloat("not-a-number"); err == nil {
		t.Fatal("expected ParseFloat to reject invalid input")
	}
}

func TestClampLevel(t *testing.T) {
	tests := []struct {
		input float64
		want  float64
	}{
		{input: -10, want: 0},
		{input: 128.5, want: 128.5},
		{input: 999, want: 255},
	}

	for _, tt := range tests {
		if got := ClampLevel(tt.input); got != tt.want {
			t.Fatalf("ClampLevel(%v) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestSortedKeys(t *testing.T) {
	got := sortedKeys(map[string]string{"B": "2", "A": "1", "C": "3"})
	want := []string{"A", "B", "C"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sortedKeys = %v, want %v", got, want)
	}
}

func TestFormatHeadersLinesSortsCardsAndSeparatesSections(t *testing.T) {
	primary := fitsio.Header{Cards: map[string]string{"BKEY": "2", "AKEY": "1"}}
	sci := fitsio.Header{Cards: map[string]string{"EXTNAME": "'SCI'", "BITPIX": "-32"}}

	got := FormatHeadersLines(primary, sci)
	want := []string{
		"Primary header",
		"AKEY     = 1",
		"BKEY     = 2",
		"",
		"SCI header",
		"BITPIX   = -32",
		"EXTNAME  = 'SCI'",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FormatHeadersLines = %v, want %v", got, want)
	}
}
