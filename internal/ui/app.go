package ui

import (
	"image"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"

	"gofitsv3/internal/version"
)

// Run starts the Fyne application.
func Run() error {
	a := app.NewWithID("gofitsv3")
	a.SetIcon(fyne.NewStaticResource("icon.png", iconBytes))
	a.Settings().SetTheme(theme.DarkTheme())

	win := a.NewWindow("Go Fits V3 - " + version.Version)

	compose := newComposeWorkspace(a, win)
	examine := newExamineWorkspace(a, win)
	mosaic := newMosaicWorkspace(a, win)
	editContent, setEditImage := newEditWorkspace(a, win)

	editTab := container.NewTabItem("Edit", editContent)
	mosaicTab := container.NewTabItem("Mosaic", mosaic)
	tabs := container.NewAppTabs(
		mosaicTab,
		container.NewTabItem("Examine", examine),
		container.NewTabItem("Compose", compose),
		editTab,
	)

	tabs.OnChanged = func(tab *container.TabItem) {
		if tab == mosaicTab && globalSetMosaicMenu != nil {
			globalSetMosaicMenu()
		}
	}

	globalExportToEdit = func(img image.Image) {
		setEditImage(img)
		tabs.Select(editTab)
	}

	win.SetContent(tabs)
	// Use a very large size so the OS/Fyne caps it to the screen bounds,
	// which effectively starts the window maximized.
	win.Resize(fyne.NewSize(10000, 10000))
	win.Show()

	a.Run()
	return nil
}
