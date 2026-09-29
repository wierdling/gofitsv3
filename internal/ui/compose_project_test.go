package ui

import (
	"encoding/json"
	"testing"

	"fyne.io/fyne/v2"
	fynetest "fyne.io/fyne/v2/test"

	"gofitsv3/internal/models"
)

func TestComposeViewportStatesRoundTrip(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	ws := &composeWorkspace{viewports: []*viewport{newViewport(), nil, newViewport()}}
	// Keep zoom and the zoom selector consistent, as the UI does.
	ws.viewports[0].zoom = 2
	ws.viewports[0].setZoomLabelValue("200%")
	ws.viewports[0].scroll.Offset = fyne.NewPos(10, 20)
	ws.viewports[2].zoom = 0.5
	ws.viewports[2].setZoomLabelValue("50%")

	states := ws.captureViewportStates()
	if len(states) != 3 {
		t.Fatalf("captured %d states, want 3", len(states))
	}
	if got := states[0].offset; got != fyne.NewPos(10, 20) {
		t.Errorf("captured offset = %v, want (10,20)", got)
	}

	ws.viewports[0].zoom = 4
	ws.viewports[0].setZoomLabelValue("fit")
	ws.viewports[2].zoom = 1
	ws.viewports[2].setZoomLabelValue("100%")
	ws.restoreViewportStates(states)

	if got := ws.viewports[0].zoom; got != 2 {
		t.Errorf("viewport 0 zoom = %v, want 2", got)
	}
	if got := ws.viewports[0].zoomLabel.Selected; got != "200%" {
		t.Errorf("viewport 0 zoom label = %q, want 200%%", got)
	}
	if got := ws.viewports[2].zoom; got != 0.5 {
		t.Errorf("viewport 2 zoom = %v, want 0.5", got)
	}
}

func TestSnapshotStarWhiteningStateRoundTripPreservesForcedStars(t *testing.T) {
	original := models.StarWhiteningState{
		ReferenceBlinkID: "channel-2",
		ForcedStars:      map[int]bool{17: true, 23: false},
	}
	snapshot := snapshotStarWhiteningState(original)
	if snapshot == nil {
		t.Fatal("snapshot is nil for a disabled state with forced stars")
	}
	original.ForcedStars[17] = false
	if !snapshot.ForcedStars[17] {
		t.Fatal("snapshot shares the mutable forced-star map")
	}
	project := models.ComposeProject{StarWhitening: snapshot}
	encoded, err := json.Marshal(project)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	var decoded models.ComposeProject
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if decoded.StarWhitening == nil || !decoded.StarWhitening.ForcedStars[17] || decoded.StarWhitening.ReferenceBlinkID != "channel-2" {
		t.Fatalf("round trip lost forced override: %#v", decoded.StarWhitening)
	}
}

func TestPreservedForcedStarsForReference(t *testing.T) {
	state := models.StarWhiteningState{ReferenceBlinkID: "channel-2", ForcedStars: map[int]bool{17: true}}
	if got := preservedForcedStarsForReference(state, "channel-2"); !got[17] {
		t.Fatal("same-reference settings change did not preserve forced star")
	}
	if got := preservedForcedStarsForReference(state, "channel-3"); got != nil {
		t.Fatalf("reference change retained forced stars: %#v", got)
	}
}
