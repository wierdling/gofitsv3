package ui

import (
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
)

func clearComposeOrigPixels(origPixels *[][]float32, idxs ...int) {
	if origPixels == nil {
		return
	}
	for _, idx := range idxs {
		if idx < 0 || idx >= len(*origPixels) {
			continue
		}
		(*origPixels)[idx] = nil
	}
}

// replaceComposeChannelImage installs a new RGB image while preserving the
// current channel orientation. Project loading and Reset Data intentionally
// restore their saved state directly instead.
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
