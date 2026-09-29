package ui

import (
	"testing"

	"gofitsv3/internal/models"
)

func TestComposeApplyDedicatedLPassThrough(t *testing.T) {
	ws := &composeWorkspace{}
	buf := []byte{1, 2, 3, 4}
	dedicated := &models.LoadedImage{}
	cases := []struct {
		name      string
		settings  models.LRGBSettings
		dedicated *models.LoadedImage
	}{
		{"disabled", models.LRGBSettings{Enabled: false, LuminanceWeight: 1}, dedicated},
		{"no image", models.LRGBSettings{Enabled: true, LuminanceWeight: 1}, nil},
		{"zero weight", models.LRGBSettings{Enabled: true, LuminanceWeight: 0}, dedicated},
	}
	for _, tc := range cases {
		got, err := ws.applyDedicatedL(buf, 1, 1, tc.settings, tc.dedicated)
		if err != nil {
			t.Fatalf("%s: unexpected error %v", tc.name, err)
		}
		if &got[0] != &buf[0] {
			t.Errorf("%s: expected the input buffer to be returned unchanged", tc.name)
		}
	}
}
