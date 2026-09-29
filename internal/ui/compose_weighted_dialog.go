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
	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
)

type composeWeightSource struct {
	ID               string
	Label            string
	Defaults         models.ComposeMixWeight
	FilterName       string
	WavelengthNm     float64
	BandClass        fitsio.FilterBandClass
	Origin           string
	Confidence       string
	UnresolvedReason string
}

// composeWeightSourceFromImage builds the dialog's metadata snapshot without
// opening the source or retaining any pixel data.
func composeWeightSourceFromImage(id, label string, defaults models.ComposeMixWeight, img *models.LoadedImage) composeWeightSource {
	source := composeWeightSource{ID: id, Label: label, Defaults: defaults, BandClass: fitsio.FilterBandUnknown}
	if img == nil {
		return source
	}
	bandpass := fitsio.ResolveFilterBandpass(img.Primary, img.HDU.Header)
	source.FilterName = bandpass.Name
	source.WavelengthNm = bandpass.WavelengthNm
	source.BandClass = bandpass.Class
	source.Origin = bandpass.Origin
	source.Confidence = bandpass.Confidence
	source.UnresolvedReason = bandpass.UnresolvedReason
	if source.FilterName != "" {
		source.Label += " — " + source.FilterName
	}
	return source
}

func wavelengthBandClassLabel(class fitsio.FilterBandClass) string {
	switch class {
	case fitsio.FilterBandWide:
		return "W"
	case fitsio.FilterBandMedium:
		return "M"
	case fitsio.FilterBandNarrow:
		return "N"
	case fitsio.FilterBandLongpass:
		return "L/LP"
	default:
		return "Unknown"
	}
}

func parseWavelengthBandClass(value string) fitsio.FilterBandClass {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "W":
		return fitsio.FilterBandWide
	case "M":
		return fitsio.FilterBandMedium
	case "N":
		return fitsio.FilterBandNarrow
	case "L/LP", "L", "LP":
		return fitsio.FilterBandLongpass
	default:
		return fitsio.FilterBandUnknown
	}
}

// composeWavelengthCandidates validates the dialog snapshot and includes only
// rows that currently contribute RGB signal. It is independent of widgets so
// the transactional behavior can be tested without constructing a dialog.
func composeWavelengthCandidates(sources []composeWeightSource, weights []models.ComposeMixWeight, wavelengths []float64, classes []fitsio.FilterBandClass) ([]processing.WavelengthMixSource, error) {
	if len(sources) != len(weights) || len(sources) != len(wavelengths) || len(sources) != len(classes) {
		return nil, fmt.Errorf("wavelength mapping source snapshot is inconsistent")
	}
	candidates := make([]processing.WavelengthMixSource, 0, len(sources))
	for i, source := range sources {
		if composeMixWeightDisabled(weights[i]) {
			continue
		}
		if !finiteComposeNumber(wavelengths[i]) || wavelengths[i] <= 0 {
			if source.UnresolvedReason != "" {
				return nil, fmt.Errorf("%s: %s", source.Label, source.UnresolvedReason)
			}
			return nil, fmt.Errorf("%s: enter a positive wavelength in nm", source.Label)
		}
		candidates = append(candidates, processing.WavelengthMixSource{BlinkID: source.ID, WavelengthNm: wavelengths[i], BandClass: classes[i]})
	}
	return candidates, nil
}

func applyGeneratedWavelengthWeights(current, generated []models.ComposeMixWeight) []models.ComposeMixWeight {
	updated := append([]models.ComposeMixWeight(nil), current...)
	byID := make(map[string]models.ComposeMixWeight, len(generated))
	for _, weight := range generated {
		byID[weight.BlinkID] = weight
	}
	for i := range updated {
		if weight, ok := byID[updated[i].BlinkID]; ok {
			updated[i] = weight
		}
	}
	return updated
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
	wavelengthEntries := make([]*widget.Entry, len(sources))
	bandSelects := make([]*widget.Select, len(sources))
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
		wavelength := widget.NewEntry()
		if source.WavelengthNm > 0 {
			wavelength.SetText(strconv.FormatFloat(source.WavelengthNm, 'g', 8, 64))
		}
		wavelengthEntries[i] = wavelength
		class := widget.NewSelect([]string{"W", "M", "N", "L/LP", "Unknown"}, nil)
		class.SetSelected(wavelengthBandClassLabel(source.BandClass))
		bandSelects[i] = class
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
	wavelengthCrossMix := widget.NewEntry()
	wavelengthCrossMix.SetText(strconv.Itoa(defaultWidebandMixPercent))
	accentPercent := widget.NewEntry()
	accentPercent.SetText("25")
	wavelengthStatus := widget.NewLabel("Enter or verify filter metadata before applying.")
	wavelengthRows := container.NewVBox()
	for i, source := range sources {
		wavelengthRows.Add(container.NewGridWithColumns(4,
			widget.NewLabel(source.Label), wavelengthEntries[i], bandSelects[i],
			widget.NewLabel(source.Origin)))
	}
	wavelengthPreset := widget.NewButton("Apply wavelength-aware preset", func() {
		// Snapshot edits first; no mode or weight state is changed on failure.
		temporaryWeights := append([]models.ComposeMixWeight(nil), currentWeights...)
		for i := range entries {
			values := []*float64{&temporaryWeights[i].Red, &temporaryWeights[i].Green, &temporaryWeights[i].Blue}
			for c, entry := range entries[i] {
				value, parseErr := strconv.ParseFloat(strings.TrimSpace(entry.Text), 64)
				if parseErr != nil || value < 0 || !finiteComposeNumber(value) {
					wavelengthStatus.SetText(fmt.Sprintf("%s RGB weight must be a non-negative number", sources[i].Label))
					return
				}
				*values[c] = value
			}
		}
		wavelengths := make([]float64, len(sources))
		classes := make([]fitsio.FilterBandClass, len(sources))
		for i, source := range sources {
			// Disabled rows are intentionally excluded from the wavelength
			// recipe. Their metadata may be blank or unresolved, and their
			// existing zero weights must remain untouched.
			if composeMixWeightDisabled(temporaryWeights[i]) {
				continue
			}
			value, err := strconv.ParseFloat(strings.TrimSpace(wavelengthEntries[i].Text), 64)
			if err != nil {
				wavelengthStatus.SetText(fmt.Sprintf("%s: enter a positive wavelength in nm", source.Label))
				return
			}
			wavelengths[i] = value
			classes[i] = parseWavelengthBandClass(bandSelects[i].Selected)
		}
		candidate, err := composeWavelengthCandidates(sources, temporaryWeights, wavelengths, classes)
		if err != nil {
			wavelengthStatus.SetText(err.Error())
			return
		}
		crossMix, err := strconv.ParseFloat(strings.TrimSpace(wavelengthCrossMix.Text), 64)
		if err != nil {
			wavelengthStatus.SetText("Cross-mix must be between 0 and 50 percent")
			return
		}
		accent, err := strconv.ParseFloat(strings.TrimSpace(accentPercent.Text), 64)
		if err != nil {
			wavelengthStatus.SetText("Narrowband accent must be between 1 and 100 percent")
			return
		}
		result, err := processing.GenerateWavelengthMixWeights(candidate, processing.WavelengthMixOptions{CrossMixPercent: crossMix, NarrowbandAccentPercent: accent})
		if err != nil {
			wavelengthStatus.SetText(err.Error())
			return
		}
		currentWeights = applyGeneratedWavelengthWeights(temporaryWeights, result.Weights)
		currentMode = models.ComposeModeWeighted
		modeSelect.SetSelected("Weighted multi-channel")
		refreshEntries()
		effective.SetText(composeModeLabel(currentMode, len(sources)))
		if len(result.Warnings) > 0 {
			wavelengthStatus.SetText(strings.Join(result.Warnings, " "))
		} else {
			wavelengthStatus.SetText("Wavelength-aware weights generated.")
		}
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
		*mode, *weights = currentMode, append([]models.ComposeMixWeight(nil), currentWeights...)
		if onApply != nil {
			onApply()
		}
		d.Hide()
	})
	cancel := widget.NewButton("Cancel", func() { d.Hide() })
	header := container.NewGridWithColumns(4, widget.NewLabel("Source"), widget.NewLabel("Red"), widget.NewLabel("Green"), widget.NewLabel("Blue"))
	widebandForm := widget.NewForm(widget.NewFormItem("Cross-mix % (s)", widebandPercent), widget.NewFormItem("Preset", wideband))
	wavelengthForm := widget.NewForm(widget.NewFormItem("Cross-mix % (0–50)", wavelengthCrossMix), widget.NewFormItem("Narrowband accent % (1–100)", accentPercent), widget.NewFormItem("Preset", wavelengthPreset))
	wavelengthHeader := container.NewGridWithColumns(4, widget.NewLabel("Filter"), widget.NewLabel("Wavelength nm"), widget.NewLabel("Class"), widget.NewLabel("Detected origin"))
	allRows := container.NewVBox(header, rows, wavelengthForm, wavelengthStatus, wavelengthHeader, wavelengthRows)
	content := container.NewBorder(container.NewVBox(widget.NewForm(widget.NewFormItem("Mode", modeSelect)), effective, widebandForm), container.NewGridWithColumns(3, reset, cancel, apply), nil, nil, container.NewVScroll(allRows))
	d = dialog.NewCustomWithoutButtons("Compose color mixing", content, win)
	d.Resize(fyne.NewSize(640, 420))
	d.Show()
}

func finiteComposeNumber(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
