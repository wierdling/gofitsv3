package ui

import (
	"fmt"

	"gofitsv3/internal/badpix"
	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/instrument"
)

// cleanHDUWithDQ runs bad-pixel masking using the file's DQ extension when available.
// It matches the DQ extension by EXTVER when the SCI HDU has one, and selects
// bad-pixel bits based on the instrument declared in the primary header.
func cleanHDUWithDQ(hdu fitsio.HDU, file *fitsio.File) (fitsio.HDU, error) {
	extver := hdu.Header.Cards["EXTVER"]
	dq := file.GetHDUByExtVer("DQ", extver)
	if dq == nil {
		return hdu, fmt.Errorf("DQ not found")
	}
	inst, _ := instrument.FromHeader(file.HDUs[0].Header)
	mask, err := badpix.MaskFromDQ(hdu, *dq, inst.BadDQBits)
	if err != nil {
		return hdu, err
	}
	hdu.Data = badpix.RepairMaskedPixels(hdu.Data, mask)
	return hdu, nil
}
