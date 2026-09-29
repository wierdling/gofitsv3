package ui

import (
	"testing"

	fynetest "fyne.io/fyne/v2/test"

	"gofitsv3/internal/models"
)

func TestComposeChannelOffsetFields(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	x, y, r := NewNumberEntry(1, 2), NewNumberEntry(1, 2), NewNumberEntry(1, 2)
	x.SetValue(1.5)
	y.SetValue(-2)
	r.SetValue(0.25)
	ws := &composeWorkspace{controlSets: []*models.ChannelControl{
		{XOffsetEntry: x, YOffsetEntry: y, RotOffsetEntry: r},
		{XOffsetEntry: x, YOffsetEntry: y}, // no rotation entry
		{},                                 // no offset entries
		nil,
	}}

	if dx, dy, rot, ok := ws.composeChannelOffsetFields(0); !ok || dx != 1.5 || dy != -2 || rot != 0.25 {
		t.Errorf("channel 0 = %v,%v,%v,%v; want 1.5,-2,0.25,true", dx, dy, rot, ok)
	}
	if _, _, rot, ok := ws.composeChannelOffsetFields(1); !ok || rot != 0 {
		t.Errorf("channel 1 rot,ok = %v,%v; want 0,true", rot, ok)
	}
	for _, idx := range []int{-1, 2, 3, 4} {
		if _, _, _, ok := ws.composeChannelOffsetFields(idx); ok {
			t.Errorf("channel %d: ok = true, want false", idx)
		}
	}
}
