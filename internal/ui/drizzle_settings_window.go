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


func defaultDrizzleSettings() models.DrizzleSettings {
	return models.DrizzleSettings{
		FinalScale:   0,
		Scale:        1.0,
		PixFrac:      1.0,
		CRMethod:     int(mosaic.CRMethodNone),
		SepKernel:    int(mosaic.KernelTurbo),
		FinalKernel:  int(mosaic.KernelSquare),
		CRSeedSNR:    4.0,
		CRDerivScale: 1.2,
	}
}

var kernelNames = []string{"Square", "Point", "Turbo", "Gaussian", "Tophat", "Lanczos2", "Lanczos3"}
var crMethodNames = []string{"None", "Legacy (single-frame)", "Drizzle-style (multi-frame)"}

// alignmentModeNames is also used by alignment_settings_window.go.
var alignmentModeNames = []string{"General Affine (legacy)", "RScale (legacy)", "TweakReg RScale", "TweakReg General"}

func kernelIndex(k int) int {
	if k >= 0 && k < len(kernelNames) {
		return k
	}
	return 0
}

func showDrizzleSettingsDialog(win fyne.Window, current models.DrizzleSettings, onSave func(models.DrizzleSettings)) {
	// Output scale: arcsec/pixel (preferred) or raw multiplier fallback.
	finalScaleEntry := widget.NewEntry()
	if current.FinalScale > 0 {
		finalScaleEntry.SetText(fmt.Sprintf("%.4f", current.FinalScale))
	}
	finalScaleEntry.SetPlaceHolder("e.g. 0.04 (leave blank to use multiplier)")

	scaleEntry := widget.NewEntry()
	scaleEntry.SetText(fmt.Sprintf("%.4f", current.Scale))

	pixFracEntry := widget.NewEntry()
	pixFracEntry.SetText(fmt.Sprintf("%.4f", current.PixFrac))

	crSelect := widget.NewSelect(crMethodNames, nil)
	crMethod := current.CRMethod
	if crMethod >= 0 && crMethod < len(crMethodNames) {
		crSelect.SetSelected(crMethodNames[crMethod])
	} else {
		crSelect.SetSelected(crMethodNames[0])
	}
	crSelect.OnChanged = func(s string) {
		for i, name := range crMethodNames {
			if name == s {
				crMethod = i
				break
			}
		}
	}

	sepSelect := widget.NewSelect(kernelNames, nil)
	sepKernel := current.SepKernel
	sepSelect.SetSelected(kernelNames[kernelIndex(sepKernel)])
	sepSelect.OnChanged = func(s string) {
		for i, name := range kernelNames {
			if name == s {
				sepKernel = i
				break
			}
		}
	}

	finalSelect := widget.NewSelect(kernelNames, nil)
	finalKernel := current.FinalKernel
	finalSelect.SetSelected(kernelNames[kernelIndex(finalKernel)])
	finalSelect.OnChanged = func(s string) {
		for i, name := range kernelNames {
			if name == s {
				finalKernel = i
				break
			}
		}
	}

	useERRWeighting := current.UseERRWeighting
	errWeightCheck := widget.NewCheck("", func(v bool) { useERRWeighting = v })
	errWeightCheck.SetChecked(useERRWeighting)

	crSeedSNREntry := widget.NewEntry()
	crSeedSNR := current.CRSeedSNR
	if crSeedSNR <= 0 {
		crSeedSNR = 4.0
	}
	crSeedSNREntry.SetText(fmt.Sprintf("%.2f", crSeedSNR))

	crDerivScaleEntry := widget.NewEntry()
	crDerivScale := current.CRDerivScale
	if crDerivScale <= 0 {
		crDerivScale = 1.2
	}
	crDerivScaleEntry.SetText(fmt.Sprintf("%.2f", crDerivScale))

	notes := widget.NewLabel(
		"Output Scale: desired plate scale in arcsec/pixel (AstroDrizzle final_scale).\n" +
			"  Smaller value = finer sampling = larger output image.\n" +
			"  e.g. native WFC3/UVIS ≈ 0.04; use 0.02 for 2× upsampling.\n" +
			"  Leave blank to use the Scale Multiplier instead.\n" +
			"Scale Multiplier: raw output/input pixel ratio (1.0 = native).\n" +
			"  Used only when Output Scale is blank.\n" +
			"PixFrac: drop size as fraction of pixel (1.0 = full coverage).\n" +
			"Sep Kernel: used during the per-frame drizzle pass.\n" +
			"Final Kernel: used during the final combination pass.\n" +
			"Lanczos kernels: only appropriate when PixFrac = 1.0.\n" +
			"CR Seed SNR: signal-to-noise threshold for seeding a CR candidate (default 4.0).\n" +
			"CR Deriv Scale: sharpness term weight in the CR rejection test (default 1.2).\n" +
			"  Both only apply when CR Method is Drizzle-style.",
	)
	notes.TextStyle = fyne.TextStyle{Italic: true}
	notes.Wrapping = fyne.TextWrapWord

	form := widget.NewForm(
		widget.NewFormItem("Output Scale (arcsec/px)", finalScaleEntry),
		widget.NewFormItem("Scale Multiplier", scaleEntry),
		widget.NewFormItem("PixFrac", pixFracEntry),
		widget.NewFormItem("CR Method", crSelect),
		widget.NewFormItem("Sep Kernel", sepSelect),
		widget.NewFormItem("Final Kernel", finalSelect),
		widget.NewFormItem("ERR Inverse-Variance Weighting", errWeightCheck),
		widget.NewFormItem("CR Seed SNR", crSeedSNREntry),
		widget.NewFormItem("CR Deriv Scale", crDerivScaleEntry),
	)

	content := container.NewVBox(form, notes)

	d := dialog.NewCustomConfirm("Drizzle Settings", "Save", "Cancel", content, func(ok bool) {
		if !ok {
			return
		}

		var finalScale float64
		fsText := strings.TrimSpace(finalScaleEntry.Text)
		if fsText != "" {
			v, err := strconv.ParseFloat(fsText, 64)
			if err != nil || v <= 0 {
				dialog.ShowInformation("Invalid Value", "Output Scale must be a positive number in arcsec/pixel.", win)
				return
			}
			finalScale = v
		}

		scale, errS := strconv.ParseFloat(strings.TrimSpace(scaleEntry.Text), 64)
		if errS != nil || scale <= 0 {
			dialog.ShowInformation("Invalid Value", "Scale Multiplier must be a positive number.", win)
			return
		}

		pixFrac, errP := strconv.ParseFloat(strings.TrimSpace(pixFracEntry.Text), 64)
		if errP != nil || pixFrac <= 0 || pixFrac > 1 {
			dialog.ShowInformation("Invalid Value", "PixFrac must be between 0 (exclusive) and 1.", win)
			return
		}

		crSNRVal, errCSNR := strconv.ParseFloat(strings.TrimSpace(crSeedSNREntry.Text), 64)
		if errCSNR != nil || crSNRVal <= 0 {
			dialog.ShowInformation("Invalid Value", "CR Seed SNR must be a positive number.", win)
			return
		}

		crDSVal, errCDS := strconv.ParseFloat(strings.TrimSpace(crDerivScaleEntry.Text), 64)
		if errCDS != nil || crDSVal <= 0 {
			dialog.ShowInformation("Invalid Value", "CR Deriv Scale must be a positive number.", win)
			return
		}

		onSave(models.DrizzleSettings{
			FinalScale:      finalScale,
			Scale:           scale,
			PixFrac:         pixFrac,
			CRMethod:        crMethod,
			SepKernel:       sepKernel,
			FinalKernel:     finalKernel,
			UseERRWeighting: useERRWeighting,
			CRSeedSNR:       crSNRVal,
			CRDerivScale:    crDSVal,
		})
	}, win)
	d.Show()
	d.Resize(fyne.NewSize(760, 510))
}
