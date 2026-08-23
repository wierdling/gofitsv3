package ui

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/instrument"
	"gofitsv3/internal/models"
	"gofitsv3/internal/mosaic"
)

func finitePositive(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v > 0 }
func finiteUnit(v float64) bool     { return !math.IsNaN(v) && !math.IsInf(v, 0) && v > 0 && v <= 1 }

func defaultDrizzleSettings() models.DrizzleSettings {
	return models.DrizzleSettings{
		FinalScale:            0,
		Scale:                 1.0,
		PixFrac:               1.0,
		CRMethod:              int(mosaic.CRMethodDrizzle),
		SepKernel:             int(mosaic.KernelTurbo),
		FinalKernel:           int(mosaic.KernelSquare),
		WeightingMode:         int(mosaic.WeightUniform),
		SurfaceBrightnessNorm: false,
		CRSeedSNR:             4.0,
		CRDerivScale:          1.2,
		DebugOutputDir:        "",
	}
}

var kernelNames = []string{"Square", "Point", "Turbo", "Gaussian", "Tophat", "Lanczos2", "Lanczos3"}
var crMethodNames = []string{"None", "Drizzle-style (multi-frame)"}
var weightingModeNames = []string{"Uniform", "Exposure Time", "ERR Inverse-Variance"}

// alignmentModeNames is also used by alignment_settings_window.go.
var alignmentModeNames = []string{"General Affine (legacy)", "RScale (legacy)", "TweakReg RScale", "TweakReg General"}

func kernelIndex(k int) int {
	if k >= 0 && k < len(kernelNames) {
		return k
	}
	return 0
}

// scalePresetCustom and scalePresetAuto are the two non-instrument entries in
// the Output Scale preset menu.
const (
	scalePresetCustom = "Custom / multiplier"
	scalePresetAuto   = "Auto — match finest input"
)

// finestInputScale returns the smallest native plate scale (arcsec/pixel) among
// the non-excluded inputs. ok is false when no input yields a usable scale.
func finestInputScale(inputs []mosaic.Input) (float64, bool) {
	best := 0.0
	found := false
	for _, in := range inputs {
		if in.Excluded {
			continue
		}
		ps, ok := mosaic.NativePlateScaleArcsec(in)
		if !ok || ps <= 0 {
			continue
		}
		if !found || ps < best {
			best = ps
			found = true
		}
	}
	return best, found
}

func showDrizzleSettingsDialog(win fyne.Window, current models.DrizzleSettings, inputs []mosaic.Input, onSave func(models.DrizzleSettings)) {
	// Output scale: arcsec/pixel (preferred) or raw multiplier fallback.
	finalScaleEntry := widget.NewEntry()
	if current.FinalScale > 0 {
		finalScaleEntry.SetText(fmt.Sprintf("%.4f", current.FinalScale))
	}
	finalScaleEntry.SetPlaceHolder("e.g. 0.04 (leave blank to use multiplier)")

	// Preset menu that fills the Output Scale field with a known instrument
	// plate scale (or the finest loaded input). The field stays editable.
	presetOptions := []string{scalePresetCustom, scalePresetAuto}
	presetScale := map[string]float64{}
	for _, p := range instrument.ScalePresets() {
		label := fmt.Sprintf("%s (%.4g\")", p.Name, p.PixelScale)
		presetOptions = append(presetOptions, label)
		presetScale[label] = p.PixelScale
	}
	presetSelect := NewSafeSelect(presetOptions, nil)
	presetSelect.SetSelected(scalePresetCustom)
	presetSelect.OnChanged = func(s string) {
		switch s {
		case scalePresetCustom:
			return
		case scalePresetAuto:
			ps, ok := finestInputScale(inputs)
			if !ok {
				dialog.ShowInformation("No Plate Scale",
					"Could not determine a plate scale from the loaded inputs (missing WCS and unrecognised instrument).", win)
				presetSelect.SetSelected(scalePresetCustom)
				return
			}
			finalScaleEntry.SetText(fmt.Sprintf("%.4f", ps))
		default:
			if v, ok := presetScale[s]; ok {
				finalScaleEntry.SetText(fmt.Sprintf("%.4f", v))
			}
		}
	}

	scaleEntry := widget.NewEntry()
	scaleEntry.SetText(fmt.Sprintf("%.4f", current.Scale))

	pixFracEntry := widget.NewEntry()
	pixFracEntry.SetText(fmt.Sprintf("%.4f", current.PixFrac))

	crSelect := NewSafeSelect(crMethodNames, nil)
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

	sepSelect := NewSafeSelect(kernelNames, nil)
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

	finalSelect := NewSafeSelect(kernelNames, nil)
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

	weightingMode := current.WeightingMode
	if weightingMode == 0 && current.UseERRWeighting {
		weightingMode = int(mosaic.WeightERR)
	}
	weightSelect := NewSafeSelect(weightingModeNames, nil)
	if weightingMode < 0 || weightingMode >= len(weightingModeNames) {
		weightingMode = int(mosaic.WeightUniform)
	}
	weightSelect.SetSelected(weightingModeNames[weightingMode])
	weightSelect.OnChanged = func(s string) {
		for i, name := range weightingModeNames {
			if name == s {
				weightingMode = i
				break
			}
		}
	}

	lockFrameCheck := widget.NewCheck("Lock output to reference baseline (match its size, rotation, scale)", nil)
	lockFrameCheck.SetChecked(current.LockToReferenceFrame)

	sbNormCheck := widget.NewCheck("Normalize mixed-scale chips by surface brightness", nil)
	sbNormCheck.SetChecked(current.SurfaceBrightnessNorm)

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

	debugDirEntry := widget.NewEntry()
	debugDirEntry.SetText(current.DebugOutputDir)
	debugDirEntry.SetPlaceHolder("Optional: path to save debug chip FITS")

	notes := widget.NewLabel(
		"Scale Preset: fills Output Scale from a known instrument plate scale, or\n" +
			"  \"Auto — match finest input\" to use the sharpest loaded frame. Editable after.\n" +
			"Output Scale: desired plate scale in arcsec/pixel (AstroDrizzle final_scale).\n" +
			"  Smaller value = finer sampling = larger output image.\n" +
			"  e.g. native WFC3/UVIS ≈ 0.04; use 0.02 for 2× upsampling.\n" +
			"  Leave blank to use the Scale Multiplier instead.\n" +
			"Lock to Reference: pins the output to the Set Reference Baseline frame\n" +
			"  (exact size, rotation, and plate scale). Use it so each channel drizzles\n" +
			"  onto the same grid for compositing. Overrides Output Scale / Multiplier.\n" +
			"Scale Multiplier: raw output/input pixel ratio (1.0 = native).\n" +
			"  Used only when Output Scale is blank.\n" +
			"PixFrac: drop size as fraction of pixel (1.0 = full coverage).\n" +
			"Sep Kernel: used during the per-frame drizzle pass.\n" +
			"Final Kernel: used during the final combination pass.\n" +
			"Weighting: Uniform ignores EXPTIME; Exposure Time matches classic drizzle EXP weighting; ERR uses the ERR plane.\n" +
			"Surface brightness normalization: for mixed-scale chips such as WFPC2 PC+WF, scales chip pixels by mapped pixel area before drizzle.\n" +
			"Lanczos kernels: only appropriate when PixFrac = 1.0.\n" +
			"CR Seed SNR: signal-to-noise threshold for seeding a CR candidate (default 4.0).\n" +
			"CR Deriv Scale: sharpness term weight in the CR rejection test (default 1.2).\n" +
			"  Both only apply when CR Method is Drizzle-style.",
	)
	notes.TextStyle = fyne.TextStyle{Italic: true}
	notes.Wrapping = fyne.TextWrapWord

	form := widget.NewForm(
		widget.NewFormItem("Scale Preset", presetSelect),
		widget.NewFormItem("Output Scale (arcsec/px)", finalScaleEntry),
		widget.NewFormItem("Lock to Reference", lockFrameCheck),
		widget.NewFormItem("Scale Multiplier", scaleEntry),
		widget.NewFormItem("PixFrac", pixFracEntry),
		widget.NewFormItem("CR Method", crSelect),
		widget.NewFormItem("Sep Kernel", sepSelect),
		widget.NewFormItem("Final Kernel", finalSelect),
		widget.NewFormItem("Weighting", weightSelect),
		widget.NewFormItem("Surface Brightness", sbNormCheck),
		widget.NewFormItem("CR Seed SNR", crSeedSNREntry),
		widget.NewFormItem("CR Deriv Scale", crDerivScaleEntry),
		widget.NewFormItem("Debug Output Dir", debugDirEntry),
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
			if err != nil || !finitePositive(v) {
				dialog.ShowInformation("Invalid Value", "Output Scale must be a positive number in arcsec/pixel.", win)
				return
			}
			finalScale = v
		}

		scale, errS := strconv.ParseFloat(strings.TrimSpace(scaleEntry.Text), 64)
		if errS != nil || !finitePositive(scale) {
			dialog.ShowInformation("Invalid Value", "Scale Multiplier must be a positive number.", win)
			return
		}

		pixFrac, errP := strconv.ParseFloat(strings.TrimSpace(pixFracEntry.Text), 64)
		if errP != nil || !finiteUnit(pixFrac) {
			dialog.ShowInformation("Invalid Value", "PixFrac must be between 0 (exclusive) and 1.", win)
			return
		}

		crSNRVal, errCSNR := strconv.ParseFloat(strings.TrimSpace(crSeedSNREntry.Text), 64)
		if errCSNR != nil || !finitePositive(crSNRVal) {
			dialog.ShowInformation("Invalid Value", "CR Seed SNR must be a positive number.", win)
			return
		}

		crDSVal, errCDS := strconv.ParseFloat(strings.TrimSpace(crDerivScaleEntry.Text), 64)
		if errCDS != nil || !finitePositive(crDSVal) {
			dialog.ShowInformation("Invalid Value", "CR Deriv Scale must be a positive number.", win)
			return
		}

		onSave(models.DrizzleSettings{
			FinalScale:            finalScale,
			LockToReferenceFrame:  lockFrameCheck.Checked,
			Scale:                 scale,
			PixFrac:               pixFrac,
			CRMethod:              crMethod,
			SepKernel:             sepKernel,
			FinalKernel:           finalKernel,
			WeightingMode:         weightingMode,
			SurfaceBrightnessNorm: sbNormCheck.Checked,
			CRSeedSNR:             crSNRVal,
			CRDerivScale:          crDSVal,
			DebugOutputDir:        strings.TrimSpace(debugDirEntry.Text),
		})
	}, win)
	d.Show()
	d.Resize(fyne.NewSize(760, 510))
}
