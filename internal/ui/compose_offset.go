package ui

import (
	"math"

	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
)

// composeManualOffsetTransform builds the backward (output→source) sampling
// transform for a channel's Manual Offset: a rotation by rot degrees about the
// image centre followed by a (dx, dy) pixel shift of the image content. It is
// the single definition shared by Apply Offset, Auto-Align, and blink so the
// Manual Offset fields are the one source of truth for a channel's placement.
func composeManualOffsetTransform(w, h int, dx, dy, rot float64) processing.AffineTransform {
	cx := float64(w) / 2
	cy := float64(h) / 2
	rad := rot * math.Pi / 180
	cosA := math.Cos(rad)
	sinA := math.Sin(rad)
	return processing.AffineTransform{
		A: cosA, B: sinA,
		C: -cosA*(cx+dx) - sinA*(cy+dy) + cx,
		D: -sinA, E: cosA,
		F: sinA*(cx+dx) - cosA*(cy+dy) + cy,
	}
}

// extractManualOffset is the inverse of composeManualOffsetTransform: given a
// backward sampling transform, it recovers the (dx, dy, rot) Manual Offset
// fields that reproduce it. It is exact for a rotation+translation (scale 1);
// any scale component is ignored (Auto-Align is captured as translation+rotation
// per the fields-as-source-of-truth model).
func extractManualOffset(t processing.AffineTransform, w, h int) (dx, dy, rot float64) {
	cx := float64(w) / 2
	cy := float64(h) / 2
	rad := math.Atan2(t.B, t.A)
	cosA := math.Cos(rad)
	sinA := math.Sin(rad)
	// Solve composeManualOffsetTransform's C/F equations for u=cx+dx, v=cy+dy.
	// The 2×2 system has determinant 1 (cos²+sin²).
	u := -cosA*(t.C-cx) + sinA*(t.F-cy)
	v := -sinA*(t.C-cx) - cosA*(t.F-cy)
	return u - cx, v - cy, rad * 180 / math.Pi
}

// channelAlignTransform returns the channel's stored star-alignment affine
// (backward sampling) and whether one is present.
func channelAlignTransform(img *models.LoadedImage) (processing.AffineTransform, bool) {
	if img == nil || !img.HasAlignTransform {
		return processing.AffineTransform{}, false
	}
	return processing.AffineTransform{
		A: img.AlignA, B: img.AlignB, C: img.AlignC,
		D: img.AlignD, E: img.AlignE, F: img.AlignF,
	}, true
}

// setChannelAlignTransform stores the full fitted alignment affine on the image.
// Unlike the Manual Offset fields (translation+rotation only), this preserves the
// scale and skew of the fit, which are applied at render time.
func setChannelAlignTransform(img *models.LoadedImage, t processing.AffineTransform) {
	if img == nil {
		return
	}
	img.HasAlignTransform = true
	img.AlignA, img.AlignB, img.AlignC = t.A, t.B, t.C
	img.AlignD, img.AlignE, img.AlignF = t.D, t.E, t.F
}
