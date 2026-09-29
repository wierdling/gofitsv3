package ui

import (
	"fmt"
	"sort"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/mosaic"
)

var normModeNames = []string{"Off", "Auto", "On"}

func normModeName(m mosaic.NormalizationMode) string {
	if int(m) >= 0 && int(m) < len(normModeNames) {
		return normModeNames[int(m)]
	}
	return normModeNames[0]
}

func normModeFromName(s string) mosaic.NormalizationMode {
	for i, name := range normModeNames {
		if name == s {
			return mosaic.NormalizationMode(i)
		}
	}
	return mosaic.NormOff
}

func exposureInputIdentity(in mosaic.Input) string {
	return fmt.Sprintf("%s#%dx%d", mosaic.InputKey(in), in.HDU.Data.Width, in.HDU.Data.Height)
}

func sameExposureInputIdentities(inputs []mosaic.Input, snapshot []string) bool {
	if len(inputs) != len(snapshot) {
		return false
	}
	for i, in := range inputs {
		if exposureInputIdentity(in) != snapshot[i] {
			return false
		}
	}
	return true
}

// showExposureReviewDialog presents the Exposure Normalization review for the
// given inputs grouped by filter. It mutates inputs in place on Save (Excluded,
// NormalizeExposure, ExposureScale) and reports the chosen mode via onApply.
func showExposureReviewDialog(win fyne.Window, inputs []mosaic.Input, mode mosaic.NormalizationMode, onApply func(mosaic.NormalizationMode)) {
	if len(inputs) == 0 {
		dialog.ShowInformation("Exposure Normalization", "No inputs loaded to review.", win)
		return
	}

	n := len(inputs)
	identities := make([]string, n)
	use := make([]bool, n)
	normalize := make([]bool, n)
	scale := make([]float64, n)

	// recomputeDefaults resets the per-item normalize/scale to the defaults for m.
	recomputeDefaults := func(m mosaic.NormalizationMode) {
		for i := range inputs {
			nm, sc := mosaic.NormalizationFor(m, inputs[i].BUnit, inputs[i].ExposureTime)
			normalize[i] = nm
			scale[i] = sc
		}
	}
	for i := range inputs {
		identities[i] = exposureInputIdentity(inputs[i])
		use[i] = !inputs[i].Excluded
		normalize[i] = inputs[i].NormalizeExposure
		scale[i] = inputs[i].ExposureScale
	}

	// Group input indices by filter name, filters sorted alphabetically.
	filterOf := func(i int) string {
		f := fitsio.FilterString(inputs[i].PrimaryHeader)
		if f == "" {
			return "Unknown"
		}
		return f
	}
	byFilter := make(map[string][]int)
	for i := range inputs {
		f := filterOf(i)
		byFilter[f] = append(byFilter[f], i)
	}
	filters := make([]string, 0, len(byFilter))
	for f := range byFilter {
		filters = append(filters, f)
	}
	sort.Strings(filters)

	scaleText := func(i int) string {
		if !use[i] || !normalize[i] {
			return "—"
		}
		if scale[i] > 0 {
			return fmt.Sprintf("1/%g = %.4g", inputs[i].ExposureTime, scale[i])
		}
		return "n/a"
	}
	bunitText := func(i int) string {
		if inputs[i].BUnit == "" {
			return "(missing)"
		}
		return inputs[i].BUnit
	}
	exptimeText := func(i int) string {
		if e := inputs[i].ExposureTime; e > 0 {
			return fmt.Sprintf("%g", e)
		}
		return "?"
	}

	// Live-updated widgets, indexed by input index.
	scaleLabels := make(map[int]*widget.Label, n)
	summaryLabels := make(map[string]*widget.Label, len(filters))

	refreshSummary := func(f string) {
		selected := 0
		total := 0.0
		anyNorm := false
		for _, i := range byFilter[f] {
			if !use[i] {
				continue
			}
			selected++
			if inputs[i].ExposureTime > 0 {
				total += inputs[i].ExposureTime
			}
			if normalize[i] {
				anyNorm = true
			}
		}
		status := "native units"
		if anyNorm {
			status = "normalize to e-/s"
		}
		summaryLabels[f].SetText(fmt.Sprintf("%s | %d selected | %gs total | %s", f, selected, total, status))
	}
	refreshScale := func(i int) {
		if lbl := scaleLabels[i]; lbl != nil {
			lbl.SetText(scaleText(i))
		}
	}

	header := func() *fyne.Container {
		return container.NewGridWithColumns(7,
			widget.NewLabelWithStyle("File / Input", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			widget.NewLabelWithStyle("Ext", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			widget.NewLabelWithStyle("EXPTIME", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			widget.NewLabelWithStyle("BUNIT", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			widget.NewLabelWithStyle("Use", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			widget.NewLabelWithStyle("Normalize", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			widget.NewLabelWithStyle("Scale Factor", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		)
	}

	sections := container.NewVBox()
	buildSections := func() {
		sections.Objects = sections.Objects[:0]
		for _, f := range filters {
			summary := widget.NewLabel("")
			summary.TextStyle = fyne.TextStyle{Bold: true}
			summaryLabels[f] = summary
			sections.Add(summary)
			sections.Add(header())
			for _, i := range byFilter[f] {
				i := i
				extLabel := "—"
				if inputs[i].SCIExt > 0 {
					extLabel = fmt.Sprintf("SCI,%d", inputs[i].SCIExt)
				}
				scaleLbl := widget.NewLabel(scaleText(i))
				scaleLabels[i] = scaleLbl

				useCheck := widget.NewCheck("", nil)
				useCheck.SetChecked(use[i])
				normCheck := widget.NewCheck("", nil)
				normCheck.SetChecked(normalize[i])

				useCheck.OnChanged = func(b bool) {
					use[i] = b
					refreshScale(i)
					refreshSummary(f)
				}
				normCheck.OnChanged = func(b bool) {
					normalize[i] = b
					refreshScale(i)
					refreshSummary(f)
				}

				sections.Add(container.NewGridWithColumns(7,
					widget.NewLabel(mosaic.InputLabel(inputs[i])),
					widget.NewLabel(extLabel),
					widget.NewLabel(exptimeText(i)),
					widget.NewLabel(bunitText(i)),
					useCheck,
					normCheck,
					scaleLbl,
				))
			}
			refreshSummary(f)
			sections.Add(widget.NewSeparator())
		}
		sections.Refresh()
	}

	modeSelect := NewSafeSelect(normModeNames, nil)
	modeSelect.SetSelected(normModeName(mode))
	curMode := mode
	modeSelect.OnChanged = func(s string) {
		curMode = normModeFromName(s)
		recomputeDefaults(curMode)
		// Reflect new defaults in every row's normalize checkbox and scale label.
		buildSections()
	}

	notes := widget.NewLabel(
		"Off: drizzle pixels in native units (existing behavior).\n" +
			"Auto: normalize only total-count BUNIT (ELECTRONS, COUNTS, DN, ADU); " +
			"rate units (…/S) and missing BUNIT are left unnormalized.\n" +
			"On: normalize every item with a valid EXPTIME.\n" +
			"Normalized pixels become ELECTRONS/S (pixel / EXPTIME). Items with a " +
			"missing or invalid EXPTIME are never normalized.")
	notes.TextStyle = fyne.TextStyle{Italic: true}
	notes.Wrapping = fyne.TextWrapWord

	buildSections()
	scroll := container.NewVScroll(sections)
	scroll.SetMinSize(fyne.NewSize(720, 360))

	content := container.NewBorder(
		container.NewVBox(
			widget.NewForm(widget.NewFormItem("Normalization Mode", modeSelect)),
			widget.NewSeparator(),
		),
		notes, nil, nil, scroll,
	)

	d := dialog.NewCustomConfirm("Exposure Normalization", "Apply", "Cancel", content, func(ok bool) {
		if !ok {
			return
		}
		if !sameExposureInputIdentities(inputs, identities) {
			dialog.ShowInformation("Inputs Changed", "The loaded inputs changed while this dialog was open. Reopen Exposure Normalization to review the current inputs.", win)
			return
		}
		for i := range inputs {
			inputs[i].Excluded = !use[i]
			inputs[i].NormalizeExposure = normalize[i] && use[i]
			inputs[i].ExposureScale = scale[i]
		}
		onApply(curMode)
	}, win)
	d.Show()
	d.Resize(fyne.NewSize(820, 620))
}
