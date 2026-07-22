package ui

import (
	"image/color"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

func TestComposeMagicRowsDoNotStretchToFillScrollViewport(t *testing.T) {
	rowList := container.NewVBox()
	var rows []*fyne.Container
	for _, filter := range []string{"F435W", "F555W", "F658N", "F814W"} {
		row := newComposeMagicDialogRow(
			widget.NewLabel(filter),
			widget.NewLabel(filter+"_drz.fits"),
			widget.NewSelect(composeMagicAssignments, nil),
			widget.NewButton("Choose...", nil),
		)
		rows = append(rows, row)
		rowList.Add(row)
	}
	rowsScroll := newComposeMagicRowsScroll(rowList)
	rowsScroll.Resize(fyne.NewSize(720, 360))

	for i, row := range rows {
		if got, want := row.Size().Height, row.MinSize().Height; got != want {
			t.Errorf("row %d height = %.1f after viewport resize, want minimum height %.1f", i, got, want)
		}
		if i > 0 && row.Position().Y <= rows[i-1].Position().Y {
			t.Errorf("row %d position = %.1f, want below row %d at %.1f", i, row.Position().Y, i-1, rows[i-1].Position().Y)
		}
	}
}

func TestOpaqueComposeMagicColorPreservesUnpremultipliedRGB(t *testing.T) {
	chosen := color.NRGBA{R: 120, G: 60, B: 30, A: 64}
	got := opaqueComposeMagicColor(chosen)
	want := color.NRGBA{R: 120, G: 60, B: 30, A: 255}
	if got != want {
		t.Fatalf("opaqueComposeMagicColor(%#v) = %#v, want %#v", chosen, got, want)
	}
}
