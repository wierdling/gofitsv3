package ui

import (
	"testing"

	"gofitsv3/internal/models"
)

func TestComposeStarNeutralizerOffWhenWhiteningDisabled(t *testing.T) {
	ws := &composeWorkspace{imgs: []*models.LoadedImage{{}, {}, {}}}
	if ref := ws.whiteningReference(); ref != nil {
		t.Fatalf("whiteningReference = %v, want nil when whitening is disabled", ref)
	}
	n, err := ws.buildStarNeutralizer(10, 10)
	if n != nil || err != nil {
		t.Fatalf("buildStarNeutralizer = %v, %v; want nil, nil", n, err)
	}
}

func TestComposeWeightSourcesSkipsEmptyChannels(t *testing.T) {
	ws := &composeWorkspace{imgs: []*models.LoadedImage{nil, {Path: "g.fits"}, nil}}
	got := ws.composeWeightSources()
	if len(got) != 1 || got[0].ID != models.ComposeChannel2BlinkID {
		t.Fatalf("sources = %+v, want only channel 2", got)
	}
}
