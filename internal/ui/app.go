package ui

import (
	"image"
	"image/color"
	"syscall"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver"
	"fyne.io/fyne/v2/theme"

	fynetooltip "github.com/dweymouth/fyne-tooltip"

	"gofitsv3/internal/version"
)

// appTheme wraps the dark theme and widens the inner padding so button text
// has more breathing room on the left and right.
type appTheme struct{ fyne.Theme }

var actionButtonColor = color.NRGBA{R: 76, G: 175, B: 80, A: 255}

func (t appTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	if name == theme.ColorNamePrimary {
		return actionButtonColor
	}
	return t.Theme.Color(name, variant)
}

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
	mosaicContent, mosaicMenu, loadMosaicItem, saveMosaicItem := newMosaicWorkspace(a, win)
	editContent, setEditImage := newEditWorkspace(a, win)

	if len(composeMenus) > 0 && composeMenus[0].Label == "File" {
		fileMenu := composeMenus[0]
		if len(fileMenu.Items) >= 3 {
			origItems := fileMenu.Items
			fileMenu.Items = []*fyne.MenuItem{
				saveMosaicItem,
				loadMosaicItem,
				fyne.NewMenuItemSeparator(),
				origItems[1],
				origItems[0],
				fyne.NewMenuItemSeparator(),
			}
			fileMenu.Items = append(fileMenu.Items, origItems[3:]...)
		}
	}

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

	globalSelectComposeTab = func() {
		tabs.Select(composeTab)
	}

	windowMenu := fyne.NewMenu("Window",
		fyne.NewMenuItem("Debug Log", func() {
			showDebugWindow(a)
		}),
	)

	var allMenus []*fyne.Menu
	if len(composeMenus) >= 3 {
		allMenus = []*fyne.Menu{
			composeMenus[0], // File
			mosaicMenu,      // Mosaic
			composeMenus[1], // Compose
			composeMenus[2], // View
			windowMenu,      // Window
		}
	} else {
		allMenus = append(composeMenus, mosaicMenu, windowMenu)
	}
	win.SetMainMenu(fyne.NewMainMenu(allMenus...))

	win.SetContent(fynetooltip.AddWindowToolTipLayer(tabs, win.Canvas()))
	win.SetCloseIntercept(func() {
		a.Quit()
	})
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
