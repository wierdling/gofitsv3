package ui

import (
	"context"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gofitsv3/internal/debuglog"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
	"gofitsv3/internal/stretch"
)

type composeMagicFile struct {
	Path         string
	Name         string
	Filter       string
	FilterNumber int
}

type composeMagicAssignment string

const (
	composeMagicBlue   composeMagicAssignment = "Blue"
	composeMagicGreen  composeMagicAssignment = "Green"
	composeMagicRed    composeMagicAssignment = "Red"
	composeMagicCustom composeMagicAssignment = "Custom"
)

type composeMagicRow struct {
	File       composeMagicFile
	Assignment composeMagicAssignment
	Color      color.NRGBA
}

type composeMagicSpec struct {
	Preset string
	Rows   []composeMagicRow
}

type composeMagicPreparedChannel struct {
	Row   composeMagicRow
	Image *models.LoadedImage
}

type composeMagicBatch struct {
	Preset   string
	Channels []composeMagicPreparedChannel
}

type composeMagicCustomInstall struct {
	Channel composeMagicPreparedChannel
	Slot    int
}

type composeMagicInstallPlan struct {
	Base    [3]*models.LoadedImage
	Customs []composeMagicCustomInstall
}

type composeMagicLoader func(string) (*models.LoadedImage, error)

type composeMagicStages struct {
	magic    func(*models.LoadedImage, processing.MagicPreset) processing.MagicLevelsResult
	magicMTF func(*models.LoadedImage, processing.MagicPreset) processing.MagicLevelsResult
	setMTF   func(*models.LoadedImage)
	autoMTF  func(*models.LoadedImage)
}

var composeMagicFilenamePattern = regexp.MustCompile(`(?i)^(F([0-9]+)[A-Z]+[0-9]*)_(?:(.+)_)?(?:drz|driz|drizzle)\.fits$`)

func composeMagicFilter(name string) (string, int, bool) {
	matches := composeMagicFilenamePattern.FindStringSubmatch(filepath.Base(name))
	if matches == nil || (matches[3] != "" && !validComposeMagicAdditionalName(matches[3])) {
		return "", 0, false
	}
	filterNumber, err := strconv.Atoi(matches[2])
	if err != nil {
		return "", 0, false
	}
	return strings.ToUpper(matches[1]), filterNumber, true
}

func validComposeMagicAdditionalName(name string) bool {
	hasLetterOrDigit := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			hasLetterOrDigit = true
		case r == '_', r == '-', r == '.':
		default:
			return false
		}
	}
	return hasLetterOrDigit
}

func discoverComposeMagicFiles(dir string) ([]composeMagicFile, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("compose Magic directory is empty")
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve compose Magic directory: %w", err)
	}
	entries, err := os.ReadDir(absDir)
	if err != nil {
		return nil, fmt.Errorf("read compose Magic directory: %w", err)
	}
	files := make([]composeMagicFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		filter, filterNumber, ok := composeMagicFilter(entry.Name())
		if !ok {
			continue
		}
		files = append(files, composeMagicFile{
			Path:         filepath.Join(absDir, entry.Name()),
			Name:         entry.Name(),
			Filter:       filter,
			FilterNumber: filterNumber,
		})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].FilterNumber != files[j].FilterNumber {
			return files[i].FilterNumber < files[j].FilterNumber
		}
		left, right := strings.ToLower(files[i].Name), strings.ToLower(files[j].Name)
		if left != right {
			return left < right
		}
		return files[i].Name < files[j].Name
	})
	return files, nil
}

func defaultComposeMagicRows(files []composeMagicFile) []composeMagicRow {
	rows := make([]composeMagicRow, len(files))
	for i, file := range files {
		rows[i] = composeMagicRow{
			File:       file,
			Assignment: composeMagicCustom,
		}
	}
	if len(rows) < 3 {
		assignComposeMagicCustomColors(rows)
		return rows
	}
	ranked := make([]int, len(rows))
	for i := range ranked {
		ranked[i] = i
	}
	sort.Slice(ranked, func(i, j int) bool {
		left, right := rows[ranked[i]].File, rows[ranked[j]].File
		if left.FilterNumber != right.FilterNumber {
			return left.FilterNumber < right.FilterNumber
		}
		leftName, rightName := strings.ToLower(left.Name), strings.ToLower(right.Name)
		if leftName != rightName {
			return leftName < rightName
		}
		return left.Path < right.Path
	})
	primary := []struct {
		index      int
		assignment composeMagicAssignment
	}{
		{ranked[0], composeMagicBlue},
		{ranked[len(rows)/2], composeMagicGreen},
		{ranked[len(rows)-1], composeMagicRed},
	}
	for _, preset := range primary {
		rows[preset.index].Assignment = preset.assignment
		rows[preset.index].Color = composeMagicPresetColor(preset.assignment)
	}
	assignComposeMagicCustomColors(rows)
	return rows
}

func composeMagicPresetColor(assignment composeMagicAssignment) color.NRGBA {
	switch assignment {
	case composeMagicBlue:
		return color.NRGBA{R: 100, G: 149, B: 237, A: 255}
	case composeMagicGreen:
		return color.NRGBA{R: 80, G: 200, B: 80, A: 255}
	case composeMagicRed:
		return color.NRGBA{R: 237, G: 80, B: 80, A: 255}
	default:
		return color.NRGBA{A: 255}
	}
}

func assignComposeMagicCustomColors(rows []composeMagicRow) {
	customIndex := 0
	for i := range rows {
		if rows[i].Assignment != composeMagicCustom {
			continue
		}
		c := overlayLayerPalette[customIndex%len(overlayLayerPalette)]
		rows[i].Color = color.NRGBA{R: c[0], G: c[1], B: c[2], A: 255}
		customIndex++
	}
}

func validateComposeMagicPlan(rows []composeMagicRow) error {
	primaryCounts := map[composeMagicAssignment]int{
		composeMagicBlue:  0,
		composeMagicGreen: 0,
		composeMagicRed:   0,
	}
	seenPaths := make(map[string]struct{}, len(rows))
	customCount := 0
	for i, row := range rows {
		if strings.TrimSpace(row.File.Path) == "" {
			return fmt.Errorf("row %d has an empty file path", i+1)
		}
		pathKey := strings.ToLower(filepath.Clean(row.File.Path))
		if _, exists := seenPaths[pathKey]; exists {
			return fmt.Errorf("row %d duplicates file %q", i+1, row.File.Path)
		}
		seenPaths[pathKey] = struct{}{}
		if row.Color.A != 255 {
			return fmt.Errorf("row %d color must be opaque", i+1)
		}
		switch row.Assignment {
		case composeMagicBlue, composeMagicGreen, composeMagicRed:
			primaryCounts[row.Assignment]++
		case composeMagicCustom:
			customCount++
		default:
			return fmt.Errorf("row %d has unknown channel assignment %q", i+1, row.Assignment)
		}
	}
	for _, assignment := range []composeMagicAssignment{composeMagicBlue, composeMagicGreen, composeMagicRed} {
		if primaryCounts[assignment] != 1 {
			return fmt.Errorf("exactly one %s channel is required (found %d)", assignment, primaryCounts[assignment])
		}
	}
	if customCount > maxOverlayLayers {
		return fmt.Errorf("a maximum of %d custom channels is supported", maxOverlayLayers)
	}
	return nil
}

func validateComposeMagicCapacity(rows []composeMagicRow, existingCustomChannels int) error {
	customCount := 0
	for _, row := range rows {
		if row.Assignment == composeMagicCustom {
			customCount++
		}
	}
	remaining := maxOverlayLayers - existingCustomChannels
	if remaining < 0 {
		remaining = 0
	}
	if customCount > remaining {
		return fmt.Errorf("the filter set needs %d custom channels, but only %d colored layer slots remain", customCount, remaining)
	}
	return nil
}

func prepareComposeMagicBatch(ctx context.Context, spec composeMagicSpec, loader composeMagicLoader) (*composeMagicBatch, error) {
	stages := composeMagicStages{
		magic:    processing.ApplyMagicLevels,
		magicMTF: processing.ApplyMagicLevelsAndMTF,
		setMTF:   func(img *models.LoadedImage) { img.Mode = stretch.MTF },
		autoMTF:  processing.AutoMTFMidtone,
	}
	return prepareComposeMagicBatchWithStages(ctx, spec, loader, stages)
}

func prepareComposeMagicBatchWithStages(ctx context.Context, spec composeMagicSpec, loader composeMagicLoader, stages composeMagicStages) (*composeMagicBatch, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := composeMagicCanceled(ctx); err != nil {
		return nil, err
	}
	if err := validateComposeMagicPlan(spec.Rows); err != nil {
		return nil, err
	}
	if loader == nil {
		return nil, fmt.Errorf("compose Magic image loader is unavailable")
	}
	preset, err := composeMagicPreset(spec.Preset)
	if err != nil {
		return nil, err
	}

	rows := append([]composeMagicRow(nil), spec.Rows...)
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].File.FilterNumber != rows[j].File.FilterNumber {
			return rows[i].File.FilterNumber < rows[j].File.FilterNumber
		}
		left, right := strings.ToLower(rows[i].File.Name), strings.ToLower(rows[j].File.Name)
		if left != right {
			return left < right
		}
		return rows[i].File.Path < rows[j].File.Path
	})

	channels := make([]composeMagicPreparedChannel, len(rows))
	for i, row := range rows {
		if err := composeMagicCanceled(ctx); err != nil {
			return nil, err
		}
		img, loadErr := loader(row.File.Path)
		if err := composeMagicCanceled(ctx); err != nil {
			return nil, err
		}
		if loadErr != nil {
			return nil, fmt.Errorf("load %s: %w", composeMagicFileLabel(row.File), loadErr)
		}
		if img == nil {
			return nil, fmt.Errorf("load %s: no image data", composeMagicFileLabel(row.File))
		}
		if structureErr := validateComposeMagicImage(img); structureErr != nil {
			return nil, fmt.Errorf("load %s: %w", composeMagicFileLabel(row.File), structureErr)
		}
		channels[i] = composeMagicPreparedChannel{Row: row, Image: img}
	}

	for i := range channels {
		if err := composeMagicCanceled(ctx); err != nil {
			return nil, err
		}
		var res processing.MagicLevelsResult
		if stages.magicMTF != nil {
			res = stages.magicMTF(channels[i].Image, preset)
		} else {
			res = stages.magic(channels[i].Image, preset)
		}
		if err := composeMagicCanceled(ctx); err != nil {
			return nil, err
		}
		if res.ValidPixels == 0 {
			return nil, fmt.Errorf("process %s: Magic found no valid image samples", composeMagicFileLabel(channels[i].Row.File))
		}
		debuglog.Log(fmt.Sprintf(
			"Compose Magic batch[%s] %s: black=%.4g white=%.4g sky=%.4g sigma=%.4g clipLow=%.3f%% clipHigh=%.3f%% stars=%v(%.2f%%) whiteSrc=%s whiteN=%d(%.2f%%)",
			res.Preset, composeMagicFileLabel(channels[i].Row.File), res.Black, res.White, res.Background, res.Sigma,
			res.ClipLowPercent, res.ClipHighPercent, res.StarsExcluded, res.StarPixelPercent,
			res.WhiteSampleSource, res.WhiteSampleCount, res.WhiteSamplePercent))
		if err := composeMagicCanceled(ctx); err != nil {
			return nil, err
		}
		if stages.magicMTF == nil {
			stages.setMTF(channels[i].Image)
			if err := composeMagicCanceled(ctx); err != nil {
				return nil, err
			}
			stages.autoMTF(channels[i].Image)
		}
		if err := composeMagicCanceled(ctx); err != nil {
			return nil, err
		}
	}
	if err := composeMagicCanceled(ctx); err != nil {
		return nil, err
	}
	return &composeMagicBatch{Preset: spec.Preset, Channels: channels}, nil
}

func composeMagicCanceled(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("compose Magic batch canceled: %w", err)
	}
	return nil
}

func composeMagicFileLabel(file composeMagicFile) string {
	if strings.TrimSpace(file.Name) != "" {
		return file.Name
	}
	return filepath.Base(file.Path)
}

func validateComposeMagicImage(img *models.LoadedImage) error {
	width, height := img.HDU.Data.Width, img.HDU.Data.Height
	if width <= 0 || height <= 0 {
		return fmt.Errorf("invalid image dimensions %dx%d", width, height)
	}
	if width > int(^uint(0)>>1)/height {
		return fmt.Errorf("image dimensions %dx%d overflow", width, height)
	}
	required := width * height
	if len(img.HDU.Data.Pixels) == 0 {
		return fmt.Errorf("image has no pixels for dimensions %dx%d", width, height)
	}
	if len(img.HDU.Data.Pixels) < required {
		return fmt.Errorf("image has %d pixels, need %d for dimensions %dx%d", len(img.HDU.Data.Pixels), required, width, height)
	}
	return nil
}

func planComposeMagicInstall(batch *composeMagicBatch, imgs []*models.LoadedImage, existingLayerSlots []int) (*composeMagicInstallPlan, error) {
	if batch == nil {
		return nil, fmt.Errorf("compose Magic batch is unavailable")
	}
	rows := make([]composeMagicRow, len(batch.Channels))
	for i, channel := range batch.Channels {
		rows[i] = channel.Row
		if channel.Image == nil {
			return nil, fmt.Errorf("prepared channel %s has no image", composeMagicFileLabel(channel.Row.File))
		}
	}
	if err := validateComposeMagicPlan(rows); err != nil {
		return nil, err
	}
	if err := validateComposeMagicCapacity(rows, len(existingLayerSlots)); err != nil {
		return nil, err
	}

	plan := &composeMagicInstallPlan{}
	var customChannels []composeMagicPreparedChannel
	for _, channel := range batch.Channels {
		switch channel.Row.Assignment {
		case composeMagicBlue:
			plan.Base[0] = channel.Image
		case composeMagicGreen:
			plan.Base[1] = channel.Image
		case composeMagicRed:
			plan.Base[2] = channel.Image
		case composeMagicCustom:
			customChannels = append(customChannels, channel)
		}
	}

	used := make(map[int]bool, len(existingLayerSlots))
	for _, slot := range existingLayerSlots {
		if slot < 3 || slot >= 3+maxOverlayLayers || used[slot] {
			return nil, fmt.Errorf("existing colored layer slot %d is invalid", slot)
		}
		used[slot] = true
	}
	available := make([]int, 0, maxOverlayLayers-len(existingLayerSlots))
	for slot := 3; slot < 3+maxOverlayLayers; slot++ {
		if used[slot] {
			continue
		}
		if slot >= len(imgs) || imgs[slot] == nil {
			available = append(available, slot)
		}
	}
	if len(customChannels) > len(available) {
		return nil, fmt.Errorf("the filter set needs %d custom channels, but only %d installable colored layer slots remain", len(customChannels), len(available))
	}
	for i, channel := range customChannels {
		plan.Customs = append(plan.Customs, composeMagicCustomInstall{Channel: channel, Slot: available[i]})
	}
	return plan, nil
}

func composeMagicPreset(label string) (processing.MagicPreset, error) {
	switch label {
	case "Balanced":
		return processing.MagicBalanced, nil
	case "Nebula":
		return processing.MagicNebula, nil
	case "Galaxy":
		return processing.MagicGalaxy, nil
	default:
		return processing.MagicBalanced, fmt.Errorf("unknown Magic preset %q", label)
	}
}
