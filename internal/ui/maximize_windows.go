//go:build windows

package ui

import (
	"syscall"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver"
)

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
