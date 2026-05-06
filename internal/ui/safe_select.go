package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"
)

// SafeSelect guards Fyne's popup creation path when the widget is no longer
// attached to a live canvas. Fyne's stock Select panics in that case.
type SafeSelect struct {
	widget.Select
}

func NewSafeSelect(options []string, changed func(string)) *SafeSelect {
	s := &SafeSelect{}
	s.Options = options
	s.OnChanged = changed
	s.PlaceHolder = "(Select one)"
	s.ExtendBaseWidget(s)
	return s
}

func (s *SafeSelect) attachedCanvas() fyne.Canvas {
	app := fyne.CurrentApp()
	if app == nil || app.Driver() == nil {
		return nil
	}
	return app.Driver().CanvasForObject(s)
}

func (s *SafeSelect) Tapped(ev *fyne.PointEvent) {
	if s.attachedCanvas() == nil {
		return
	}
	s.Select.Tapped(ev)
}

func (s *SafeSelect) TypedKey(ev *fyne.KeyEvent) {
	switch ev.Name {
	case fyne.KeySpace, fyne.KeyUp, fyne.KeyDown:
		if s.attachedCanvas() == nil {
			return
		}
	}
	s.Select.TypedKey(ev)
}
