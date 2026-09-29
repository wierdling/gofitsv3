package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// tapShield is a transparent overlay that swallows taps. It is placed on top
// of a freshly opened floating window's content for a couple of frames so a
// widget.Select can't be tapped before Fyne's canvas cache is populated by
// the first paint pass (see openOverlayLayerWindow).
type tapShield struct {
	widget.BaseWidget
}

func newTapShield() *tapShield {
	s := &tapShield{}
	s.ExtendBaseWidget(s)
	return s
}

func (s *tapShield) Tapped(*fyne.PointEvent) {}

func (s *tapShield) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(canvas.NewRectangle(color.Transparent))
}

// channelBorder wraps a canvas object with a colored rectangular border.
func channelBorder(content fyne.CanvasObject, col color.Color) fyne.CanvasObject {
	rect := canvas.NewRectangle(color.Transparent)
	rect.StrokeColor = col
	rect.StrokeWidth = 1
	rect.CornerRadius = 6
	return container.NewMax(content, rect)
}

// offsetRow builds a compact labelled row for the manual-offset inputs.
// The label is rendered smaller than body text to save vertical space.
func offsetRow(name string, entry *NumberEntry) fyne.CanvasObject {
	lbl := canvas.NewText(name, theme.ForegroundColor())
	lbl.TextSize = theme.TextSize() - 2
	return container.NewBorder(nil, nil, lbl, nil, entry)
}
