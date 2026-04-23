package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/widget"
)

const (
	toggleWidth  float32 = 44
	toggleHeight float32 = 24
	togglePad    float32 = 3
)

var (
	toggleColorOff = color.NRGBA{R: 0x1a, G: 0x1a, B: 0x1a, A: 0xff} // ~10% grey
	toggleColorOn  = color.NRGBA{R: 0x29, G: 0x7e, B: 0xd8, A: 0xff} // nice blue
)

// Toggle is a pill-shaped on/off switch that replaces a checkbox.
type Toggle struct {
	widget.BaseWidget
	Checked   bool
	OnChanged func(bool)
}

// NewToggle creates a Toggle with an optional change callback.
func NewToggle(changed func(bool)) *Toggle {
	t := &Toggle{OnChanged: changed}
	t.ExtendBaseWidget(t)
	return t
}

func (t *Toggle) Tapped(_ *fyne.PointEvent) {
	t.Checked = !t.Checked
	t.Refresh()
	if t.OnChanged != nil {
		t.OnChanged(t.Checked)
	}
}

func (t *Toggle) TappedSecondary(_ *fyne.PointEvent) {}

func (t *Toggle) MinSize() fyne.Size {
	return fyne.NewSize(toggleWidth, toggleHeight)
}

func (t *Toggle) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(toggleColorOff)
	bg.CornerRadius = toggleHeight / 2

	thumb := canvas.NewCircle(color.White)

	r := &toggleRenderer{toggle: t, bg: bg, thumb: thumb}
	r.Layout(fyne.NewSize(toggleWidth, toggleHeight))
	return r
}

type toggleRenderer struct {
	toggle *Toggle
	bg     *canvas.Rectangle
	thumb  *canvas.Circle
}

func (r *toggleRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.bg, r.thumb}
}

func (r *toggleRenderer) Layout(size fyne.Size) {
	r.bg.Resize(size)
	r.bg.Move(fyne.NewPos(0, 0))

	diameter := size.Height - togglePad*2
	r.thumb.Resize(fyne.NewSize(diameter, diameter))
	r.updateThumbPos(size)
}

func (r *toggleRenderer) MinSize() fyne.Size {
	return fyne.NewSize(toggleWidth, toggleHeight)
}

func (r *toggleRenderer) Refresh() {
	if r.toggle.Checked {
		r.bg.FillColor = toggleColorOn
	} else {
		r.bg.FillColor = toggleColorOff
	}
	r.bg.Refresh()
	r.updateThumbPos(r.bg.Size())
	r.thumb.Refresh()
}

func (r *toggleRenderer) Destroy() {}

func (r *toggleRenderer) updateThumbPos(size fyne.Size) {
	diameter := size.Height - togglePad*2
	y := togglePad
	var x float32
	if r.toggle.Checked {
		x = size.Width - diameter - togglePad
	} else {
		x = togglePad
	}
	r.thumb.Move(fyne.NewPos(x, y))
}
