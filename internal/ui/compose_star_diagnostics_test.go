package ui

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	fynetest "fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/processing"
)

// Fyne's test window returns a fresh clipboard on each call. Keep one
// clipboard here to model the desktop system clipboard across calls.
type diagnosticTestWindow struct {
	fyne.Window
	clipboard fyne.Clipboard
}

func (w diagnosticTestWindow) Clipboard() fyne.Clipboard { return w.clipboard }

func TestSkippedStarClickShowsAndCopiesDiagnostics(t *testing.T) {
	for _, channel := range []int{0, 3} {
		t.Run(map[int]string{0: "channel", 3: "composite"}[channel], func(t *testing.T) {
			app := fynetest.NewApp()
			defer app.Quit()
			win := diagnosticTestWindow{Window: app.NewWindow("diagnostics test"), clipboard: fynetest.NewClipboard()}
			defer win.Close()
			win.SetContent(widget.NewLabel("image"))
			ws := &composeWorkspace{win: win, viewports: make([]*viewport, 4)}
			vp := newViewport()
			vp.origW, vp.origH = 200, 200
			vp.overlay.Resize(fyne.NewSize(200, 200))
			ws.viewports[channel] = vp
			points := []processing.StarTreatmentDiagnostic{
				{Source: "Channel 1 (Blue)", SourceID: 10, X: 50, Y: 50, Usable: true},
				{Source: "Channel 2 (Green)", SourceID: 42, X: 50, Y: 50, Reason: "insufficient background samples", Saturated: true},
			}
			vp.setDiagnosticOverlay(points)
			vp.overlay.updateMarkerHover(fyne.NewPos(50, 50))
			win.Clipboard().SetContent("old contents")
			if channel == 3 {
				ws.starDiagPoints = points
				ws.starDiagnosticTapped(fyne.NewPos(50, 50))
			} else {
				ws.starDiagChannelPts[channel] = points
				ws.channelStarDiagnosticTapped(channel, fyne.NewPos(50, 50))
			}
			got := win.Clipboard().Content()
			for _, want := range []string{"Source: Channel 2 (Green)", "Star ID: 42", "Position: 50.0, 50.0", "Status: skipped", "Saturated: true", "Halo validated: false", "Reason: insufficient background samples"} {
				if !strings.Contains(got, want) {
					t.Fatalf("clipboard missing %q: %s", want, got)
				}
			}
			if win.Canvas().Overlays().Top() == nil {
				t.Fatal("diagnostic dialog was not shown")
			}
		})
	}
}
