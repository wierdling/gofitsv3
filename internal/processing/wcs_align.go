package processing

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"gofitsv3/internal/fitsio"
)

type linearWCS struct {
	crpix1 float64
	crpix2 float64
	crval1 float64
	crval2 float64
	cd11   float64
	cd12   float64
	cd21   float64
	cd22   float64
}

func AlignChannelUsingWCS(targetPixels []float32, targetWidth, targetHeight int, targetHeader fitsio.Header, refPixels []float32, refWidth, refHeight int, refHeader fitsio.Header) ([]float32, AffineTransform, error) {
	if targetWidth <= 0 || targetHeight <= 0 || refWidth <= 0 || refHeight <= 0 {
		return nil, AffineTransform{}, fmt.Errorf("invalid image dimensions (target: %dx%d, ref: %dx%d)", targetWidth, targetHeight, refWidth, refHeight)
	}

	transform, err := ComputeWCSTransform(targetHeader, refHeader)
	if err != nil {
		return nil, AffineTransform{}, err
	}

	alignedPixels := WarpImageToSize(targetPixels, targetWidth, targetHeight, refWidth, refHeight, transform)
	if len(alignedPixels) != refWidth*refHeight {
		return nil, AffineTransform{}, fmt.Errorf("unexpected aligned pixel count: got %d want %d", len(alignedPixels), refWidth*refHeight)
	}
	return alignedPixels, transform, nil
}

func ComputeWCSTransform(targetHeader fitsio.Header, refHeader fitsio.Header) (AffineTransform, error) {
	targetWCS, err := parseLinearWCS(targetHeader)
	if err != nil {
		return AffineTransform{}, err
	}
	refWCS, err := parseLinearWCS(refHeader)
	if err != nil {
		return AffineTransform{}, err
	}

	x00, y00, err := mapRefPixelToTargetPixel(0, 0, refWCS, targetWCS)
	if err != nil {
		return AffineTransform{}, err
	}
	x10, y10, err := mapRefPixelToTargetPixel(1, 0, refWCS, targetWCS)
	if err != nil {
		return AffineTransform{}, err
	}
	x01, y01, err := mapRefPixelToTargetPixel(0, 1, refWCS, targetWCS)
	if err != nil {
		return AffineTransform{}, err
	}

	return AffineTransform{
		A: x10 - x00,
		B: x01 - x00,
		C: x00,
		D: y10 - y00,
		E: y01 - y00,
		F: y00,
	}, nil
}

func parseLinearWCS(header fitsio.Header) (linearWCS, error) {
	crpix1, err := headerFloat(header, "CRPIX1")
	if err != nil {
		return linearWCS{}, err
	}
	crpix2, err := headerFloat(header, "CRPIX2")
	if err != nil {
		return linearWCS{}, err
	}
	crval1, err := headerFloat(header, "CRVAL1")
	if err != nil {
		return linearWCS{}, err
	}
	crval2, err := headerFloat(header, "CRVAL2")
	if err != nil {
		return linearWCS{}, err
	}

	cd11, ok11 := tryHeaderFloat(header, "CD1_1")
	cd12, ok12 := tryHeaderFloat(header, "CD1_2")
	cd21, ok21 := tryHeaderFloat(header, "CD2_1")
	cd22, ok22 := tryHeaderFloat(header, "CD2_2")
	if ok11 && ok12 && ok21 && ok22 {
		return linearWCS{crpix1: crpix1, crpix2: crpix2, crval1: crval1, crval2: crval2, cd11: cd11, cd12: cd12, cd21: cd21, cd22: cd22}, nil
	}

	cdelt1, err1 := headerFloat(header, "CDELT1")
	cdelt2, err2 := headerFloat(header, "CDELT2")
	if err1 != nil || err2 != nil {
		return linearWCS{}, fmt.Errorf("missing WCS matrix: need CD* or PC*+CDELT* keywords")
	}

	pc11, ok11 := tryHeaderFloat(header, "PC1_1")
	pc12, ok12 := tryHeaderFloat(header, "PC1_2")
	pc21, ok21 := tryHeaderFloat(header, "PC2_1")
	pc22, ok22 := tryHeaderFloat(header, "PC2_2")
	if ok11 && ok12 && ok21 && ok22 {
		return linearWCS{
			crpix1: crpix1,
			crpix2: crpix2,
			crval1: crval1,
			crval2: crval2,
			cd11:   pc11 * cdelt1,
			cd12:   pc12 * cdelt2,
			cd21:   pc21 * cdelt1,
			cd22:   pc22 * cdelt2,
		}, nil
	}

	return linearWCS{crpix1: crpix1, crpix2: crpix2, crval1: crval1, crval2: crval2, cd11: cdelt1, cd12: 0, cd21: 0, cd22: cdelt2}, nil
}

func mapRefPixelToTargetPixel(refX, refY float64, refWCS, targetWCS linearWCS) (float64, float64, error) {
	world1, world2 := pixelToWorldLinear(refX, refY, refWCS)
	return worldToPixelLinear(world1, world2, targetWCS)
}

func pixelToWorldLinear(x, y float64, w linearWCS) (float64, float64) {
	dx := (x + 1) - w.crpix1
	dy := (y + 1) - w.crpix2
	world1 := w.crval1 + w.cd11*dx + w.cd12*dy
	world2 := w.crval2 + w.cd21*dx + w.cd22*dy
	return world1, world2
}

func worldToPixelLinear(world1, world2 float64, w linearWCS) (float64, float64, error) {
	det := w.cd11*w.cd22 - w.cd12*w.cd21
	if math.Abs(det) < 1e-18 {
		return 0, 0, fmt.Errorf("singular WCS matrix")
	}
	d1 := normalizeAngleDelta(world1 - w.crval1)
	d2 := world2 - w.crval2
	dx := (w.cd22*d1 - w.cd12*d2) / det
	dy := (-w.cd21*d1 + w.cd11*d2) / det
	return dx + w.crpix1 - 1, dy + w.crpix2 - 1, nil
}

func headerFloat(header fitsio.Header, key string) (float64, error) {
	if v, ok := tryHeaderFloat(header, key); ok {
		return v, nil
	}
	return 0, fmt.Errorf("missing %s in FITS header", key)
}

func tryHeaderFloat(header fitsio.Header, key string) (float64, bool) {
	raw, ok := header.Cards[key]
	if !ok {
		return 0, false
	}
	val := raw
	if idx := strings.Index(val, "/"); idx >= 0 {
		val = val[:idx]
	}
	val = strings.TrimSpace(strings.Trim(val, "'"))
	if val == "" {
		return 0, false
	}
	parsed, err := strconv.ParseFloat(val, 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func normalizeAngleDelta(delta float64) float64 {
	for delta > 180 {
		delta -= 360
	}
	for delta < -180 {
		delta += 360
	}
	return delta
}
