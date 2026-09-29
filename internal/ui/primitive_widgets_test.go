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

func TestNumberEntryAllowsTypingNegativeFraction(t *testing.T) {
	n := NewNumberEntry(0.001, 4)

	n.entry.SetText("-0")
	if n.entry.Text != "-0" {
		t.Fatalf("text after negative zero = %q, want to preserve intermediate input", n.entry.Text)
	}
	n.entry.SetText("-0.001")
	if got := n.Value(); got != -0.001 {
		t.Fatalf("Value() = %v, want -0.001", got)
	}
}

func TestNumberEntryPreservesManualDecimalPrecisionAndRejectsInvalidLexicalInput(t *testing.T) {
	n := NewNumberEntry(0.1, 1)

	n.entry.SetText(".25")
	if got := n.Value(); got != .25 {
		t.Fatalf("Value() = %v, want .25", got)
	}
	if got := n.entry.Text; got != ".25" {
		t.Fatalf("text after manual decimal = %q, want .25", got)
	}

	n.entry.SetText(".0001.001")
	if got := n.entry.Text; got != ".25" {
		t.Fatalf("text after invalid decimal = %q, want previous valid text .25", got)
	}
	n.entry.SetText("1e")
	if got := n.entry.Text; got != "1e" {
		t.Fatalf("text after exponent intermediate = %q, want 1e", got)
	}
	n.entry.SetText("1e-3")
	if got := n.Value(); got != .001 {
		t.Fatalf("Value() after exponent = %v, want .001", got)
	}
	n.entry.SetText("1e999")
	if got := n.entry.Text; got != "1e-3" {
		t.Fatalf("text after exponent overflow = %q, want previous valid text 1e-3", got)
	}

	n.entry.SetText("-")
	if got := n.entry.Text; got != "-" {
		t.Fatalf("text after negative intermediate = %q, want -", got)
	}
	n.entry.SetText("-.0")
	if got := n.entry.Text; got != "-.0" {
		t.Fatalf("text after negative fractional intermediate = %q, want -.0", got)
	}

	n.SetValue(.25)
	if got := n.entry.Text; got != "0.3" {
		t.Fatalf("programmatic text = %q, want rounded 0.3", got)
	}
}

func TestNumberEntryManualClampDisplaysClampedValue(t *testing.T) {
	n := NewNumberEntry(0.1, 2)
	n.Min = 0
	n.Max = 1
	n.entry.SetText("1.25")
	if n.Value() != 1 || n.entry.Text != "1.00" {
		t.Fatalf("clamped entry = value %v, text %q; want 1 and 1.00", n.Value(), n.entry.Text)
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
