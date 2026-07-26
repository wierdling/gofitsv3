package ui

import (
	"reflect"
	"testing"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
)

func examineTestImage(extver string) *models.LoadedImage {
	h := fitsio.Header{Cards: map[string]string{}}
	if extver != "" {
		h.Cards["EXTVER"] = "'" + extver + "'"
	}
	return &models.LoadedImage{HDU: fitsio.HDU{Header: h}}
}

func TestExamineChipLabels(t *testing.T) {
	images := []*models.LoadedImage{examineTestImage("1"), examineTestImage("2"), examineTestImage("")}
	want := []string{"SCI 1 (EXTVER 1)", "SCI 2 (EXTVER 2)", "SCI 3 (EXTVER 3)"}
	if got := examineChipLabels(images); !reflect.DeepEqual(got, want) {
		t.Fatalf("labels = %#v, want %#v", got, want)
	}
}

func TestExamineChipIndexPrefersEXTVERAndFallsBackSafely(t *testing.T) {
	images := []*models.LoadedImage{examineTestImage("2"), examineTestImage("4")}
	tests := []struct {
		name     string
		extver   string
		fallback int
		want     int
	}{
		{"matching extver", "4", 0, 1},
		{"missing extver uses valid fallback", "3", 0, 0},
		{"invalid fallback uses first", "3", 9, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := examineChipIndex(images, tt.extver, tt.fallback); got != tt.want {
				t.Fatalf("index = %d, want %d", got, tt.want)
			}
		})
	}
	if got := examineChipIndex(nil, "", 0); got != -1 {
		t.Fatalf("empty index = %d, want -1", got)
	}
}

func TestExamineChipIndexNewLoadDefaultsToFirstChip(t *testing.T) {
	images := []*models.LoadedImage{examineTestImage("1"), examineTestImage("2")}
	if got := examineChipIndex(images, "", 0); got != 0 {
		t.Fatalf("new-load index = %d, want first chip (0)", got)
	}
	if got := examineChipIndex(images, "2", 1); got != 1 {
		t.Fatalf("reload index = %d, want matching EXTVER chip (1)", got)
	}
}
