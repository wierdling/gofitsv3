package mosaic

import (
	"fmt"

	"gofitsv3/internal/fitsio"
)

func loadBinaryMaskFITS(path string) ([]bool, int, int, error) {
	file, err := fitsio.LoadFile(path)
	if err != nil {
		return nil, 0, 0, err
	}
	var hdu *fitsio.HDU
	if sci := file.SelectSCI(); len(sci) > 0 {
		hdu = &sci[0]
	} else if len(file.HDUs) > 0 && len(file.HDUs[0].Data.Pixels) > 0 {
		hdu = &file.HDUs[0]
	}
	if hdu == nil || len(hdu.Data.Pixels) == 0 {
		return nil, 0, 0, fmt.Errorf("no image data")
	}
	mask := make([]bool, len(hdu.Data.Pixels))
	for i, v := range hdu.Data.Pixels {
		mask[i] = isFinite32(v) && v != 0
	}
	return mask, hdu.Data.Width, hdu.Data.Height, nil
}
