package ui

import (
	"testing"

	fynetest "fyne.io/fyne/v2/test"
)

func TestComposeSetPickerToggles(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	ws := &composeWorkspace{viewports: []*viewport{newViewport(), newViewport(), newViewport(), newViewport()}}
	ws.clearPicker()
	if ws.activePicker.channel != -1 {
		t.Fatalf("after clear channel = %d, want -1", ws.activePicker.channel)
	}

	ws.setPicker(1, "black")
	if ws.activePicker != (composePicker{channel: 1, target: "black"}) {
		t.Fatalf("active picker = %+v, want channel 1 black", ws.activePicker)
	}

	// Switching target on the same channel keeps the picker active.
	ws.setPicker(1, "white")
	if ws.activePicker != (composePicker{channel: 1, target: "white"}) {
		t.Fatalf("active picker = %+v, want channel 1 white", ws.activePicker)
	}

	// Selecting the same picker again turns it off.
	ws.setPicker(1, "white")
	if ws.activePicker.channel != -1 {
		t.Fatalf("re-selecting picker should clear it, got %+v", ws.activePicker)
	}
}
