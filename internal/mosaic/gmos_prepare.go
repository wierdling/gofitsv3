package mosaic

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"gofitsv3/internal/fitsio"
)

// prepareGMOSHDU trims a raw GMOS image to DATASEC after subtracting the
// per-row median of BIASSEC. It is deliberately limited to rectangular,
// non-reversed sections; calibrated products do not enter this path.
func prepareGMOSHDU(hdu fitsio.HDU) (fitsio.HDU, error) {
	data, err := prepareGMOSPixels(hdu.Header, hdu.Data)
	if err != nil {
		return hdu, err
	}
	hdu.Data = data
	shiftGMOSWCS(&hdu.Header, data.Width, data.Height)
	return hdu, nil
}

func prepareGMOSMetadata(hdu fitsio.HDU) (fitsio.HDU, error) {
	sec, err := parseGMOSSection(fitsio.HeaderString(hdu.Header, "DATASEC"))
	if err != nil {
		return hdu, err
	}
	hdu.Data.Width, hdu.Data.Height = sec.x2-sec.x1+1, sec.y2-sec.y1+1
	shiftGMOSWCS(&hdu.Header, 0, 0)
	return hdu, nil
}

type gmosSection struct{ x1, x2, y1, y2 int }

func parseGMOSSection(raw string) (gmosSection, error) {
	s := strings.TrimSpace(strings.Trim(raw, "'"))
	if len(s) < 9 || s[0] != '[' || s[len(s)-1] != ']' {
		return gmosSection{}, fmt.Errorf("GMOS DATASEC is missing or malformed")
	}
	parts := strings.Split(strings.Trim(s[1:len(s)-1], " "), ",")
	if len(parts) != 2 {
		return gmosSection{}, fmt.Errorf("GMOS section %q is malformed", raw)
	}
	parseRange := func(v string) (int, int, error) {
		p := strings.Split(strings.TrimSpace(v), ":")
		if len(p) != 2 {
			return 0, 0, fmt.Errorf("GMOS section %q is malformed", raw)
		}
		a, e1 := strconv.Atoi(strings.TrimSpace(p[0]))
		b, e2 := strconv.Atoi(strings.TrimSpace(p[1]))
		if e1 != nil || e2 != nil || a < 1 || b < a {
			return 0, 0, fmt.Errorf("GMOS section %q is reversed or invalid", raw)
		}
		return a, b, nil
	}
	x1, x2, e := parseRange(parts[0])
	if e != nil {
		return gmosSection{}, e
	}
	y1, y2, e := parseRange(parts[1])
	if e != nil {
		return gmosSection{}, e
	}
	return gmosSection{x1, x2, y1, y2}, nil
}

func prepareGMOSPixels(h fitsio.Header, in fitsio.ImageData) (fitsio.ImageData, error) {
	dataSec, err := parseGMOSSection(fitsio.HeaderString(h, "DATASEC"))
	if err != nil {
		return in, err
	}
	biasSec, err := parseGMOSSection(fitsio.HeaderString(h, "BIASSEC"))
	if err != nil {
		return in, err
	}
	if in.Width < dataSec.x2 || in.Height < dataSec.y2 || in.Width < biasSec.x2 || in.Height < biasSec.y2 || len(in.Pixels) < in.Width*in.Height {
		return in, fmt.Errorf("GMOS section exceeds decoded image dimensions")
	}
	w, ht := dataSec.x2-dataSec.x1+1, dataSec.y2-dataSec.y1+1
	out := make([]float32, w*ht)
	for y := dataSec.y1; y <= dataSec.y2; y++ {
		vals := make([]float64, 0, biasSec.x2-biasSec.x1+1)
		for x := biasSec.x1; x <= biasSec.x2; x++ {
			v := float64(in.Pixels[(y-1)*in.Width+x-1])
			if !math.IsNaN(v) && !math.IsInf(v, 0) {
				vals = append(vals, v)
			}
		}
		if len(vals) == 0 {
			return in, fmt.Errorf("GMOS BIASSEC has no finite pixels on row %d", y)
		}
		sort.Float64s(vals)
		bias := vals[len(vals)/2]
		for x := dataSec.x1; x <= dataSec.x2; x++ {
			v := in.Pixels[(y-1)*in.Width+x-1]
			if !math.IsNaN(float64(v)) && !math.IsInf(float64(v), 0) {
				v -= float32(bias)
			}
			out[(y-dataSec.y1)*w+x-dataSec.x1] = v
		}
	}
	return fitsio.ImageData{Width: w, Height: ht, Pixels: out}, nil
}

func shiftGMOSWCS(h *fitsio.Header, _, _ int) {
	sec, err := parseGMOSSection(fitsio.HeaderString(*h, "DATASEC"))
	if err != nil {
		return
	}
	for _, key := range []string{"CRPIX1", "CRPIX2"} {
		if v, ok := fitsio.HeaderFloat(*h, key); ok {
			if key == "CRPIX1" {
				v -= float64(sec.x1 - 1)
			} else {
				v -= float64(sec.y1 - 1)
			}
			h.Cards[key] = strconv.FormatFloat(v, 'g', 15, 64)
		}
	}
}
