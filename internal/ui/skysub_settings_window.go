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
		Enabled:     false,
		SkyMethod:   int(mosaic.SkyMethodLocalMin),
		SkyStat:     int(mosaic.SkyStatMedian),
		SkyWidth:    0.1,
		SkyClip:     5,
		SkyLSigma:   4.0,
		SkyUSigma:   4.0,
		SkyLowerSet: false,
		SkyUpperSet: false,
	}
}

var skyMethodNames = []string{"localmin", "globalmin", "match", "globalmin+match"}
var skyStatNames = []string{"median", "mode", "mean"}

func showSkysubSettingsDialog(win fyne.Window, current models.SkysubSettings, onSave func(models.SkysubSettings)) {
	enabled := current.Enabled
	enabledCheck := widget.NewCheck("Enable AstroDrizzle-style sky subtraction before CR rejection and drizzle.", func(v bool) {
		enabled = v
	})
	enabledCheck.SetChecked(enabled)

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
			"skywidth: histogram bin width in sigma for mode estimation.\n" +
			"skylower/skyupper: optional pixel cutoffs in the input image units.\n" +
			"skyclip, skylsigma, skyusigma: iterative clipping controls for sky estimation.",
	)
	notes.TextStyle = fyne.TextStyle{Italic: true}
	notes.Wrapping = fyne.TextWrapWord

	form := widget.NewForm(
		widget.NewFormItem("Enabled", enabledCheck),
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

		onSave(models.SkysubSettings{
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
		})
	}, win)
	d.Resize(fyne.NewSize(700, 470))
	d.Show()
}

func skysubOptionsFromSettings(s models.SkysubSettings) mosaic.SkysubOptions {
	return mosaic.SkysubOptions{
		Enabled:  s.Enabled,
		Method:   mosaic.SkyMethod(s.SkyMethod),
		Stat:     mosaic.SkyStat(s.SkyStat),
		Width:    s.SkyWidth,
		Lower:    s.SkyLower,
		Upper:    s.SkyUpper,
		HasLower: s.SkyLowerSet,
		HasUpper: s.SkyUpperSet,
		Clip:     s.SkyClip,
		LSigma:   s.SkyLSigma,
		USigma:   s.SkyUSigma,
	}
}
