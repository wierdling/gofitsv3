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

func defaultSkysubSettings() models.SkysubSettings {
	return models.SkysubSettings{
		Enabled:                false,
		SkyMethod:              int(mosaic.SkyMethodLocalMin),
		SkyStat:                int(mosaic.SkyStatMedian),
		SkyWidth:               0.1,
		SkyClip:                5,
		SkyLSigma:              4.0,
		SkyUSigma:              4.0,
		SkyLowerSet:            false,
		SkyUpperSet:            false,
		RowDestripeMaskSigma:   3.0,
		RowDestripeTrendWindow: 129,
		RowDestripeDirection:   "rows",
		NIRCamWispAutoScale:    true,
	}
}

var skyMethodNames = []string{"localmin", "globalmin", "match", "globalmin+match", "match+plane"}
var skyStatNames = []string{"median", "mode", "mean"}

func showSkysubSettingsDialog(win fyne.Window, current models.SkysubSettings, onSave func(models.SkysubSettings)) {
	enabled := current.Enabled
	enabledCheck := widget.NewCheck("Enable AstroDrizzle-style sky subtraction before CR rejection and drizzle.", func(v bool) {
		enabled = v
	})
	enabledCheck.SetChecked(enabled)

	artifactSettings := current
	artifactSummary := widget.NewLabel(jwstArtifactSummary(artifactSettings))
	artifactSummary.Wrapping = fyne.TextWrapWord
	artifactButton := widget.NewButton("JWST Artifact Corrections...", func() {
		showJWSTArtifactSettingsDialog(win, artifactSettings, func(saved models.SkysubSettings) {
			artifactSettings = copyJWSTArtifactSettings(artifactSettings, saved)
			artifactSummary.SetText(jwstArtifactSummary(artifactSettings))
		})
	})

	method := current.SkyMethod
	if method < 0 || method >= len(skyMethodNames) {
		method = int(mosaic.SkyMethodLocalMin)
	}
	methodSelect := NewSafeSelect(skyMethodNames, func(s string) {
		for i, name := range skyMethodNames {
			if name == s {
				method = i
				break
			}
		}
	})
	methodSelect.SetSelected(skyMethodNames[method])

	stat := current.SkyStat
	if stat < 0 || stat >= len(skyStatNames) {
		stat = int(mosaic.SkyStatMedian)
	}
	statSelect := NewSafeSelect(skyStatNames, func(s string) {
		for i, name := range skyStatNames {
			if name == s {
				stat = i
				break
			}
		}
	})
	statSelect.SetSelected(skyStatNames[stat])

	widthEntry := widget.NewEntry()
	width := current.SkyWidth
	if width <= 0 {
		width = 0.1
	}
	widthEntry.SetText(fmt.Sprintf("%.3f", width))

	lowerEntry := widget.NewEntry()
	if current.SkyLowerSet {
		lowerEntry.SetText(fmt.Sprintf("%.4f", current.SkyLower))
	}
	lowerEntry.SetPlaceHolder("blank = no lower cutoff")

	upperEntry := widget.NewEntry()
	if current.SkyUpperSet {
		upperEntry.SetText(fmt.Sprintf("%.4f", current.SkyUpper))
	}
	upperEntry.SetPlaceHolder("blank = no upper cutoff")

	clipEntry := widget.NewEntry()
	clip := current.SkyClip
	if clip <= 0 {
		clip = 5
	}
	clipEntry.SetText(fmt.Sprintf("%d", clip))

	lSigmaEntry := widget.NewEntry()
	lSigma := current.SkyLSigma
	if lSigma <= 0 {
		lSigma = 4.0
	}
	lSigmaEntry.SetText(fmt.Sprintf("%.2f", lSigma))

	uSigmaEntry := widget.NewEntry()
	uSigma := current.SkyUSigma
	if uSigma <= 0 {
		uSigma = 4.0
	}
	uSigmaEntry.SetText(fmt.Sprintf("%.2f", uSigma))

	notes := widget.NewLabel(
		"skymethod: localmin/globalmin/match/globalmin+match follow AstroDrizzle naming.\n" +
			"match finds relative frame offsets from overlaps; match+plane fits a relative offset plus gradient from overlap differences only.\n" +
			"Use JWST Artifact Corrections for detector-fixed NIRCam/MIRI cleanup before sky matching.\n" +
			"skywidth: histogram bin width in sigma for mode estimation.\n" +
			"skylower/skyupper: optional pixel cutoffs in the input image units.\n" +
			"skyclip, skylsigma, skyusigma: iterative clipping controls for sky estimation.",
	)
	notes.TextStyle = fyne.TextStyle{Italic: true}
	notes.Wrapping = fyne.TextWrapWord

	form := widget.NewForm(
		widget.NewFormItem("Enabled", enabledCheck),
		widget.NewFormItem("JWST artifacts", container.NewVBox(artifactButton, artifactSummary)),
		widget.NewFormItem("skymethod", methodSelect),
		widget.NewFormItem("skystat", statSelect),
		widget.NewFormItem("skywidth", widthEntry),
		widget.NewFormItem("skylower", lowerEntry),
		widget.NewFormItem("skyupper", upperEntry),
		widget.NewFormItem("skyclip", clipEntry),
		widget.NewFormItem("skylsigma", lSigmaEntry),
		widget.NewFormItem("skyusigma", uSigmaEntry),
	)

	content := container.NewVBox(form, notes)
	d := dialog.NewCustomConfirm("Skysub Settings", "Save", "Cancel", content, func(ok bool) {
		if !ok {
			return
		}

		widthVal, errWidth := strconv.ParseFloat(strings.TrimSpace(widthEntry.Text), 64)
		if errWidth != nil || widthVal <= 0 {
			dialog.ShowInformation("Invalid Value", "skywidth must be a positive number.", win)
			return
		}

		clipVal, errClip := strconv.Atoi(strings.TrimSpace(clipEntry.Text))
		if errClip != nil || clipVal < 0 {
			dialog.ShowInformation("Invalid Value", "skyclip must be zero or a positive integer.", win)
			return
		}

		lSigmaVal, errLS := strconv.ParseFloat(strings.TrimSpace(lSigmaEntry.Text), 64)
		if errLS != nil || lSigmaVal <= 0 {
			dialog.ShowInformation("Invalid Value", "skylsigma must be a positive number.", win)
			return
		}

		uSigmaVal, errUS := strconv.ParseFloat(strings.TrimSpace(uSigmaEntry.Text), 64)
		if errUS != nil || uSigmaVal <= 0 {
			dialog.ShowInformation("Invalid Value", "skyusigma must be a positive number.", win)
			return
		}

		lowerText := strings.TrimSpace(lowerEntry.Text)
		upperText := strings.TrimSpace(upperEntry.Text)
		lowerVal := 0.0
		upperVal := 0.0
		lowerSet := false
		upperSet := false
		if lowerText != "" {
			v, err := strconv.ParseFloat(lowerText, 64)
			if err != nil {
				dialog.ShowInformation("Invalid Value", "skylower must be blank or a number.", win)
				return
			}
			lowerVal = v
			lowerSet = true
		}
		if upperText != "" {
			v, err := strconv.ParseFloat(upperText, 64)
			if err != nil {
				dialog.ShowInformation("Invalid Value", "skyupper must be blank or a number.", win)
				return
			}
			upperVal = v
			upperSet = true
		}
		if lowerSet && upperSet && lowerVal > upperVal {
			dialog.ShowInformation("Invalid Value", "skylower cannot be greater than skyupper.", win)
			return
		}

		saved := models.SkysubSettings{
			Enabled:     enabled,
			SkyMethod:   method,
			SkyStat:     stat,
			SkyWidth:    widthVal,
			SkyLower:    lowerVal,
			SkyUpper:    upperVal,
			SkyLowerSet: lowerSet,
			SkyUpperSet: upperSet,
			SkyClip:     clipVal,
			SkyLSigma:   lSigmaVal,
			SkyUSigma:   uSigmaVal,
		}
		onSave(copyJWSTArtifactSettings(saved, artifactSettings))
	}, win)
	d.Resize(fyne.NewSize(700, 500))
	d.Show()
}

func showJWSTArtifactSettingsDialog(win fyne.Window, current models.SkysubSettings, onSave func(models.SkysubSettings)) {
	ampPedestal := current.AmpPedestal
	ampPedestalCheck := widget.NewCheck("Remove NIRCam per-amplifier pedestal.", func(v bool) {
		ampPedestal = v
	})
	ampPedestalCheck.SetChecked(ampPedestal)

	rowDestripe := current.RowDestripe
	rowDestripeCheck := widget.NewCheck("Remove NIRCam 1/f row banding.", func(v bool) {
		rowDestripe = v
	})
	rowDestripeCheck.SetChecked(rowDestripe)

	rowMaskEntry := widget.NewEntry()
	rowMaskEntry.SetText(current.RowDestripeMaskPath)
	rowMaskEntry.SetPlaceHolder("optional binary FITS mask; nonzero = excluded")

	rowMaskDirEntry := widget.NewEntry()
	rowMaskDirEntry.SetText(current.RowDestripeMaskDir)
	rowMaskDirEntry.SetPlaceHolder("folder containing <input-stem>_rowmask.fits")

	rowMaskSigmaEntry := widget.NewEntry()
	rowMaskSigma := current.RowDestripeMaskSigma
	if rowMaskSigma <= 0 {
		rowMaskSigma = 3.0
	}
	rowMaskSigmaEntry.SetText(fmt.Sprintf("%.2f", rowMaskSigma))

	rowTrendEntry := widget.NewEntry()
	rowTrend := current.RowDestripeTrendWindow
	if rowTrend <= 0 {
		rowTrend = 129
	}
	rowTrendEntry.SetText(fmt.Sprintf("%d", rowTrend))

	nircamWisp := current.NIRCamWisp
	nircamWispCheck := widget.NewCheck("Subtract NIRCam wisp template (local files only).", func(v bool) {
		nircamWisp = v
	})
	nircamWispCheck.SetChecked(nircamWisp)

	wispDirEntry := widget.NewEntry()
	wispDirEntry.SetText(current.NIRCamWispTemplateDir)
	wispDirEntry.SetPlaceHolder("folder containing nircam_wisp_nrcb4_f200w.fits")

	wispAutoScale := current.NIRCamWispAutoScale
	if !current.NIRCamWisp && !current.NIRCamWispAutoScale && current.NIRCamWispScale == 0 {
		wispAutoScale = true
	}
	wispAutoScaleCheck := widget.NewCheck("Auto-scale template", func(v bool) {
		wispAutoScale = v
	})
	wispAutoScaleCheck.SetChecked(wispAutoScale)

	wispScaleEntry := widget.NewEntry()
	wispScaleEntry.SetText(fmt.Sprintf("%.6g", current.NIRCamWispScale))
	wispScaleEntry.SetPlaceHolder("fixed non-negative scale")

	miriArtifactMask := current.MIRIArtifactMask
	miriArtifactMaskCheck := widget.NewCheck("Apply MIRI artifact mask (user-provided masks only; no shower subtraction).", func(v bool) {
		miriArtifactMask = v
	})
	miriArtifactMaskCheck.SetChecked(miriArtifactMask)

	miriMaskPathEntry := widget.NewEntry()
	miriMaskPathEntry.SetText(current.MIRIArtifactMaskPath)
	miriMaskPathEntry.SetPlaceHolder("optional direct binary FITS mask")

	miriMaskDirEntry := widget.NewEntry()
	miriMaskDirEntry.SetText(current.MIRIArtifactMaskDir)
	miriMaskDirEntry.SetPlaceHolder("folder containing <input-stem>_miri_mask.fits")

	notes := widget.NewLabel(
		"These corrections run before sky matching and are saved with the mosaic project.\n" +
			"NIRCam wisp/destriping are detector corrections, not sky subtraction. Wisp templates must be local files named nircam_wisp_<detector>_<filter>.fits.\n" +
			"MIRI masks exclude user-marked pixels. Shower/snowball detection belongs upstream in the current STScI JWST pipeline.",
	)
	notes.TextStyle = fyne.TextStyle{Italic: true}
	notes.Wrapping = fyne.TextWrapWord

	form := widget.NewForm(
		widget.NewFormItem("NIRCam amp pedestal", ampPedestalCheck),
		widget.NewFormItem("NIRCam row banding", rowDestripeCheck),
		widget.NewFormItem("Row mask FITS", rowMaskEntry),
		widget.NewFormItem("Row mask dir", rowMaskDirEntry),
		widget.NewFormItem("Row mask sigma", rowMaskSigmaEntry),
		widget.NewFormItem("Row trend window", rowTrendEntry),
		widget.NewFormItem("NIRCam wisp template", nircamWispCheck),
		widget.NewFormItem("Wisp template dir", wispDirEntry),
		widget.NewFormItem("Wisp scale mode", wispAutoScaleCheck),
		widget.NewFormItem("Fixed wisp scale", wispScaleEntry),
		widget.NewFormItem("MIRI artifact mask", miriArtifactMaskCheck),
		widget.NewFormItem("MIRI mask FITS", miriMaskPathEntry),
		widget.NewFormItem("MIRI mask dir", miriMaskDirEntry),
	)

	content := container.NewVBox(form, notes)
	d := dialog.NewCustomConfirm("JWST Artifact Corrections", "Save", "Cancel", content, func(ok bool) {
		if !ok {
			return
		}

		rowMaskSigmaVal, errRowSigma := strconv.ParseFloat(strings.TrimSpace(rowMaskSigmaEntry.Text), 64)
		if errRowSigma != nil || rowMaskSigmaVal <= 0 {
			dialog.ShowInformation("Invalid Value", "row mask sigma must be a positive number.", win)
			return
		}
		rowTrendVal, errRowTrend := strconv.Atoi(strings.TrimSpace(rowTrendEntry.Text))
		if errRowTrend != nil || rowTrendVal <= 0 {
			dialog.ShowInformation("Invalid Value", "row trend window must be a positive integer.", win)
			return
		}
		wispScaleVal := 0.0
		scaleText := strings.TrimSpace(wispScaleEntry.Text)
		if scaleText != "" {
			v, err := strconv.ParseFloat(scaleText, 64)
			if err != nil || v < 0 {
				dialog.ShowInformation("Invalid Value", "fixed wisp scale must be blank or a non-negative number.", win)
				return
			}
			wispScaleVal = v
		}

		onSave(models.SkysubSettings{
			AmpPedestal:            ampPedestal,
			RowDestripe:            rowDestripe,
			RowDestripeMaskPath:    strings.TrimSpace(rowMaskEntry.Text),
			RowDestripeMaskDir:     strings.TrimSpace(rowMaskDirEntry.Text),
			RowDestripeMaskSigma:   rowMaskSigmaVal,
			RowDestripeTrendWindow: rowTrendVal,
			RowDestripeDirection:   "rows",
			NIRCamWisp:             nircamWisp,
			NIRCamWispTemplateDir:  strings.TrimSpace(wispDirEntry.Text),
			NIRCamWispAutoScale:    wispAutoScale,
			NIRCamWispScale:        wispScaleVal,
			MIRIArtifactMask:       miriArtifactMask,
			MIRIArtifactMaskPath:   strings.TrimSpace(miriMaskPathEntry.Text),
			MIRIArtifactMaskDir:    strings.TrimSpace(miriMaskDirEntry.Text),
		})
	}, win)
	d.Resize(fyne.NewSize(760, 560))
	d.Show()
}

func copyJWSTArtifactSettings(dst, src models.SkysubSettings) models.SkysubSettings {
	dst.AmpPedestal = src.AmpPedestal
	dst.RowDestripe = src.RowDestripe
	dst.RowDestripeMaskPath = src.RowDestripeMaskPath
	dst.RowDestripeMaskDir = src.RowDestripeMaskDir
	dst.RowDestripeMaskSigma = src.RowDestripeMaskSigma
	dst.RowDestripeTrendWindow = src.RowDestripeTrendWindow
	dst.RowDestripeDirection = src.RowDestripeDirection
	dst.NIRCamWisp = src.NIRCamWisp
	dst.NIRCamWispTemplateDir = src.NIRCamWispTemplateDir
	dst.NIRCamWispAutoScale = src.NIRCamWispAutoScale
	dst.NIRCamWispScale = src.NIRCamWispScale
	dst.MIRIArtifactMask = src.MIRIArtifactMask
	dst.MIRIArtifactMaskPath = src.MIRIArtifactMaskPath
	dst.MIRIArtifactMaskDir = src.MIRIArtifactMaskDir
	return dst
}

func jwstArtifactSummary(s models.SkysubSettings) string {
	var enabled []string
	if s.AmpPedestal {
		enabled = append(enabled, "NIRCam amp pedestal")
	}
	if s.RowDestripe {
		enabled = append(enabled, "NIRCam row banding")
	}
	if s.NIRCamWisp {
		enabled = append(enabled, "NIRCam wisp")
	}
	if s.MIRIArtifactMask {
		enabled = append(enabled, "MIRI mask")
	}
	if len(enabled) == 0 {
		return "Detector corrections: off. Existing projects with zero values stay unchanged."
	}
	return "Detector corrections: " + strings.Join(enabled, ", ") + "."
}

func skysubOptionsFromSettings(s models.SkysubSettings) mosaic.SkysubOptions {
	return mosaic.SkysubOptions{
		Enabled:                s.Enabled,
		AmpPedestal:            s.AmpPedestal,
		RowDestripe:            s.RowDestripe,
		RowDestripeMaskPath:    s.RowDestripeMaskPath,
		RowDestripeMaskDir:     s.RowDestripeMaskDir,
		RowDestripeMaskSigma:   s.RowDestripeMaskSigma,
		RowDestripeTrendWindow: s.RowDestripeTrendWindow,
		RowDestripeDirection:   s.RowDestripeDirection,
		NIRCamWisp:             s.NIRCamWisp,
		NIRCamWispTemplateDir:  s.NIRCamWispTemplateDir,
		NIRCamWispAutoScale:    s.NIRCamWispAutoScale,
		NIRCamWispScale:        s.NIRCamWispScale,
		MIRIArtifactMask:       s.MIRIArtifactMask,
		MIRIArtifactMaskPath:   s.MIRIArtifactMaskPath,
		MIRIArtifactMaskDir:    s.MIRIArtifactMaskDir,
		Method:                 mosaic.SkyMethod(s.SkyMethod),
		Stat:                   mosaic.SkyStat(s.SkyStat),
		Width:                  s.SkyWidth,
		Lower:                  s.SkyLower,
		Upper:                  s.SkyUpper,
		HasLower:               s.SkyLowerSet,
		HasUpper:               s.SkyUpperSet,
		Clip:                   s.SkyClip,
		LSigma:                 s.SkyLSigma,
		USigma:                 s.SkyUSigma,
	}
}
