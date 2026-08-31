package ui

import (
	"context"
	"image"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/histogram"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
)

type composeOverlayPreviewData struct {
	image      *image.RGBA
	bins       [256]int
	width      int
	height     int
	sky        float64
	mean       float64
	std        float64
	filterText string
}

type composeGlobalMagicTarget struct {
	index       int
	image       models.LoadedImage
	independent bool
}

type composeGlobalMagicResult struct {
	index   int
	image   models.LoadedImage
	preview *composeOverlayPreviewData
}

func composeLoadRequestCurrent(currentSession, requestSession, currentRequest, request uint64) bool {
	return currentSession == requestSession && currentRequest == request
}

// prepareComposeGlobalMagic applies the shared Magic operation to every loaded
// channel and also prepares the independently displayed channel previews. The
// caller publishes the complete result atomically on the UI thread.
func prepareComposeGlobalMagic(ctx context.Context, targets []composeGlobalMagicTarget, preset processing.MagicPreset) ([]composeGlobalMagicResult, error) {
	results := make([]composeGlobalMagicResult, len(targets))
	for i, target := range targets {
		if err := composeMagicCanceled(ctx); err != nil {
			return nil, err
		}
		img := target.image
		processing.ApplyMagicLevels(&img, preset)
		processing.AutoMTFMidtone(&img)
		result := composeGlobalMagicResult{index: target.index, image: img}
		if target.independent {
			preview, err := buildComposeOverlayPreviewData(ctx, &img)
			if err != nil {
				return nil, err
			}
			result.preview = preview
		}
		results[i] = result
	}
	return results, nil
}

func buildComposeOverlayPreviewData(ctx context.Context, img *models.LoadedImage) (*composeOverlayPreviewData, error) {
	if err := composeMagicCanceled(ctx); err != nil {
		return nil, err
	}
	stretched, mask := processing.ApplyStretchParallel(img)
	if err := composeMagicCanceled(ctx); err != nil {
		return nil, err
	}
	stats := histogram.Compute(stretched.Pixels)
	sky, _ := processing.EstimateBackground(stretched.Pixels)
	return &composeOverlayPreviewData{
		image:      processing.ToGrayRGBA(stretched, mask),
		bins:       stats.Hist,
		width:      stretched.Width,
		height:     stretched.Height,
		sky:        sky,
		mean:       stats.Mean,
		std:        stats.Std,
		filterText: fitsio.FilterString(img.Primary),
	}, nil
}

// replaceComposeChannelImage installs a replacement while preserving the
// current channel orientation. Project loading and Reset Data restore their
// saved state directly instead.
func replaceComposeChannelImage(imgs []*models.LoadedImage, idx int, replacement *models.LoadedImage) {
	if idx < 0 || idx >= len(imgs) {
		return
	}
	rotation90 := 0
	if imgs[idx] != nil {
		rotation90 = imgs[idx].Rotation90
	}
	imgs[idx] = replacement
	restoreComposeChannelRotation(replacement, rotation90)
}

func rotateComposeChannel90CW(img *models.LoadedImage) {
	if img == nil {
		return
	}
	img.HDU.Data = processing.RotateImageData90CW(img.HDU.Data)
	img.Rotation90 = (img.Rotation90 + 1) % 4
}

func restoreComposeChannelRotation(img *models.LoadedImage, rotation90 int) {
	if img == nil {
		return
	}
	img.Rotation90 = 0
	for turns := rotation90 % 4; turns > 0; turns-- {
		rotateComposeChannel90CW(img)
	}
}

func clearComposeChannelAlignment(img *models.LoadedImage) {
	if img == nil {
		return
	}
	img.HasAlignTransform = false
	img.AlignA, img.AlignB, img.AlignC = 0, 0, 0
	img.AlignD, img.AlignE, img.AlignF = 0, 0, 0
}
