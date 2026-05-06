package badpix

import (
	"errors"

	"gofitsv3/internal/badpix"
	"gofitsv3/internal/fitsio"
)

// Config controls bad-pixel cleaning behavior.
type Config struct {
	// BadBits selects which DQ bits mark a pixel as bad. If zero, any non-zero DQ marks bad.
	BadBits uint32
	// AllowMissingDQ permits processing even when no DQ extension is found; pixels are left unchanged.
	AllowMissingDQ bool
}

// Clean loads a FITS file, builds a bad-pixel mask from its DQ extension, and returns a cleaned image.
// If no DQ extension exists and AllowMissingDQ is false, an error is returned.
func Clean(path string, cfg Config) (*fitsio.ImageData, []bool, error) {
	file, err := fitsio.LoadFile(path)
	if err != nil {
		return nil, nil, err
	}

	var sci fitsio.HDU
	if sciHDUs := file.SelectSCI(); len(sciHDUs) > 0 {
		sci = sciHDUs[0]
	} else if len(file.HDUs) > 0 {
		sci = file.HDUs[0]
	} else {
		return nil, nil, errors.New("no HDUs available")
	}

	dq := file.SelectDQ()
	if dq == nil {
		if !cfg.AllowMissingDQ {
			return nil, nil, errors.New("DQ extension not found")
		}
		// Nothing to do; return original image.
		img := sci.Data
		return &img, make([]bool, len(img.Pixels)), nil
	}

	mask, err := badpix.MaskFromDQ(sci, *dq, cfg.BadBits)
	if err != nil {
		return nil, nil, err
	}
	cleaned := badpix.RepairMaskedPixels(sci.Data, mask)
	return &cleaned, mask, nil
}
