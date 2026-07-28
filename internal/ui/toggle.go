package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

const (
	toggleWidth float32 = 35
	togglePad   float32 = 1.5
)

// toggleH returns the pill height: text size + 4px so it sits just above text.
func toggleH() float32 { return theme.TextSize() + 4 }

var (
	toggleColorOff    = color.NRGBA{R: 0x44, G: 0x44, B: 0x44, A: 0xff}
	toggleColorOn     = color.NRGBA{R: 0x29, G: 0x7e, B: 0xd8, A: 0xff}
	toggleBorderColor = color.NRGBA{R: 0x77, G: 0x77, B: 0x77, A: 0xff}
)

// Toggle is a pill-shaped on/off switch that replaces a checkbox.
type Toggle struct {
	widget.BaseWidget
	Checked   bool
	disabled  bool
	OnChanged func(bool)
}

// NewToggle creates a Toggle with an optional change callback.
func NewToggle(changed func(bool)) *Toggle {
	t := &Toggle{OnChanged: changed}
	t.ExtendBaseWidget(t)
	return t
}

func (t *Toggle) Tapped(_ *fyne.PointEvent) {
	if t.disabled {
		return
	}
	t.Checked = !t.Checked
	t.Refresh()
	if t.OnChanged != nil {
		t.OnChanged(t.Checked)
	}
}

func (t *Toggle) Disable() { t.disabled = true; t.Refresh() }
func (t *Toggle) Enable()  { t.disabled = false; t.Refresh() }

func (t *Toggle) TappedSecondary(_ *fyne.PointEvent) {}

// SetChecked updates the checked state and refreshes the widget.
func (t *Toggle) SetChecked(v bool) {
	t.Checked = v
	t.Refresh()
}

func (t *Toggle) MinSize() fyne.Size {
	return fyne.NewSize(toggleWidth, toggleH())
}

func (t *Toggle) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(toggleColorOff)
	bg.CornerRadius = toggleH() / 2
	bg.StrokeColor = toggleBorderColor
	bg.StrokeWidth = 1

	thumb := canvas.NewCircle(color.White)
	thumb.StrokeColor = color.Black
	thumb.StrokeWidth = 2

	r := &toggleRenderer{toggle: t, bg: bg, thumb: thumb}
	r.Layout(fyne.NewSize(toggleWidth, toggleH()))
	return r
}

type toggleRenderer struct {
	toggle     *Toggle
	bg         *canvas.Rectangle
	thumb      *canvas.Circle
	layoutSize fyne.Size // full size from the most recent Layout call
}

func (r *toggleRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.bg, r.thumb}
}

func (r *toggleRenderer) Layout(size fyne.Size) {
	r.layoutSize = size
	h := toggleH()
	bgY := (size.Height - h) / 2
	r.bg.Move(fyne.NewPos(0, bgY))
	r.bg.Resize(fyne.NewSize(size.Width, h))
	r.bg.CornerRadius = h / 2

	diameter := h - togglePad*2
	r.thumb.Resize(fyne.NewSize(diameter, diameter))
	r.updateThumbPos()
}

func (r *toggleRenderer) MinSize() fyne.Size {
	return fyne.NewSize(toggleWidth, toggleH())
}

func (r *toggleRenderer) Refresh() {
	if r.toggle.Checked {
		r.bg.FillColor = toggleColorOn
	} else {
		r.bg.FillColor = toggleColorOff
	}
	r.bg.Refresh()
	r.updateThumbPos()
	r.thumb.Refresh()
}

func (r *toggleRenderer) Destroy() {}

func (r *toggleRenderer) updateThumbPos() {
	size := r.layoutSize
	h := toggleH()
	bgY := (size.Height - h) / 2
	diameter := h - togglePad*2
	y := bgY + togglePad
	var x float32
	if r.toggle.Checked {
		x = size.Width - diameter - togglePad
	} else {
		x = togglePad
	}
	r.thumb.Move(fyne.NewPos(x, y))
}
