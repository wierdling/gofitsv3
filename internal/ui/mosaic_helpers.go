package ui

import (
	"fmt"
	"image"
	"image/color"
	"math"

	"gofitsv3/internal/debuglog"
	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/mosaic"
	"gofitsv3/internal/processing"
	"gofitsv3/internal/stretch"
)

// buildMosaicPreviewImageWithLevels renders a mosaic result to RGBA using the same
// ApplyStretchParallel pipeline used throughout the rest of the application.
func buildMosaicPreviewImageWithLevels(result *mosaic.Result, black, white, background, peak, scaledPeak float64, mode stretch.Mode, mtfMidtone float64) *image.RGBA {
	debuglog.Log(fmt.Sprintf("buildMosaicPreviewImage: start %dx%d", result.Width, result.Height))
	img := &models.LoadedImage{
		HDU: fitsio.HDU{
			Data: fitsio.ImageData{
				Pixels: result.Pixels,
				Width:  result.Width,
				Height: result.Height,
			},
		},
		Mode:       mode,
		Black:      black,
		White:      white,
		Background: background,
		Peak:       peak,
		ScaledPeak: scaledPeak,
		MTFMidtone: mtfMidtone,
	}
	debuglog.Log("buildMosaicPreviewImage: ApplyStretchParallel")
	stretched, mask := processing.ApplyStretchParallel(img)
	if mask == nil {
		mask = make([]byte, len(stretched.Pixels))
	}
	debuglog.Log("buildMosaicPreviewImage: ToGrayRGBA")
	rgba := processing.ToGrayRGBA(stretched, mask)
	debuglog.Log(fmt.Sprintf("buildMosaicPreviewImage: drawInputBorders (%d footprints)", len(result.InputFootprints)))
	drawInputBorders(rgba, result.InputFootprints)
	debuglog.Log("buildMosaicPreviewImage: done")
	return rgba
}

// drawInputBorders draws a 5-pixel pure-white border around each input image
// footprint so the seam between images is visible in the preview.
// corners order: TL, TR, BL, BR (matching mosaic.imageCorners).
func drawInputBorders(img *image.RGBA, footprints [][4][2]float64) {
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	for _, fp := range footprints {
		// Draw edges: TL→TR, TR→BR, BR→BL, BL→TL
		pairs := [4][2]int{{0, 1}, {1, 3}, {3, 2}, {2, 0}}
		for _, p := range pairs {
			drawThickLine(img, fp[p[0]], fp[p[1]], 5, white)
		}
	}
}

// drawThickLine draws a line from a to b with the given thickness (in pixels)
// using a simple perpendicular offset approach.
func drawThickLine(img *image.RGBA, a, b [2]float64, thickness int, c color.RGBA) {
	dx := b[0] - a[0]
	dy := b[1] - a[1]
	length := math.Sqrt(dx*dx + dy*dy)
	if length == 0 || math.IsNaN(length) || math.IsInf(length, 0) || length > 1e7 {
		return
	}
	// Perpendicular unit vector.
	px := -dy / length
	py := dx / length
	half := float64(thickness) / 2.0
	steps := int(length) + 1
	for s := 0; s <= steps; s++ {
		t := float64(s) / float64(steps)
		cx := a[0] + t*dx
		cy := a[1] + t*dy
		for d := -half; d <= half; d += 0.5 {
			ix := int(math.Round(cx + d*px))
			iy := int(math.Round(cy + d*py))
			if ix >= 0 && iy >= 0 && ix < img.Bounds().Max.X && iy < img.Bounds().Max.Y {
				img.SetRGBA(ix, iy, c)
			}
		}
	}
}

// centroidNearPreview finds the flux-weighted centroid of the nearest bright
// point source within searchRadius pixels of (x, y), operating on the R channel
// of the stretched preview image. Because the preview has already been
// background-subtracted and stretched, stars appear as sharp bright peaks and
// the centroid is far more reliable than on raw float32 drizzle data.
func centroidNearPreview(img *image.RGBA, x, y float64, searchRadius int) (float64, float64, bool) {
	b := img.Bounds()
	w := b.Dx()
	h := b.Dy()

	clamp := func(v, lo, hi int) int {
		if v < lo {
			return lo
		}
		if v > hi {
			return hi
		}
		return v
	}
	pixel := func(px, py int) int {
		return int(img.RGBAAt(b.Min.X+px, b.Min.Y+py).R)
	}

	cx := int(math.Round(x))
	cy := int(math.Round(y))
	cx = clamp(cx, 0, w-1)
	cy = clamp(cy, 0, h-1)

	// Hill-climb from the click position to the nearest local brightness maximum.
	// This finds the closest bright peak rather than the globally brightest pixel
	// in the search box, so clicking one star won't snap to a brighter nearby star.
	for step := 0; step < 30; step++ {
		best := pixel(cx, cy)
		bx, by := cx, cy
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				nx := clamp(cx+dx, 0, w-1)
				ny := clamp(cy+dy, 0, h-1)
				if v := pixel(nx, ny); v > best {
					best = v
					bx, by = nx, ny
				}
			}
		}
		if bx == cx && by == cy {
			break
		}
		// Stop if we've wandered too far from the original click.
		ddx := bx - int(math.Round(x))
		ddy := by - int(math.Round(y))
		if ddx*ddx+ddy*ddy > searchRadius*searchRadius {
			break
		}
		cx, cy = bx, by
	}

	peakX, peakY := cx, cy
	peakVal := pixel(peakX, peakY)

	// Reject if the peak is too dim to be a real source.
	if peakVal < 16 {
		return x, y, false
	}

	// Flux-weighted centroid in a ±7 pixel window around the peak.
	const hw = 7
	c0x, c1x := peakX-hw, peakX+hw
	c0y, c1y := peakY-hw, peakY+hw
	if c0x < 0 {
		c0x = 0
	}
	if c1x >= w {
		c1x = w - 1
	}
	if c0y < 0 {
		c0y = 0
	}
	if c1y >= h {
		c1y = h - 1
	}

	var sumX, sumY, sumW float64
	for py := c0y; py <= c1y; py++ {
		for px := c0x; px <= c1x; px++ {
			v := float64(img.RGBAAt(b.Min.X+px, b.Min.Y+py).R)
			sumX += float64(px) * v
			sumY += float64(py) * v
			sumW += v
		}
	}
	if sumW == 0 {
		return x, y, false
	}
	return sumX / sumW, sumY / sumW, true
}

func modeNameForMode(m stretch.Mode) string {
	switch m {
	case stretch.Linear:
		return "Linear"
	case stretch.Log:
		return "Log"
	case stretch.Sqrt:
		return "Sqrt"
	case stretch.HistEq:
		return "HistEq"
	case stretch.MTF:
		return "MTF"
	default:
		return "Asinh"
	}
}
