package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// outlineButtonStrokeWidth is the thickness of the colored outline drawn around
// an outlined button.
const outlineButtonStrokeWidth = 2

// outlinedButton tags a button with a color by overlaying a stroked rectangle
// directly on top of the button at the same bounds. Matching the button's own
// corner radius makes the outline trace the button's rounded edge instead of a
// larger mismatched box. The transparent rectangle is not tappable, so clicks
// still reach the button beneath it.
//
// Use this when you already have a button (or button-like CanvasObject) and want
// to keep your handle to it; see newOutlinedButton for the common case.
func outlinedButton(col color.Color, btn fyne.CanvasObject) fyne.CanvasObject {
	border := canvas.NewRectangle(color.Transparent)
	border.StrokeColor = col
	border.StrokeWidth = outlineButtonStrokeWidth
	border.CornerRadius = theme.Size(theme.SizeNameInputRadius)
	return container.NewStack(btn, border)
}

// newOutlinedButton creates a button with a colored outline. It returns the
// underlying *widget.Button (so callers can still Disable, SetText, etc.) and
// the wrapped CanvasObject to place in the layout.
func newOutlinedButton(label string, col color.Color, tapped func()) (*widget.Button, fyne.CanvasObject) {
	btn := widget.NewButton(label, tapped)
	return btn, outlinedButton(col, btn)
}
