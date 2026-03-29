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
	a.Settings().SetTheme(theme.DarkTheme())

	win := a.NewWindow("GoFitsV3 - " + version.Version)

	compose := newComposeWorkspace(a, win)
	mosaic := newMosaicWorkspace(win)

	tabs := container.NewAppTabs(
		container.NewTabItem("Compose", compose),
		container.NewTabItem("Mosaic", mosaic),
	)

	win.SetContent(tabs)
	// Smaller default size to better fit shorter displays; still resizable by the user.
	win.Resize(fyne.NewSize(980, 620))
	win.Show()

	a.Run()
	return nil
}
