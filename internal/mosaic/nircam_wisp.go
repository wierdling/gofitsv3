package mosaic

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"gofitsv3/internal/debuglog"
	"gofitsv3/internal/fitsio"
)

const (
	nircamWispClipSigma = 4.0
	nircamWispMinPixels = 16
)

func applyNIRCamWispCorrection(p plannedInput, sci []float32, opts SkysubOptions) error {
	if !opts.NIRCamWisp || p.input.ReferenceOnly {
		return nil
	}
	if !isNircamFrame(p.input) {
		debuglog.Log(fmt.Sprintf("WISP input=%s skipped: non-NIRCam", InputKey(p.input)))
		return nil
	}
	path, reason, ok := nircamWispTemplatePath(p.input, opts)
	if !ok {
		debuglog.Log(fmt.Sprintf("WISP input=%s skipped: %s", InputKey(p.input), reason))
		return nil
	}
	template, err := loadNIRCamWispTemplate(path, p.input.HDU.Data.Width, p.input.HDU.Data.Height)
	if err != nil {
		return fmt.Errorf("NIRCam wisp template %s for %s: %w", path, InputKey(p.input), err)
	}

	scale := opts.NIRCamWispScale
	valid, rejected := 0, 0
	if opts.NIRCamWispAutoScale {
		var fit nircamWispFit
		fit, err = fitNIRCamWispScale(sci, template, p.input.HDU.Data.Width, p.input.HDU.Data.Height)
		if err != nil {
			debuglog.Log(fmt.Sprintf("WISP input=%s skipped: %v", InputKey(p.input), err))
			return nil
		}
		scale = fit.Scale
		valid = fit.Valid
		rejected = fit.Rejected
	} else if scale < 0 || !isFinite64(scale) {
		debuglog.Log(fmt.Sprintf("WISP input=%s skipped: fixed scale %.6g is not non-negative", InputKey(p.input), scale))
		return nil
	} else {
		valid = countFiniteTemplatePixels(sci, template)
	}
	if scale <= 0 {
		debuglog.Log(fmt.Sprintf("WISP input=%s skipped: scale %.6g", InputKey(p.input), scale))
		return nil
	}

	var sumSq, maxAbs float64
	corrected := 0
	for i := range sci {
		if !isFinite32(sci[i]) || !isFinite32(template[i]) {
			continue
		}
		corr := scale * float64(template[i])
		sci[i] -= float32(corr)
		sumSq += corr * corr
		if a := math.Abs(corr); a > maxAbs {
			maxAbs = a
		}
		corrected++
	}
	rms := 0.0
	if corrected > 0 {
		rms = math.Sqrt(sumSq / float64(corrected))
	}
	debuglog.Log(fmt.Sprintf("WISP input=%s template=%s scale=%.6g valid=%d rejected=%d corrected=%d rms=%.6g max=%.6g",
		InputKey(p.input), filepath.Base(path), scale, valid, rejected, corrected, rms, maxAbs))
	return nil
}

func nircamWispTemplatePath(in Input, opts SkysubOptions) (path, reason string, ok bool) {
	dir := strings.TrimSpace(opts.NIRCamWispTemplateDir)
	if dir == "" {
		return "", "template directory is blank", false
	}
	detector := strings.ToUpper(strings.TrimSpace(fitsio.HeaderString(in.PrimaryHeader, "DETECTOR")))
	if !nircamWispSupportedDetector(detector) {
		return "", "unsupported detector " + detector, false
	}
	filter := fitsio.FilterString(in.PrimaryHeader)
	if filter == "" {
		filter = fitsio.FilterString(in.HDU.Header)
	}
	filter = strings.ToUpper(strings.TrimSpace(filter))
	if filter == "" {
		return "", "filter not found", false
	}

	name := fmt.Sprintf("nircam_wisp_%s_%s.fits", strings.ToLower(detector), strings.ToLower(filter))
	candidate := filepath.Join(dir, name)
	if _, err := os.Stat(candidate); err == nil {
		return candidate, "", true
	} else if os.IsNotExist(err) {
		return "", "template not found: " + name, false
	}
	return candidate, "", true
}

func nircamWispSupportedDetector(detector string) bool {
	switch strings.ToUpper(strings.TrimSpace(detector)) {
	case "NRCA3", "NRCA4", "NRCB3", "NRCB4":
		return true
	default:
		return false
	}
}

func loadNIRCamWispTemplate(path string, wantW, wantH int) ([]float32, error) {
	file, err := fitsio.LoadFile(path)
	if err != nil {
		return nil, err
	}
	var hdu *fitsio.HDU
	if sci := file.SelectSCI(); len(sci) > 0 {
		hdu = &sci[0]
	} else if len(file.HDUs) > 0 && len(file.HDUs[0].Data.Pixels) > 0 {
		hdu = &file.HDUs[0]
	}
	if hdu == nil || len(hdu.Data.Pixels) == 0 {
		return nil, fmt.Errorf("no image data")
	}
	if hdu.Data.Width != wantW || hdu.Data.Height != wantH {
		return nil, fmt.Errorf("dimension mismatch: template %dx%d vs input %dx%d", hdu.Data.Width, hdu.Data.Height, wantW, wantH)
	}
	out := make([]float32, len(hdu.Data.Pixels))
	finite := 0
	for i, v := range hdu.Data.Pixels {
		if isFinite32(v) {
			finite++
		}
		out[i] = v
	}
	if finite < nircamWispMinPixels {
		return nil, fmt.Errorf("only %d finite template pixels", finite)
	}
	return out, nil
}

type nircamWispFit struct {
	Scale    float64
	Valid    int
	Rejected int
}

func fitNIRCamWispScale(sci, template []float32, width, height int) (nircamWispFit, error) {
	if len(sci) != len(template) {
		return nircamWispFit{}, fmt.Errorf("SCI/template length mismatch")
	}
	sourceMask := buildNIRCamWispSourceMask(sci, width, height)
	values := make([]nircamWispSample, 0, len(sci))
	sciVals := make([]float64, 0, len(sci))
	tplVals := make([]float64, 0, len(template))
	for i := range sci {
		if i < len(sourceMask) && sourceMask[i] {
			continue
		}
		if !isFinite32(sci[i]) || !isFinite32(template[i]) {
			continue
		}
		s := float64(sci[i])
		t := float64(template[i])
		values = append(values, nircamWispSample{Sci: s, Template: t, Keep: true})
		sciVals = append(sciVals, s)
		tplVals = append(tplVals, t)
	}
	if len(values) < nircamWispMinPixels {
		return nircamWispFit{}, fmt.Errorf("not enough finite fit pixels (%d)", len(values))
	}
	sciBase := medianCopy(sciVals)
	tplBase := medianCopy(tplVals)
	for i := range values {
		values[i].Sci -= sciBase
		values[i].Template -= tplBase
	}

	scale := math.NaN()
	for iter := 0; iter < 3; iter++ {
		var num, den float64
		valid := 0
		for _, v := range values {
			if !v.Keep {
				continue
			}
			num += v.Template * v.Sci
			den += v.Template * v.Template
			valid++
		}
		if valid < nircamWispMinPixels || den <= 0 || !isFinite64(den) {
			return nircamWispFit{}, fmt.Errorf("ill-conditioned scale fit")
		}
		scale = num / den
		if scale < 0 || !isFinite64(scale) {
			return nircamWispFit{}, fmt.Errorf("negative or invalid fitted scale %.6g", scale)
		}

		resid := make([]float64, 0, valid)
		for _, v := range values {
			if v.Keep {
				resid = append(resid, v.Sci-scale*v.Template)
			}
		}
		center := medianCopy(resid)
		for i := range resid {
			resid[i] = math.Abs(resid[i] - center)
		}
		sigma := 1.4826 * medianCopy(resid)
		if sigma <= 0 || !isFinite64(sigma) {
			break
		}
		threshold := nircamWispClipSigma * sigma
		changed := false
		for i := range values {
			if !values[i].Keep {
				continue
			}
			r := values[i].Sci - scale*values[i].Template
			if math.Abs(r-center) > threshold {
				values[i].Keep = false
				changed = true
			}
		}
		if !changed {
			break
		}
	}

	valid := 0
	for _, v := range values {
		if v.Keep {
			valid++
		}
	}
	if valid < nircamWispMinPixels {
		return nircamWispFit{}, fmt.Errorf("not enough clipped fit pixels (%d)", valid)
	}
	return nircamWispFit{Scale: scale, Valid: valid, Rejected: len(values) - valid}, nil
}

func buildNIRCamWispSourceMask(sci []float32, width, height int) []bool {
	mask := make([]bool, len(sci))
	if width <= 0 || height <= 0 || len(sci) < width*height {
		return mask
	}
	values := make([]float64, 0, len(sci))
	for _, v := range sci {
		if isFinite32(v) {
			values = append(values, float64(v))
		}
	}
	if len(values) == 0 {
		return mask
	}
	center := medianCopy(values)
	for i := range values {
		values[i] = math.Abs(values[i] - center)
	}
	sigma := 1.4826 * medianCopy(values)
	if sigma <= 0 || !isFinite64(sigma) {
		return mask
	}
	threshold := center + 5*sigma
	for y := 0; y < height; y++ {
		row := y * width
		for x := 0; x < width; x++ {
			idx := row + x
			if idx < len(sci) && isFinite32(sci[idx]) && float64(sci[idx]) > threshold {
				dilateMask(mask, width, height, x, y, 2)
			}
		}
	}
	return mask
}

func dilateMask(mask []bool, width, height, cx, cy, radius int) {
	y0, y1 := cy-radius, cy+radius
	if y0 < 0 {
		y0 = 0
	}
	if y1 >= height {
		y1 = height - 1
	}
	x0, x1 := cx-radius, cx+radius
	if x0 < 0 {
		x0 = 0
	}
	if x1 >= width {
		x1 = width - 1
	}
	for y := y0; y <= y1; y++ {
		row := y * width
		for x := x0; x <= x1; x++ {
			idx := row + x
			if idx < len(mask) {
				mask[idx] = true
			}
		}
	}
}

type nircamWispSample struct {
	Sci      float64
	Template float64
	Keep     bool
}

func countFiniteTemplatePixels(sci, template []float32) int {
	n := minInt(len(sci), len(template))
	count := 0
	for i := 0; i < n; i++ {
		if isFinite32(sci[i]) && isFinite32(template[i]) {
			count++
		}
	}
	return count
}

func medianCopy(values []float64) float64 {
	scratch := append([]float64(nil), values...)
	return quickSelectMedian(scratch)
}
