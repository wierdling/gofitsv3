package ui

import (
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

	tabs := container.NewAppTabs(
		container.NewTabItem("Mosaic", mosaic),
		container.NewTabItem("Examine", examine),
		container.NewTabItem("Compose", compose),
	)

	win.SetContent(tabs)
	// Use a very large size so the OS/Fyne caps it to the screen bounds,
	// which effectively starts the window maximized.
	win.Resize(fyne.NewSize(10000, 10000))
	win.Show()

	a.Run()
	return nil
}
