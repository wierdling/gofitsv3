package ui

import (
	"testing"

	"gofitsv3/internal/models"
)

func TestComposeSourceBlinkIDs(t *testing.T) {
	ws := &composeWorkspace{
		imgs: make([]*models.LoadedImage, 5),
		overlayLayers: []*overlayLayer{
			{idx: 4, settings: models.OrangeLayerState{BlinkID: "layer-b"}},
			{idx: 3, settings: models.OrangeLayerState{BlinkID: "layer-a"}},
			{idx: 9, settings: models.OrangeLayerState{BlinkID: "out-of-range"}},
		},
	}
	got := ws.composeSourceBlinkIDs()
	want := []string{models.ComposeChannel1BlinkID, models.ComposeChannel2BlinkID, models.ComposeChannel3BlinkID, "layer-a", "layer-b"}
	if len(got) != len(want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ids[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
