package ui

import (
	"fmt"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// chevron SVGs — white fill so Fyne's theme engine recolors them correctly.
var chevronUpResource = fyne.NewStaticResource("chevron-up.svg", []byte(
	`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 12 8">` +
		`<path d="M0 7 L6 1 L12 7 L10 7 L6 3 L2 7 Z" fill="#ffffff"/>` +
		`</svg>`))

var chevronDownResource = fyne.NewStaticResource("chevron-down.svg", []byte(
	`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 12 8">` +
		`<path d="M0 1 L6 7 L12 1 L10 1 L6 5 L2 1 Z" fill="#ffffff"/>` +
		`</svg>`))

// chevronBtn is a minimal tappable icon — no button chrome, sized explicitly.
type chevronBtn struct {
	widget.BaseWidget
	resource fyne.Resource
	size     fyne.Size
	onTap    func()
}

func newChevronBtn(res fyne.Resource, w, h float32, onTap func()) *chevronBtn {
	b := &chevronBtn{resource: res, size: fyne.NewSize(w, h), onTap: onTap}
	b.ExtendBaseWidget(b)
	return b
}

func (b *chevronBtn) Tapped(_ *fyne.PointEvent)          { b.onTap() }
func (b *chevronBtn) TappedSecondary(_ *fyne.PointEvent) {}
func (b *chevronBtn) MinSize() fyne.Size                 { return b.size }

func (b *chevronBtn) CreateRenderer() fyne.WidgetRenderer {
	img := canvas.NewImageFromResource(b.resource)
	img.FillMode = canvas.ImageFillContain
	return widget.NewSimpleRenderer(img)
}

// NumberEntry is a compact numeric input with chevron step buttons and a clear
// button that resets the value to zero.
type NumberEntry struct {
	widget.BaseWidget

	// Step is the amount added/subtracted by the up/down chevrons.
	Step float64
	// Decimals controls how many decimal places are shown.
	Decimals int
	// OnChanged is called whenever the value changes (button click or manual edit).
	OnChanged func(float64)

	value    float64
	entry    *widget.Entry
	upBtn    *chevronBtn
	downBtn  *chevronBtn
	clearBtn *widget.Button
	content  *fyne.Container
}

// NewNumberEntry creates a NumberEntry with the given step size and decimal places.
func NewNumberEntry(step float64, decimals int) *NumberEntry {
	n := &NumberEntry{
		Step:     step,
		Decimals: decimals,
	}
	n.ExtendBaseWidget(n)

	n.entry = widget.NewEntry()
	n.entry.OnChanged = func(s string) {
		v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return
		}
		n.value = v
		if n.OnChanged != nil {
			n.OnChanged(v)
		}
	}

	// Chevron size: 45% of entry height each, width proportional to the SVG aspect (12:8).
	entryH := n.entry.MinSize().Height
	chevH := entryH * 0.22
	chevW := chevH * (12.0 / 8.0)

	n.upBtn = newChevronBtn(chevronUpResource, chevW, chevH, func() {
		n.SetValue(n.value + n.Step)
	})
	n.downBtn = newChevronBtn(chevronDownResource, chevW, chevH, func() {
		n.SetValue(n.value - n.Step)
	})

	n.clearBtn = widget.NewButtonWithIcon("", theme.ContentClearIcon(), func() {
		n.SetValue(0)
	})
	n.clearBtn.Importance = widget.LowImportance

	btnSide := container.NewHBox(
		container.NewCenter(container.NewVBox(n.upBtn, n.downBtn)),
		n.clearBtn,
	)
	n.content = container.NewBorder(nil, nil, nil, btnSide, n.entry)

	n.syncText()
	return n
}

// Value returns the current numeric value.
func (n *NumberEntry) Value() float64 { return n.value }

// SetValue updates the value, refreshes the text, and fires OnChanged.
func (n *NumberEntry) SetValue(v float64) {
	n.value = v
	n.syncText()
	if n.OnChanged != nil {
		n.OnChanged(v)
	}
}

// Disable prevents user interaction.
func (n *NumberEntry) Disable() {
	n.entry.Disable()
	n.upBtn.Hide()
	n.downBtn.Hide()
	n.clearBtn.Disable()
}

// Enable restores user interaction.
func (n *NumberEntry) Enable() {
	n.entry.Enable()
	n.upBtn.Show()
	n.downBtn.Show()
	n.clearBtn.Enable()
}

func (n *NumberEntry) syncText() {
	text := fmt.Sprintf("%.*f", n.Decimals, n.value)
	if n.entry.Text != text {
		fyne.Do(func() {
			n.entry.SetText(text)
		})
	}
}

func (n *NumberEntry) MinSize() fyne.Size {
	s := n.content.MinSize()
	// Fyne Entry.MinSize ignores placeholder width; enforce enough room for 10 chars + buttons.
	const minWidth float32 = 180
	if s.Width < minWidth {
		s.Width = minWidth
	}
	return s
}

func (n *NumberEntry) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(n.content)
}
