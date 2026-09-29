package ui

import "fyne.io/fyne/v2"

// minWidthLayout enforces a minimum width on its single child while preserving its natural height.
type minWidthLayout struct{ w float32 }

func (l *minWidthLayout) Layout(obs []fyne.CanvasObject, size fyne.Size) {
	for _, o := range obs {
		o.Resize(size)
		o.Move(fyne.NewPos(0, 0))
	}
}

func (l *minWidthLayout) MinSize(obs []fyne.CanvasObject) fyne.Size {
	var h float32
	for _, o := range obs {
		if m := o.MinSize().Height; m > h {
			h = m
		}
	}
	w := l.w
	for _, o := range obs {
		if m := o.MinSize().Width; m > w {
			w = m
		}
	}
	return fyne.NewSize(w, h)
}

// fixedVSpacingLayout stacks its children vertically with an exact pixel gap between them.
type fixedVSpacingLayout struct{ gap float32 }

func (l *fixedVSpacingLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	y := float32(0)
	for i, o := range objects {
		if i > 0 {
			y += l.gap
		}
		h := o.MinSize().Height
		o.Move(fyne.NewPos(0, y))
		o.Resize(fyne.NewSize(size.Width, h))
		y += h
	}
}

func (l *fixedVSpacingLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var w, h float32
	for i, o := range objects {
		if i > 0 {
			h += l.gap
		}
		ms := o.MinSize()
		if ms.Width > w {
			w = ms.Width
		}
		h += ms.Height
	}
	return fyne.NewSize(w, h)
}

// sidePaddedLayout adds equal horizontal padding on the left and right of its single child.
type sidePaddedLayout struct{ pad float32 }

func (l *sidePaddedLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	for _, o := range objects {
		o.Move(fyne.NewPos(l.pad, 0))
		w := size.Width - 2*l.pad
		if w < 0 {
			w = 0
		}
		h := size.Height
		if h < 0 {
			h = 0
		}
		o.Resize(fyne.NewSize(w, h))
	}
}

func (l *sidePaddedLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var w, h float32
	for _, o := range objects {
		ms := o.MinSize()
		if ms.Width > w {
			w = ms.Width
		}
		if ms.Height > h {
			h = ms.Height
		}
	}
	return fyne.NewSize(w+2*l.pad, h)
}
