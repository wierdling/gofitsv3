package ui

import (
	"fmt"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/models"
)

func defaultStarlessComposeSettings() models.StarlessComposeSettings {
	return models.StarlessComposeSettings{
		Enabled:                 false,
		DetectionMode:           "min",
		DetectionPreprocessMode: "none",
		DetectionMergeMode:      "per-channel-merged",
		ThresholdSigma:          5.5,
		BackgroundTileSize:      96,
		UseNoDataFloor:          false,
		NoDataFloor:             0.0,
		SeedMinProminence:       0.05,
		MinDetectedChannels:     2,
		MinSeedFootprintArea:    5,
		MinSharedChannels:       2,
		SuppressionRadius:       10,
		MaskBaseRadius:          3,
		MaxRadius:               60,
		FeatherRadius:           2,
		InpaintRadius:           12,
		StarBrightness:          0.45,
		StarSaturation:          0.08,
		ExportDebugMasks:        false,
	}
}

func normalizeStarlessComposeSettings(in models.StarlessComposeSettings) models.StarlessComposeSettings {
	def := defaultStarlessComposeSettings()
	out := in
	if strings.TrimSpace(out.DetectionMode) == "" {
		out.DetectionMode = def.DetectionMode
	}
	preprocessMode := strings.ToLower(strings.TrimSpace(out.DetectionPreprocessMode))
	if preprocessMode != "none" && preprocessMode != "dog" {
		out.DetectionPreprocessMode = def.DetectionPreprocessMode
	} else {
		out.DetectionPreprocessMode = preprocessMode
	}
	if strings.TrimSpace(out.DetectionMergeMode) == "" {
		out.DetectionMergeMode = def.DetectionMergeMode
	}
	mergeMode := strings.ToLower(strings.TrimSpace(out.DetectionMergeMode))
	if mergeMode != "shared" && mergeMode != "per-channel-merged" {
		out.DetectionMergeMode = def.DetectionMergeMode
	} else {
		out.DetectionMergeMode = mergeMode
	}
	if out.ThresholdSigma <= 0 {
		out.ThresholdSigma = def.ThresholdSigma
	}
	if out.BackgroundTileSize < 1 {
		out.BackgroundTileSize = def.BackgroundTileSize
	}
	if out.SeedMinProminence < 0 {
		out.SeedMinProminence = def.SeedMinProminence
	}
	if out.MinDetectedChannels < 1 || out.MinDetectedChannels > 3 {
		out.MinDetectedChannels = def.MinDetectedChannels
	}
	if out.MinSeedFootprintArea < 1 {
		out.MinSeedFootprintArea = def.MinSeedFootprintArea
	}
	if out.MinSharedChannels < 2 || out.MinSharedChannels > 3 {
		out.MinSharedChannels = def.MinSharedChannels
	}
	if out.SuppressionRadius < 1 {
		out.SuppressionRadius = def.SuppressionRadius
	}
	if out.MaskBaseRadius < 0 {
		out.MaskBaseRadius = def.MaskBaseRadius
	}
	if out.MaxRadius < out.MaskBaseRadius {
		out.MaxRadius = maxIntUI(def.MaxRadius, out.MaskBaseRadius)
	}
	if out.FeatherRadius < 0 {
		out.FeatherRadius = def.FeatherRadius
	}
	if out.InpaintRadius < 1 {
		out.InpaintRadius = def.InpaintRadius
	}
	if out.StarBrightness < 0 {
		out.StarBrightness = def.StarBrightness
	}
	if out.StarSaturation < 0 {
		out.StarSaturation = 0
	}
	if out.StarSaturation > 1 {
		out.StarSaturation = 1
	}
	return out
}

// showStarlessSettingsDialog opens the starless settings dialog.
// onSave is called (and the dialog closed) when Save is clicked.
// onApply is called (dialog stays open) when Apply is clicked.
func showStarlessSettingsDialog(win fyne.Window, current models.StarlessComposeSettings, onSave func(models.StarlessComposeSettings), onApply func(models.StarlessComposeSettings)) {
	current = normalizeStarlessComposeSettings(current)
	lastApplied := current

	enableCheck := widget.NewCheck("Enable starless processing in compose preview/export", nil)
	enableCheck.SetChecked(current.Enabled)

	detectionSelect := NewSafeSelect([]string{"median", "min", "max"}, nil)
	detectionSelect.SetSelected(strings.ToLower(current.DetectionMode))

	preprocessSelect := NewSafeSelect([]string{"dog", "none"}, nil)
	preprocessSelect.SetSelected(strings.ToLower(current.DetectionPreprocessMode))

	mergeModeSelect := NewSafeSelect([]string{"shared", "per-channel-merged"}, nil)
	mergeModeSelect.SetSelected(strings.ToLower(current.DetectionMergeMode))

	thresholdEntry := widget.NewEntry()
	thresholdEntry.SetText(fmt.Sprintf("%.2f", current.ThresholdSigma))

	tileEntry := widget.NewEntry()
	tileEntry.SetText(fmt.Sprintf("%d", current.BackgroundTileSize))

	noDataFloorEntry := widget.NewEntry()
	noDataFloorEntry.SetText(fmt.Sprintf("%.4f", current.NoDataFloor))

	useNoDataFloorCheck := widget.NewCheck("Treat pixels at/below floor as no-data", nil)
	useNoDataFloorCheck.SetChecked(current.UseNoDataFloor)

	seedProminenceEntry := widget.NewEntry()
	seedProminenceEntry.SetText(fmt.Sprintf("%.4f", current.SeedMinProminence))

	minSeedFootprintEntry := widget.NewEntry()
	minSeedFootprintEntry.SetText(fmt.Sprintf("%d", current.MinSeedFootprintArea))

	minDetectedChannelsEntry := widget.NewEntry()
	minDetectedChannelsEntry.SetText(fmt.Sprintf("%d", current.MinDetectedChannels))

	minSharedChannelsEntry := widget.NewEntry()
	minSharedChannelsEntry.SetText(fmt.Sprintf("%d", current.MinSharedChannels))

	suppressionRadiusEntry := widget.NewEntry()
	suppressionRadiusEntry.SetText(fmt.Sprintf("%d", current.SuppressionRadius))

	baseRadiusEntry := widget.NewEntry()
	baseRadiusEntry.SetText(fmt.Sprintf("%d", current.MaskBaseRadius))

	maxRadiusEntry := widget.NewEntry()
	maxRadiusEntry.SetText(fmt.Sprintf("%d", current.MaxRadius))

	featherEntry := widget.NewEntry()
	featherEntry.SetText(fmt.Sprintf("%d", current.FeatherRadius))

	inpaintEntry := widget.NewEntry()
	inpaintEntry.SetText(fmt.Sprintf("%d", current.InpaintRadius))

	brightnessEntry := widget.NewEntry()
	brightnessEntry.SetText(fmt.Sprintf("%.2f", current.StarBrightness))

	saturationEntry := widget.NewEntry()
	saturationEntry.SetText(fmt.Sprintf("%.2f", current.StarSaturation))

	debugCheck := widget.NewCheck("Show diagnostic images on Apply/Save", nil)
	debugCheck.SetChecked(current.ExportDebugMasks)

	notes := widget.NewLabel("Seed Detection controls decide what is accepted as a star candidate. Mask, inpaint, and recombination controls affect what happens after a seed is accepted.")
	notes.Wrapping = fyne.TextWrapWord
	notes.TextStyle = fyne.TextStyle{Italic: true}

	section := func(title, help string, items ...*widget.FormItem) fyne.CanvasObject {
		label := widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
		helpLabel := widget.NewLabel(help)
		helpLabel.Wrapping = fyne.TextWrapWord
		form := widget.NewForm(items...)
		return container.NewVBox(label, helpLabel, form)
	}

	seedPanel := section("Seed Detection", "Controls how compact stellar candidates are identified and filtered before masks are grown.",
		widget.NewFormItem("Enabled", enableCheck),
		widget.NewFormItem("Detection Mode", detectionSelect),
		widget.NewFormItem("High-Pass Detection", preprocessSelect),
		widget.NewFormItem("Detection Merge Mode", mergeModeSelect),
		widget.NewFormItem("Threshold Sigma", thresholdEntry),
		widget.NewFormItem("Background Tile Size", tileEntry),
		widget.NewFormItem("Seed Min Prominence", seedProminenceEntry),
		widget.NewFormItem("Suppression Radius", suppressionRadiusEntry),
		widget.NewFormItem("Min Detected Channels", minDetectedChannelsEntry),
		widget.NewFormItem("Min Seed Footprint Area", minSeedFootprintEntry),
	)
	validityPanel := section("Validity / No-Data", "Controls which pixels and channels are considered usable for starless processing.",
		widget.NewFormItem("Use No-Data Floor", useNoDataFloorCheck),
		widget.NewFormItem("No-Data Floor", noDataFloorEntry),
		widget.NewFormItem("Min Shared Channels", minSharedChannelsEntry),
	)
	maskPanel := section("Mask Generation", "Controls the size and softness of masks grown from accepted star seeds.",
		widget.NewFormItem("Mask Base Radius", baseRadiusEntry),
		widget.NewFormItem("Mask Max Radius", maxRadiusEntry),
		widget.NewFormItem("Feather Radius", featherEntry),
	)
	inpaintPanel := section("Starless Fill / Inpainting", "Controls how removed star pixels are filled from nearby background.",
		widget.NewFormItem("Inpaint Radius", inpaintEntry),
	)
	recombinePanel := section("Star Recombination", "Controls how extracted stars are added back into preview/export output.",
		widget.NewFormItem("Star Brightness", brightnessEntry),
		widget.NewFormItem("Star Saturation", saturationEntry),
	)
	diagnosticsPanel := section("Diagnostics", "Optional diagnostic images show accepted and rejected seeds, masks, and starless layers.",
		widget.NewFormItem("Diagnostic Images", debugCheck),
	)

	parseFloat := func(label, raw string, min float64, max float64, maxInclusive bool) (float64, bool) {
		v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil || v < min || (!maxInclusive && v >= max) || (maxInclusive && v > max) {
			dialog.ShowInformation("Invalid Value", fmt.Sprintf("%s is out of range.", label), win)
			return 0, false
		}
		return v, true
	}
	parseInt := func(label, raw string, min int) (int, bool) {
		v, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || v < min {
			dialog.ShowInformation("Invalid Value", fmt.Sprintf("%s must be >= %d.", label, min), win)
			return 0, false
		}
		return v, true
	}

	collect := func() (models.StarlessComposeSettings, bool) {
		threshold, ok := parseFloat("Threshold Sigma", thresholdEntry.Text, 0.1, 1000, true)
		if !ok {
			return models.StarlessComposeSettings{}, false
		}
		tileSize, ok := parseInt("Background Tile Size", tileEntry.Text, 1)
		if !ok {
			return models.StarlessComposeSettings{}, false
		}
		noDataFloor, ok := parseFloat("No-Data Floor", noDataFloorEntry.Text, -1e9, 1e9, true)
		if !ok {
			return models.StarlessComposeSettings{}, false
		}
		seedProminence, ok := parseFloat("Seed Min Prominence", seedProminenceEntry.Text, 0, 1000, true)
		if !ok {
			return models.StarlessComposeSettings{}, false
		}
		minSeedFootprint, ok := parseInt("Min Seed Footprint", minSeedFootprintEntry.Text, 1)
		if !ok {
			return models.StarlessComposeSettings{}, false
		}
		minDetectedChannels, ok := parseInt("Min Detected Channels", minDetectedChannelsEntry.Text, 1)
		if !ok || minDetectedChannels > 3 {
			dialog.ShowInformation("Invalid Value", "Min Detected Channels must be between 1 and 3.", win)
			return models.StarlessComposeSettings{}, false
		}
		minSharedChannels, ok := parseInt("Min Shared Channels", minSharedChannelsEntry.Text, 2)
		if !ok || minSharedChannels > 3 {
			dialog.ShowInformation("Invalid Value", "Min Shared Channels must be 2 or 3.", win)
			return models.StarlessComposeSettings{}, false
		}
		suppressionRadius, ok := parseInt("Suppression Radius", suppressionRadiusEntry.Text, 1)
		if !ok {
			return models.StarlessComposeSettings{}, false
		}
		baseRadius, ok := parseInt("Mask Base Radius", baseRadiusEntry.Text, 0)
		if !ok {
			return models.StarlessComposeSettings{}, false
		}
		maxRadius, ok := parseInt("Mask Max Radius", maxRadiusEntry.Text, baseRadius)
		if !ok {
			return models.StarlessComposeSettings{}, false
		}
		featherRadius, ok := parseInt("Feather Radius", featherEntry.Text, 0)
		if !ok {
			return models.StarlessComposeSettings{}, false
		}
		inpaintRadius, ok := parseInt("Inpaint Radius", inpaintEntry.Text, 1)
		if !ok {
			return models.StarlessComposeSettings{}, false
		}
		brightness, ok := parseFloat("Star Brightness", brightnessEntry.Text, 0, 1000, true)
		if !ok {
			return models.StarlessComposeSettings{}, false
		}
		saturation, ok := parseFloat("Star Saturation", saturationEntry.Text, 0, 1, true)
		if !ok {
			return models.StarlessComposeSettings{}, false
		}
		return models.StarlessComposeSettings{
			Enabled:                 enableCheck.Checked,
			DetectionMode:           strings.ToLower(strings.TrimSpace(detectionSelect.Selected)),
			DetectionPreprocessMode: strings.ToLower(strings.TrimSpace(preprocessSelect.Selected)),
			DetectionMergeMode:      strings.ToLower(strings.TrimSpace(mergeModeSelect.Selected)),
			ThresholdSigma:          threshold,
			BackgroundTileSize:      tileSize,
			UseNoDataFloor:          useNoDataFloorCheck.Checked,
			NoDataFloor:             noDataFloor,
			SeedMinProminence:       seedProminence,
			MinDetectedChannels:     minDetectedChannels,
			MinSeedFootprintArea:    minSeedFootprint,
			MinSharedChannels:       minSharedChannels,
			SuppressionRadius:       suppressionRadius,
			MaskBaseRadius:          baseRadius,
			MaxRadius:               maxRadius,
			FeatherRadius:           featherRadius,
			InpaintRadius:           inpaintRadius,
			StarBrightness:          brightness,
			StarSaturation:          saturation,
			ExportDebugMasks:        debugCheck.Checked,
		}, true
	}

	var d *dialog.CustomDialog

	saveBtn := widget.NewButton("Save", func() {
		s, ok := collect()
		if !ok {
			return
		}
		s = normalizeStarlessComposeSettings(s)
		if s != lastApplied {
			onApply(s)
			lastApplied = s
		}
		onSave(s)
		d.Hide()
	})
	saveBtn.Importance = widget.HighImportance

	applyBtn := widget.NewButton("Apply", func() {
		s, ok := collect()
		if !ok {
			return
		}
		s = normalizeStarlessComposeSettings(s)
		onApply(s)
		lastApplied = s
	})

	buttonRow := container.NewHBox(layout.NewSpacer(), applyBtn, saveBtn)
	settingsContent := container.NewVBox(
		seedPanel,
		widget.NewSeparator(),
		validityPanel,
		widget.NewSeparator(),
		maskPanel,
		widget.NewSeparator(),
		inpaintPanel,
		widget.NewSeparator(),
		recombinePanel,
		widget.NewSeparator(),
		diagnosticsPanel,
		notes,
	)
	settingsScroll := container.NewVScroll(settingsContent)
	settingsScroll.SetMinSize(fyne.NewSize(740, 560))
	footer := container.NewVBox(widget.NewSeparator(), buttonRow)
	content := container.NewBorder(nil, footer, nil, nil, settingsScroll)

	d = dialog.NewCustom("Starless Settings", "Cancel", content, win)
	d.Resize(fyne.NewSize(780, 720))
	d.Show()
}

func maxIntUI(a, b int) int {
	if a > b {
		return a
	}
	return b
}
