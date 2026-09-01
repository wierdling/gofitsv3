package ui

import (
	"fmt"
	"image/color"
	"math"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
	"gofitsv3/internal/models"
)

type composeWeightSource struct {
	ID       string
	Label    string
	Defaults models.ComposeMixWeight
}

func composeWeightForColor(id string, c color.NRGBA, opacity float64) models.ComposeMixWeight {
	if opacity < 0 {
		opacity = 0
	}
	if opacity > 1 {
		opacity = 1
	}
	return models.ComposeMixWeight{BlinkID: id, Red: float64(c.R) / 255 * opacity, Green: float64(c.G) / 255 * opacity, Blue: float64(c.B) / 255 * opacity}
}

func defaultComposeMixWeights(sources []composeWeightSource) []models.ComposeMixWeight {
	weights := make([]models.ComposeMixWeight, 0, len(sources))
	for _, source := range sources {
		weight := source.Defaults
		weight.BlinkID = source.ID
		if weight.Red == 0 && weight.Green == 0 && weight.Blue == 0 {
			weight.Red, weight.Green, weight.Blue = 1, 1, 1
		}
		weights = append(weights, weight)
	}
	return weights
}

func normalizeComposeMixWeights(existing []models.ComposeMixWeight, sources []composeWeightSource) []models.ComposeMixWeight {
	byID := make(map[string]models.ComposeMixWeight, len(existing))
	for _, weight := range existing {
		if weight.BlinkID != "" {
			byID[weight.BlinkID] = weight
		}
	}
	result := make([]models.ComposeMixWeight, 0, len(sources))
	for _, source := range sources {
		weight, ok := byID[source.ID]
		if !ok {
			weight = source.Defaults
		}
		weight.BlinkID = source.ID
		if weight.Red == 0 && weight.Green == 0 && weight.Blue == 0 {
			weight = source.Defaults
			weight.BlinkID = source.ID
		}
		result = append(result, weight)
	}
	return result
}

func composeMixWeightDisabled(weight models.ComposeMixWeight) bool {
	return weight.Red == 0 && weight.Green == 0 && weight.Blue == 0
}

func upsertComposeMixWeight(weights *[]models.ComposeMixWeight, weight models.ComposeMixWeight) {
	if weights == nil || weight.BlinkID == "" {
		return
	}
	if composeMixWeightDisabled(weight) {
		for i := range *weights {
			if (*weights)[i].BlinkID == weight.BlinkID {
				*weights = append((*weights)[:i], (*weights)[i+1:]...)
				return
			}
		}
		return
	}
	for i := range *weights {
		if (*weights)[i].BlinkID == weight.BlinkID {
			(*weights)[i] = weight
			return
		}
	}
	*weights = append(*weights, weight)
}

func composeModeLabel(mode models.ComposeMode, sourceCount int) string {
	effective := (models.ComposeProject{CompositionMode: mode}).ResolveComposeMode(sourceCount)
	label := "Artistic overlays"
	if effective == models.ComposeModeWeighted {
		label = "Weighted multi-channel"
	}
	return fmt.Sprintf("Effective mode: %s", label)
}

const defaultWidebandMixPercent = 8

// applyWidebandComposeMixPreset applies a symmetric cross-channel mix to the
// three standard channels. Overlay weights are intentionally left unchanged.
func applyWidebandComposeMixPreset(mode *models.ComposeMode, weights []models.ComposeMixWeight, percent float64) ([]models.ComposeMixWeight, error) {
	if math.IsNaN(percent) || math.IsInf(percent, 0) || percent < 0 || percent > 50 {
		return nil, fmt.Errorf("wideband cross-mix percentage must be between 0 and 50")
	}
	s := percent / 100
	preset := map[string]models.ComposeMixWeight{
		models.ComposeChannel1BlinkID: {BlinkID: models.ComposeChannel1BlinkID, Green: s, Blue: 1 - s},
		models.ComposeChannel2BlinkID: {BlinkID: models.ComposeChannel2BlinkID, Red: s, Green: 1 - 2*s, Blue: s},
		models.ComposeChannel3BlinkID: {BlinkID: models.ComposeChannel3BlinkID, Red: 1 - s, Green: s},
	}
	updated := append([]models.ComposeMixWeight(nil), weights...)
	for i := range updated {
		if weight, ok := preset[updated[i].BlinkID]; ok {
			updated[i] = weight
		}
	}
	if mode != nil {
		*mode = models.ComposeModeWeighted
	}
	return updated, nil
}

func showComposeWeightsDialog(win fyne.Window, mode *models.ComposeMode, weights *[]models.ComposeMixWeight, sources []composeWeightSource, onApply func()) {
	if mode == nil || weights == nil {
		return
	}
	sources = append([]composeWeightSource(nil), sources...)
	currentMode := *mode
	currentWeights := normalizeComposeMixWeights(*weights, sources)
	modeSelect := widget.NewSelect([]string{"Auto", "Weighted multi-channel", "Artistic overlays"}, nil)
	modeLabel := "Artistic overlays"
	if currentMode == models.ComposeModeAuto {
		modeLabel = "Auto"
	} else if currentMode == models.ComposeModeWeighted {
		modeLabel = "Weighted multi-channel"
	}
	modeSelect.SetSelected(modeLabel)
	effective := widget.NewLabel(composeModeLabel(currentMode, len(sources)))
	rows := container.NewVBox()
	entries := make([][3]*widget.Entry, len(sources))
	refreshEntries := func() {
		for i := range entries {
			for c, entry := range entries[i] {
				entry.SetText(strconv.FormatFloat([]float64{currentWeights[i].Red, currentWeights[i].Green, currentWeights[i].Blue}[c], 'g', 6, 64))
			}
		}
	}
	for i, source := range sources {
		row := container.NewGridWithColumns(4, widget.NewLabel(source.Label))
		for c, value := range []float64{currentWeights[i].Red, currentWeights[i].Green, currentWeights[i].Blue} {
			entry := widget.NewEntry()
			entry.SetText(strconv.FormatFloat(value, 'g', 6, 64))
			entries[i][c] = entry
			row.Add(entry)
		}
		rows.Add(row)
	}
	modeSelect.OnChanged = func(selected string) {
		switch selected {
		case "Auto":
			currentMode = models.ComposeModeAuto
		case "Weighted multi-channel":
			currentMode = models.ComposeModeWeighted
		default:
			currentMode = models.ComposeModeArtistic
		}
		currentWeights = normalizeComposeMixWeights(currentWeights, sources)
		effective.SetText(composeModeLabel(currentMode, len(sources)))
	}
	reset := widget.NewButton("Reset weights", func() {
		disabled := make(map[string]bool, len(currentWeights))
		for _, weight := range currentWeights {
			disabled[weight.BlinkID] = composeMixWeightDisabled(weight)
		}
		currentWeights = defaultComposeMixWeights(sources)
		for i := range currentWeights {
			if disabled[currentWeights[i].BlinkID] {
				currentWeights[i].Red, currentWeights[i].Green, currentWeights[i].Blue = 0, 0, 0
			}
		}
		refreshEntries()
	})
	widebandPercent := widget.NewEntry()
	widebandPercent.SetText(strconv.Itoa(defaultWidebandMixPercent))
	wideband := widget.NewButton("Apply wideband preset", func() {
		percent, err := strconv.ParseFloat(strings.TrimSpace(widebandPercent.Text), 64)
		if err != nil {
			dialog.ShowError(fmt.Errorf("wideband cross-mix percentage must be between 0 and 50"), win)
			return
		}
		updated, err := applyWidebandComposeMixPreset(&currentMode, currentWeights, percent)
		if err != nil {
			dialog.ShowError(err, win)
			return
		}
		currentWeights = updated
		modeSelect.SetSelected("Weighted multi-channel")
		refreshEntries()
		effective.SetText(composeModeLabel(currentMode, len(sources)))
	})
	var d *dialog.CustomDialog
	apply := widget.NewButton("Apply", func() {
		for i := range entries {
			values := []*float64{&currentWeights[i].Red, &currentWeights[i].Green, &currentWeights[i].Blue}
			for c, entry := range entries[i] {
				value, err := strconv.ParseFloat(strings.TrimSpace(entry.Text), 64)
				if err != nil || value < 0 {
					dialog.ShowError(fmt.Errorf("%s RGB weight must be a non-negative number", sources[i].Label), win)
					return
				}
				*values[c] = value
			}
			if composeMixWeightDisabled(currentWeights[i]) {
				continue
			}
			if err := currentWeights[i].Validate(); err != nil {
				dialog.ShowError(fmt.Errorf("%s: %w", sources[i].Label, err), win)
				return
			}
		}
		activeWeights := make([]models.ComposeMixWeight, 0, len(currentWeights))
		for _, weight := range currentWeights {
			if !composeMixWeightDisabled(weight) {
				activeWeights = append(activeWeights, weight)
			}
		}
		*mode, *weights = currentMode, activeWeights
		if onApply != nil {
			onApply()
		}
		d.Hide()
	})
	cancel := widget.NewButton("Cancel", func() { d.Hide() })
	header := container.NewGridWithColumns(4, widget.NewLabel("Source"), widget.NewLabel("Red"), widget.NewLabel("Green"), widget.NewLabel("Blue"))
	widebandForm := widget.NewForm(widget.NewFormItem("Cross-mix % (s)", widebandPercent), widget.NewFormItem("Preset", wideband))
	content := container.NewBorder(container.NewVBox(widget.NewForm(widget.NewFormItem("Mode", modeSelect)), effective, widebandForm, header), container.NewGridWithColumns(3, reset, cancel, apply), nil, nil, container.NewVScroll(rows))
	d = dialog.NewCustomWithoutButtons("Compose color mixing", content, win)
	d.Resize(fyne.NewSize(640, 420))
	d.Show()
}
