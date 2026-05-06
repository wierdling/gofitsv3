package ui

import (
	"fmt"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/models"
	"gofitsv3/internal/mosaic"
)

func defaultAlignmentSettings() models.AlignmentSettings {
	return models.AlignmentSettings{
		AlignmentMode:      int(mosaic.AlignmentModeTweakRegRScale),
		SearchRadiusArcsec: 1.5,
		NumRefs:            1,
	}
}

func showAlignmentSettingsDialog(win fyne.Window, current models.AlignmentSettings, onSave func(models.AlignmentSettings)) {
	alignSelect := NewSafeSelect(alignmentModeNames, nil)
	alignmentMode := current.AlignmentMode
	if alignmentMode >= 0 && alignmentMode < len(alignmentModeNames) {
		alignSelect.SetSelected(alignmentModeNames[alignmentMode])
	} else {
		alignSelect.SetSelected(alignmentModeNames[int(mosaic.AlignmentModeTweakRegRScale)])
	}
	alignSelect.OnChanged = func(s string) {
		for i, name := range alignmentModeNames {
			if name == s {
				alignmentMode = i
				break
			}
		}
	}

	searchRadiusEntry := widget.NewEntry()
	searchRadius := current.SearchRadiusArcsec
	if searchRadius <= 0 {
		searchRadius = 1.5
	}
	searchRadiusEntry.SetText(fmt.Sprintf("%.2f", searchRadius))

	numRefsEntry := widget.NewEntry()
	numRefs := current.NumRefs
	if numRefs < 1 {
		numRefs = 1
	}
	numRefsEntry.SetText(fmt.Sprintf("%d", numRefs))

	notes := widget.NewLabel(
		"Alignment Mode: TweakReg modes use catalog matching via full WCS (recommended).\n" +
			"  Legacy modes use image warping and are kept for backward compatibility.\n" +
			"  RScale = shift + rotation + uniform scale; General = full 6-parameter affine.\n" +
			"Search Radius: TweakReg catalog matching tolerance in arcseconds (default 1.5).\n" +
			"Num Reference Images: first N images are treated as pre-aligned references.\n" +
			"  Each non-reference image aligns to whichever reference it overlaps.\n" +
			"  Use 2 for a two-chip detector where each chip is loaded separately.",
	)
	notes.TextStyle = fyne.TextStyle{Italic: true}
	notes.Wrapping = fyne.TextWrapWord

	form := widget.NewForm(
		widget.NewFormItem("Alignment Mode", alignSelect),
		widget.NewFormItem("Search Radius (arcsec)", searchRadiusEntry),
		widget.NewFormItem("Num Reference Images", numRefsEntry),
	)

	content := container.NewVBox(form, notes)
	d := dialog.NewCustomConfirm("Alignment Settings", "Save", "Cancel", content, func(ok bool) {
		if !ok {
			return
		}

		srVal, errSR := strconv.ParseFloat(strings.TrimSpace(searchRadiusEntry.Text), 64)
		if errSR != nil || srVal <= 0 {
			dialog.ShowInformation("Invalid Value", "Search Radius must be a positive number in arcseconds.", win)
			return
		}

		nrVal, errNR := strconv.Atoi(strings.TrimSpace(numRefsEntry.Text))
		if errNR != nil || nrVal < 1 {
			dialog.ShowInformation("Invalid Value", "Num Reference Images must be a positive integer.", win)
			return
		}

		onSave(models.AlignmentSettings{
			AlignmentMode:      alignmentMode,
			SearchRadiusArcsec: srVal,
			NumRefs:            nrVal,
		})
	}, win)
	d.Resize(fyne.NewSize(700, 380))
	d.Show()
}
