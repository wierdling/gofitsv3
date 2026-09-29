package ui

import (
	"testing"

	"fyne.io/fyne/v2"
	fynetest "fyne.io/fyne/v2/test"

	"gofitsv3/internal/models"
)

func newMenuTestWorkspace(app fyne.App) *composeWorkspace {
	item := func() *fyne.MenuItem { return fyne.NewMenuItem("x", nil) }
	return &composeWorkspace{
		win:                app.NewWindow("test"),
		imgs:               make([]*models.LoadedImage, 3),
		viewHeaderItems:    []*fyne.MenuItem{item(), item(), item()},
		saveHeaderItems:    []*fyne.MenuItem{item(), item(), item()},
		copySettingsItem:   item(),
		matchStretchItem:   item(),
		normalizeScaleItem: item(),
		alignChannelsItem:  item(),
		cleanChannelsItem:  item(),
		resetDataItem:      item(),
		exportRGBItem:      item(),
		sendToEditItem:     item(),
		saveProjectItem:    item(),
		largeFilesCheck:    NewToggle(nil),
		blinkCheck:         NewToggle(nil),
	}
}

func TestComposeUpdateMenusEmpty(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()
	ws := newMenuTestWorkspace(app)
	ws.updateMenus()

	for name, it := range map[string]*fyne.MenuItem{
		"save project": ws.saveProjectItem, "align": ws.alignChannelsItem,
		"export": ws.exportRGBItem, "header 0": ws.viewHeaderItems[0],
	} {
		if !it.Disabled {
			t.Errorf("%s enabled with no channels loaded", name)
		}
	}
	if ws.largeFilesCheck.disabled {
		t.Error("large-files toggle should be enabled with nothing loaded")
	}
}

func TestComposeUpdateMenusPartialAndFull(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()
	ws := newMenuTestWorkspace(app)

	ws.imgs[0] = &models.LoadedImage{}
	ws.updateMenus()
	if ws.viewHeaderItems[0].Disabled || !ws.viewHeaderItems[1].Disabled {
		t.Error("header items should follow per-channel load state")
	}
	if ws.saveProjectItem.Disabled || ws.copySettingsItem.Disabled {
		t.Error("save project / copy settings should be enabled with channel 1 loaded")
	}
	if !ws.alignChannelsItem.Disabled {
		t.Error("align should need all three channels")
	}
	if !ws.largeFilesCheck.disabled {
		t.Error("large-files toggle should lock once a channel is loaded")
	}

	ws.imgs[1], ws.imgs[2] = &models.LoadedImage{}, &models.LoadedImage{}
	ws.updateMenus()
	for name, it := range map[string]*fyne.MenuItem{
		"align": ws.alignChannelsItem, "clean": ws.cleanChannelsItem,
		"export": ws.exportRGBItem, "send to edit": ws.sendToEditItem,
	} {
		if it.Disabled {
			t.Errorf("%s disabled with all channels loaded", name)
		}
	}
}
