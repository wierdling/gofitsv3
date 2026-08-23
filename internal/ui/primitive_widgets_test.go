package ui

import (
	"math"
	"testing"

	"fyne.io/fyne/v2"
)

func TestNumberEntryRejectsNonFiniteAndNotifiesOncePerValue(t *testing.T) {
	n := NewNumberEntry(1, 2)
	var got []float64
	n.OnChanged = func(v float64) { got = append(got, v) }

	n.SetValue(2)
	n.SetValue(2)
	n.SetValue(1.234)
	n.SetValue(math.NaN())
	n.SetValue(math.Inf(1))
	n.SetValue(math.Copysign(0, -1))
	if n.Value() != 0 {
		t.Fatalf("Value() = %v, want canonical zero", n.Value())
	}
	if len(got) != 3 || got[0] != 2 || got[1] != 1.23 || got[2] != 0 {
		t.Fatalf("OnChanged values = %v, want [2 1.23 0]", got)
	}
}

func TestSafeSelectDetachedInteractionsAreNoOps(t *testing.T) {
	s := NewSafeSelect([]string{"one", "two"}, nil)
	s.Tapped(nil)
	s.TypedKey(&fyne.KeyEvent{Name: fyne.KeyDown})
	if s.Selected != "" {
		t.Fatalf("detached select changed selection to %q", s.Selected)
	}
}

func TestToggleStateAndDisabledCallbacks(t *testing.T) {
	var got []bool
	toggle := NewToggle(func(v bool) { got = append(got, v) })
	toggle.Tapped(nil)
	if !toggle.Checked || len(got) != 1 || !got[0] {
		t.Fatalf("after tap: checked=%v callbacks=%v", toggle.Checked, got)
	}
	toggle.Disable()
	toggle.Tapped(nil)
	if !toggle.Checked || len(got) != 1 {
		t.Fatalf("disabled tap changed state/callbacks: checked=%v callbacks=%v", toggle.Checked, got)
	}
	toggle.Enable()
	toggle.SetChecked(false)
	if toggle.Checked {
		t.Fatal("SetChecked(false) left toggle checked")
	}
	toggle.Tapped(nil)
	if !toggle.Checked || len(got) != 2 || !got[1] {
		t.Fatalf("second tap: checked=%v callbacks=%v", toggle.Checked, got)
	}
}
