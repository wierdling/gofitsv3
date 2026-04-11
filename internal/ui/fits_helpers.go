package ui

import (
	"fmt"

	"gofitsv3/internal/badpix"
	"gofitsv3/internal/fitsio"
)

// cleanHDUWithDQ runs bad-pixel masking using the file's DQ extension when available.
func cleanHDUWithDQ(hdu fitsio.HDU, file *fitsio.File) (fitsio.HDU, error) {
	dq := file.SelectDQ()
	if dq == nil {
		return hdu, fmt.Errorf("DQ not found")
	}
	mask, err := badpix.MaskFromDQ(hdu, *dq, 0)
	if err != nil {
		return hdu, err
	}
	hdu.Data = badpix.InterpolateBicubic(hdu.Data, mask)
	return hdu, nil
}
