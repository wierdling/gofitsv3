package ui

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	fynetest "fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

func TestComposeControlsAccordionStartsWithChannelsOpenAndTogglesIndependently(t *testing.T) {
	app := fynetest.NewApp()
	t.Cleanup(app.Quit)

	win := fynetest.NewWindow(nil)
	root, _ := newComposeWorkspace(app, win)
	accordion := composeControlsAccordion(t, root)

	if !accordion.MultiOpen {
		t.Fatal("Compose controls accordion does not allow multiple open sections")
	}
	wantTitles := []string{"Options", "Channels"}
	if len(accordion.Items) != len(wantTitles) {
		t.Fatalf("accordion item count = %d, want %d", len(accordion.Items), len(wantTitles))
	}
	for i, want := range wantTitles {
		if got := accordion.Items[i].Title; got != want {
			t.Errorf("accordion item %d title = %q, want %q", i, got, want)
		}
	}
	if accordion.Items[0].Open {
		t.Error("Options section is open initially, want collapsed")
	}
	if !accordion.Items[1].Open {
		t.Error("Channels section is closed initially, want open")
	}

	first, second := accordion.Items[0].Detail, accordion.Items[1].Detail
	if !containsButton(first, "Reset") {
		t.Error("Options detail does not contain the Reset button")
	}
	if containsLabel(first, "Build color composite") {
		t.Error("Options detail contains Build color composite")
	}
	if containsLabeledToggle(first, "Build color composite") {
		t.Error("Options detail contains a Build color composite checkbox")
	}
	if !containsLabel(second, "Build color composite") {
		t.Error("Channels detail does not contain Build color composite")
	}
	if !containsLabeledToggle(second, "Build color composite") {
		t.Error("Channels detail does not contain a Build color composite checkbox")
	}
	if !containsType[*ChannelTabs](second) {
		t.Error("Channels detail does not contain channel tabs")
	}

	accordion.Open(0)
	if !accordion.Items[0].Open || !accordion.Items[1].Open {
		t.Error("opening Options did not preserve the open Channels section")
	}
	accordion.Close(1)
	if !accordion.Items[0].Open || accordion.Items[1].Open {
		t.Error("closing Channels changed the Options section state")
	}
	accordion.Open(1)
	if !accordion.Items[0].Open || !accordion.Items[1].Open {
		t.Error("reopening Channels changed the Options section state")
	}
}

func composeControlsAccordion(t *testing.T, root fyne.CanvasObject) *widget.Accordion {
	t.Helper()
	if split, ok := root.(*container.Split); ok {
		root = split.Leading
	}
	if accordion := findAccordion(root); accordion != nil {
		return accordion
	}
	t.Fatal("Compose controls do not contain an accordion")
	return nil
}

func containsButton(object fyne.CanvasObject, text string) bool {
	if button, ok := object.(*widget.Button); ok {
		return button.Text == text
	}
	return containsObject(object, func(child fyne.CanvasObject) bool {
		button, ok := child.(*widget.Button)
		return ok && button.Text == text
	})
}

func containsLabel(object fyne.CanvasObject, text string) bool {
	return containsObject(object, func(child fyne.CanvasObject) bool {
		label, ok := child.(*widget.Label)
		return ok && label.Text == text
	})
}

func containsType[T fyne.CanvasObject](object fyne.CanvasObject) bool {
	return containsObject(object, func(child fyne.CanvasObject) bool {
		_, ok := child.(T)
		return ok
	})
}

func containsLabeledToggle(object fyne.CanvasObject, labelText string) bool {
	if containerObject, ok := object.(*fyne.Container); ok {
		hasLabel := false
		hasToggle := false
		for _, child := range containerObject.Objects {
			if label, ok := child.(*widget.Label); ok && label.Text == labelText {
				hasLabel = true
			}
			if _, ok := child.(*Toggle); ok {
				hasToggle = true
			}
		}
		if hasLabel && hasToggle {
			return true
		}
		for _, child := range containerObject.Objects {
			if containsLabeledToggle(child, labelText) {
				return true
			}
		}
	}
	if scroll, ok := object.(*container.Scroll); ok {
		return containsLabeledToggle(scroll.Content, labelText)
	}
	return false
}

func containsObject(object fyne.CanvasObject, match func(fyne.CanvasObject) bool) bool {
	if match(object) {
		return true
	}
	switch object := object.(type) {
	case *fyne.Container:
		for _, child := range object.Objects {
			if containsObject(child, match) {
				return true
			}
		}
	case *container.Scroll:
		return containsObject(object.Content, match)
	}
	return false
}
