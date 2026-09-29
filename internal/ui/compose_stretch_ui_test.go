package ui

import (
	"testing"

	fynetest "fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

func TestComposeUpdateHistScaleLabel(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	ws := &composeWorkspace{sharedHistCheck: NewToggle(nil), histScaleStatus: widget.NewLabel("")}
	ws.updateHistScaleLabel()
	if got := ws.histScaleStatus.Text; got != "Histograms: per-filter auto scale" {
		t.Errorf("unchecked label = %q", got)
	}
	ws.sharedHistCheck.SetChecked(true)
	ws.updateHistScaleLabel()
	if got := ws.histScaleStatus.Text; got != "Histograms: shared filter scale" {
		t.Errorf("checked label = %q", got)
	}
}
