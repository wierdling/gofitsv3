package mosaic

import (
	"fmt"
	"math"
	"path/filepath"

	"gofitsv3/internal/badpix"
	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/processing"
)

// LoadInputFromPath loads a single mosaic input. If the FITS file contains
// multiple SCI extensions, they are resampled onto one shared canvas using
// their WCS headers so the drizzle pipeline receives one image per file.
func LoadInputFromPath(path string) (Input, error) {
	file, err := fitsio.LoadFile(path)
	if err != nil {
		return Input{}, err
	}
	if len(file.HDUs) == 0 {
		return Input{}, fmt.Errorf("no HDUs found in %s", path)
	}

	primary := file.HDUs[0].Header
	sci := file.SelectSCI()
	if len(sci) == 0 {
		hdu := cleanSCIWithMatchingDQ(file.HDUs[0], file)
		return Input{Path: path, PrimaryHeader: primary, HDU: hdu}, nil
	}
	if len(sci) == 1 {
		hdu := cleanSCIWithMatchingDQ(sci[0], file)
		return Input{Path: path, PrimaryHeader: primary, HDU: hdu}, nil
	}

	hdu, err := combineSCIHDUs(path, primary, sci, file)
	if err != nil {
		return Input{}, err
	}
	return Input{Path: path, PrimaryHeader: primary, HDU: hdu}, nil
}

func combineSCIHDUs(path string, primary fitsio.Header, sci []fitsio.HDU, file *fitsio.File) (fitsio.HDU, error) {
	cleaned := make([]fitsio.HDU, len(sci))
	for i := range sci {
		cleaned[i] = cleanSCIWithMatchingDQ(sci[i], file)
	}

	ref := cleaned[0]
	transforms := make([]processing.AffineTransform, len(cleaned))
	transforms[0] = processing.IdentityTransform()

	minX, minY := 0.0, 0.0
	maxX := float64(ref.Data.Width - 1)
	maxY := float64(ref.Data.Height - 1)

	for i := 1; i < len(cleaned); i++ {
		refToSCI, err := processing.ComputeWCSTransform(cleaned[i].Header, ref.Header)
		if err != nil {
			return fitsio.HDU{}, fmt.Errorf("combine %s SCI[%d]: %w", filepath.Base(path), i+1, err)
		}
		sciToRef, err := processing.InvertAffineTransform(refToSCI)
		if err != nil {
			return fitsio.HDU{}, fmt.Errorf("combine %s SCI[%d]: %w", filepath.Base(path), i+1, err)
		}
		transforms[i] = sciToRef

		for _, corner := range imageCorners(cleaned[i].Data.Width, cleaned[i].Data.Height) {
			x, y := processing.ApplyAffineTransform(sciToRef, corner[0], corner[1])
			if x < minX {
				minX = x
			}
			if x > maxX {
				maxX = x
			}
			if y < minY {
				minY = y
			}
			if y > maxY {
				maxY = y
			}
		}
	}

	width := int(math.Ceil(maxX - minX + 1))
	height := int(math.Ceil(maxY - minY + 1))
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}

	sums := make([]float32, width*height)
	weights := make([]float32, width*height)
	for i := range cleaned {
		hdu := cleaned[i]
		for y := 0; y < hdu.Data.Height; y++ {
			for x := 0; x < hdu.Data.Width; x++ {
				idx := y*hdu.Data.Width + x
				val := float64(hdu.Data.Pixels[idx])
				if math.IsNaN(val) || math.IsInf(val, 0) {
					continue
				}
				refX, refY := processing.ApplyAffineTransform(transforms[i], float64(x), float64(y))
				drizzlePixel(sums, weights, width, height, refX-minX, refY-minY, 1, float32(val))
			}
		}
	}

	pixels := make([]float32, len(sums))
	for i := range pixels {
		if weights[i] == 0 {
			pixels[i] = float32(math.NaN())
			continue
		}
		pixels[i] = sums[i] / weights[i]
	}

	return fitsio.HDU{
		Header:  buildCombinedInputHeader(path, primary, ref.Header, width, height, minX, minY, len(cleaned)),
		Data:    fitsio.ImageData{Width: width, Height: height, Pixels: pixels},
		ExtName: "SCI",
	}, nil
}

func buildCombinedInputHeader(path string, primary, ref fitsio.Header, width, height int, originX, originY float64, combinedCount int) fitsio.Header {
	merged := mergeHeaders(primary, ref)
	cards := fitsio.CloneHeader(merged).Cards

	for _, key := range []string{
		"END", "SIMPLE", "BITPIX", "NAXIS", "NAXIS1", "NAXIS2", "XTENSION", "PCOUNT", "GCOUNT",
		"CHECKSUM", "DATASUM", "BSCALE", "BZERO", "LTV1", "LTV2",
	} {
		delete(cards, key)
	}
	cards["OBJECT"] = firstNonEmpty(cards["OBJECT"], quotedString(filepath.Base(path)))
	cards["IMAGETYP"] = quotedString("SCI-COMB")
	cards["NCOMBINE"] = fmt.Sprintf("%d", combinedCount)
	cards["ORIGOFFX"] = formatFloat(originX)
	cards["ORIGOFFY"] = formatFloat(originY)
	cards["EXTNAME"] = quotedString("SCI")
	cards["EXTEND"] = "T"
	cards["NAXIS1"] = fmt.Sprintf("%d", width)
	cards["NAXIS2"] = fmt.Sprintf("%d", height)

	if crpix1, ok := fitsio.HeaderFloat(ref, "CRPIX1"); ok {
		cards["CRPIX1"] = formatFloat(crpix1 - originX)
	}
	if crpix2, ok := fitsio.HeaderFloat(ref, "CRPIX2"); ok {
		cards["CRPIX2"] = formatFloat(crpix2 - originY)
	}

	return fitsio.Header{Cards: cards}
}

func cleanSCIWithMatchingDQ(hdu fitsio.HDU, file *fitsio.File) fitsio.HDU {
	dq := matchingDQHDU(file, hdu)
	if dq == nil {
		return hdu
	}
	mask, err := badpix.MaskFromDQ(hdu, *dq, 0)
	if err != nil {
		return hdu
	}
	hdu.Data = badpix.InterpolateBicubic(hdu.Data, mask)
	return hdu
}

func matchingDQHDU(file *fitsio.File, sci fitsio.HDU) *fitsio.HDU {
	sciExtver := fitsio.HeaderString(sci.Header, "EXTVER")
	var sizeMatch *fitsio.HDU
	for i := range file.HDUs {
		hdu := &file.HDUs[i]
		if hdu.ExtName != "DQ" {
			continue
		}
		if sciExtver != "" && sciExtver == fitsio.HeaderString(hdu.Header, "EXTVER") {
			if hdu.Data.Width == sci.Data.Width && hdu.Data.Height == sci.Data.Height {
				return hdu
			}
		}
		if sizeMatch == nil && hdu.Data.Width == sci.Data.Width && hdu.Data.Height == sci.Data.Height {
			sizeMatch = hdu
		}
	}
	return sizeMatch
}
