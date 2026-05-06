package ui

import (
	"image"
	"syscall"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver"
	"fyne.io/fyne/v2/theme"

	"gofitsv3/internal/version"
)

// appTheme wraps the dark theme and widens the inner padding so button text
// has more breathing room on the left and right.
type appTheme struct{ fyne.Theme }

func (t appTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	case theme.SizeNameInnerPadding:
		return 5 // default is 4; gives TextSize+10 control height
	case theme.SizeNameInputRadius:
		return 10 // default is 5; rounder button and input corners
	}
	return t.Theme.Size(name)
}

// Run starts the Fyne application.
func Run() error {
	a := app.NewWithID("gofitsv3")
	a.SetIcon(fyne.NewStaticResource("icon.png", iconBytes))
	a.Settings().SetTheme(&appTheme{theme.DarkTheme()})

	win := a.NewWindow("Go Fits V3 - " + version.Version)

	composeContent, composeMenus := newComposeWorkspace(a, win)
	examine := newExamineWorkspace(a, win)
	mosaicContent, mosaicMenu := newMosaicWorkspace(a, win)
	editContent, setEditImage := newEditWorkspace(a, win)

	editTab := container.NewTabItem("Edit", editContent)
	mosaicTab := container.NewTabItem("Mosaic", mosaicContent)
	composeTab := container.NewTabItem("Compose", composeContent)
	tabs := container.NewAppTabs(
		mosaicTab,
		container.NewTabItem("Examine", examine),
		composeTab,
		editTab,
	)

	globalExportToEdit = func(img image.Image) {
		setEditImage(img)
		tabs.Select(editTab)
	}

	windowMenu := fyne.NewMenu("Window",
		fyne.NewMenuItem("Debug Log", func() {
			showDebugWindow(a)
		}),
	)

	allMenus := append(composeMenus, mosaicMenu, windowMenu)
	win.SetMainMenu(fyne.NewMainMenu(allMenus...))

	win.SetContent(tabs)
	win.Show()
	maximizeWindow(win)

	a.Run()
	return nil
}

// maximizeWindow sends SW_MAXIMIZE to the native Win32 window handle.
func maximizeWindow(win fyne.Window) {
	nw, ok := win.(driver.NativeWindow)
	if !ok {
		return
	}
	nw.RunNative(func(ctx any) {
		wctx, ok := ctx.(driver.WindowsWindowContext)
		if !ok {
			return
		}
		const swMaximize = 3
		user32 := syscall.NewLazyDLL("user32.dll")
		showWindow := user32.NewProc("ShowWindow")
		showWindow.Call(wctx.HWND, uintptr(swMaximize))
	})
}
