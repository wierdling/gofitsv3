package ui

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// triangle SVGs — solid filled triangles, white fill.
var chevronUpResource = fyne.NewStaticResource("tri-up.svg", []byte(
	`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 8">` +
		`<path d="M5 0 L10 8 L0 8 Z" fill="#ffffff"/>` +
		`</svg>`))

var chevronDownResource = fyne.NewStaticResource("tri-down.svg", []byte(
	`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 8">` +
		`<path d="M5 8 L10 0 L0 0 Z" fill="#ffffff"/>` +
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

// selectAllEntry is a widget.Entry that selects all text when it gains focus.
type selectAllEntry struct {
	widget.Entry
}

func newSelectAllEntry() *selectAllEntry {
	e := &selectAllEntry{}
	e.ExtendBaseWidget(e)
	return e
}

func (e *selectAllEntry) FocusGained() {
	e.Entry.FocusGained()
	e.TypedShortcut(&fyne.ShortcutSelectAll{})
}

func (e *selectAllEntry) Tapped(ev *fyne.PointEvent) {
	e.Entry.Tapped(ev)
	e.TypedShortcut(&fyne.ShortcutSelectAll{})
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
	// MinWidth overrides the default 180-pixel minimum. When > 0, the entry is at
	// least max(content natural min, MinWidth) wide. Set to 1 to get the natural
	// content minimum with no extra enforcement.
	MinWidth float32
	// Min and Max clamp the value entered via the text field or chevrons.
	// Default to -/+ math.MaxFloat64 (unclamped).
	Min float64
	Max float64

	value    float64
	entry    *selectAllEntry
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
		Min:      -math.MaxFloat64,
		Max:      math.MaxFloat64,
	}
	n.ExtendBaseWidget(n)

	n.entry = newSelectAllEntry()
	n.entry.OnChanged = func(s string) {
		v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return
		}
		if v < n.Min {
			v = n.Min
		} else if v > n.Max {
			v = n.Max
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
		n.clearBtn,
		container.NewCenter(container.NewVBox(n.upBtn, n.downBtn)),
	)
	n.content = container.NewBorder(nil, nil, nil, btnSide, n.entry)

	n.syncText()
	return n
}

// Value returns the current numeric value.
func (n *NumberEntry) Value() float64 { return n.value }

// SetValue updates the value, refreshes the text, and fires OnChanged.
func (n *NumberEntry) SetValue(v float64) {
	if v < n.Min {
		v = n.Min
	} else if v > n.Max {
		v = n.Max
	}
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
	// Fyne Entry.MinSize ignores placeholder width; enforce enough room for content.
	minW := n.MinWidth
	if minW <= 0 {
		minW = 180 // default: enough room for ~10 chars + buttons
	}
	if s.Width < minW {
		s.Width = minW
	}
	return s
}

func (n *NumberEntry) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(n.content)
}
