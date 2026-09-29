//go:build !windows

package ui

import "fyne.io/fyne/v2"

// maximizeWindow is only implemented on Windows.
func maximizeWindow(win fyne.Window) {}
