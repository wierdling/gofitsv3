package ui

import (
	"testing"

	fynetest "fyne.io/fyne/v2/test"

	"gofitsv3/internal/models"
)

// Cleaning needs all three channels; with one missing it must return before
// touching viewports or starting any work (viewports is nil here, so any
// further progress would panic).
func TestComposeCrossChannelCleanRequiresAllChannels(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	ws := &composeWorkspace{
		win:  app.NewWindow("test"),
		imgs: []*models.LoadedImage{{}, nil, {}},
	}
	ws.crossChannelClean()
	if ws.largeStore != nil {
		t.Fatal("unexpected state change")
	}
}
