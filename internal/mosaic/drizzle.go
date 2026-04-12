package mosaic

import (
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/processing"
)

// edgeTrim is the number of pixels to exclude from each edge of every input
// image before drizzling, to avoid border artifacts.
const edgeTrim = 20

type Input struct {
	Path          string
	PrimaryHeader fitsio.Header
	HDU           fitsio.HDU
	OffsetX       float64
	OffsetY       float64
}

type Options struct {
	Scale            float64
	CleanCosmicRays  bool
	DropShrinkFactor float64
}

type InputStatus struct {
	Path     string
	Status   string
	Error    string
	Included bool
	Cleaned  bool
	OffsetX  float64
	OffsetY  float64
}

type Result struct {
	Pixels       []float32
	Weights      []float32
	Width        int
	Height       int
	OriginX      float64
	OriginY      float64
	Scale        float64
	OutputHeader fitsio.Header
	Inputs       []InputStatus
}

type StarAlignmentResult struct {
	OffsetX float64
	OffsetY float64
	Error   string
	Applied bool
}

type plannedInput struct {
	input       Input
	sourceToRef processing.AffineTransform
	statusIndex int
}

func Build(inputs []Input, options Options) (*Result, error) {
	if len(inputs) == 0 {
		return nil, fmt.Errorf("no FITS inputs selected")
	}
	if options.Scale <= 0 {
		options.Scale = 1
	}
	if options.DropShrinkFactor <= 0 || options.DropShrinkFactor > 1 {
		options.DropShrinkFactor = 1
	}

	planned, statuses, minX, minY, maxX, maxY, err := planInputs(inputs)
	if err != nil {
		return nil, err
	}

	width := int(math.Ceil((maxX - minX + 1) * options.Scale))
	height := int(math.Ceil((maxY - minY + 1) * options.Scale))
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}

	sums := make([]float32, width*height)
	weights := make([]float32, width*height)
	dropSize := options.Scale * options.DropShrinkFactor
	includedCount := 0

	var crMasks [][]bool
	if options.CleanCosmicRays && len(planned) > 1 {
		frameInfos := make([]processing.FrameInfo, len(planned))
		for i := range planned {
			refToSource, err := processing.InvertAffineTransform(planned[i].sourceToRef)
			if err != nil {
				refToSource = processing.IdentityTransform()
			}
			_, sigma := processing.EstimateBackground(planned[i].input.HDU.Data.Pixels)
			frameInfos[i] = processing.FrameInfo{
				Pixels:      planned[i].input.HDU.Data.Pixels,
				Width:       planned[i].input.HDU.Data.Width,
				Height:      planned[i].input.HDU.Data.Height,
				SourceToRef: planned[i].sourceToRef,
				RefToSource: refToSource,
				OffsetX:     planned[i].input.OffsetX,
				OffsetY:     planned[i].input.OffsetY,
				Sigma:       sigma,
			}
		}
		crMasks = processing.BuildCosmicRayMasks(frameInfos, 5.0, 2.0)
	}

	for i := range planned {
		pixels := planned[i].input.HDU.Data.Pixels
		var crMask []bool

		if options.CleanCosmicRays {
			if crMasks != nil {
				crMask = crMasks[i]
			} else {
				_, sigma := processing.EstimateBackground(pixels)
				pixels = processing.RemoveCosmicRays(pixels, planned[i].input.HDU.Data.Width, planned[i].input.HDU.Data.Height, sigma, 2, nil)
			}
			statuses[planned[i].statusIndex].Cleaned = true
			statuses[planned[i].statusIndex].Status = "cleaned and drizzled"
		} else if statuses[planned[i].statusIndex].Status == "reference" {
			statuses[planned[i].statusIndex].Status = "reference drizzled"
		} else {
			statuses[planned[i].statusIndex].Status = "aligned and drizzled"
		}
		includedCount++

		trimX := effectiveEdgeTrim(planned[i].input.HDU.Data.Width)
		trimY := effectiveEdgeTrim(planned[i].input.HDU.Data.Height)
		for y := trimY; y < planned[i].input.HDU.Data.Height-trimY; y++ {
			for x := trimX; x < planned[i].input.HDU.Data.Width-trimX; x++ {
				idx := y*planned[i].input.HDU.Data.Width + x
				if crMask != nil && crMask[idx] {
					continue
				}
				val := float64(pixels[idx])
				if math.IsNaN(val) || math.IsInf(val, 0) {
					continue
				}

				refX, refY := processing.ApplyAffineTransform(planned[i].sourceToRef, float64(x), float64(y))
				refX += planned[i].input.OffsetX
				refY += planned[i].input.OffsetY
				outX := (refX - minX) * options.Scale
				outY := (refY - minY) * options.Scale
				drizzlePixel(sums, weights, width, height, outX, outY, dropSize, float32(val))
			}
		}
	}

	out := make([]float32, len(sums))
	for i := range out {
		if weights[i] == 0 {
			out[i] = float32(math.NaN())
			continue
		}
		out[i] = sums[i] / weights[i]
	}

	return &Result{
		Pixels:       out,
		Weights:      weights,
		Width:        width,
		Height:       height,
		OriginX:      minX,
		OriginY:      minY,
		Scale:        options.Scale,
		OutputHeader: buildOutputHeader(inputs[0], width, height, minX, minY, options.Scale, includedCount),
		Inputs:       statuses,
	}, nil
}

func AlignInputsByStars(inputs []Input) ([]StarAlignmentResult, error) {
	if len(inputs) == 0 {
		return nil, fmt.Errorf("no FITS inputs selected")
	}
	results := make([]StarAlignmentResult, len(inputs))
	results[0] = StarAlignmentResult{OffsetX: inputs[0].OffsetX, OffsetY: inputs[0].OffsetY, Applied: true}

	aligned := make([]bool, len(inputs))
	aligned[0] = true
	queue := []int{0}

	for len(queue) > 0 {
		refIdx := queue[0]
		queue = queue[1:]

		for i := 0; i < len(inputs); i++ {
			if aligned[i] {
				continue
			}

			var initOx, initOy float64
			if refIdx == 0 {
				initOx = inputs[i].OffsetX
				initOy = inputs[i].OffsetY
			}

			dx, dy, err := processing.EstimateTranslationAfterWCS(
				inputs[i].HDU.Data.Pixels,
				inputs[i].HDU.Data.Width,
				inputs[i].HDU.Data.Height,
				inputs[i].HDU.Header,
				inputs[refIdx].HDU.Data.Pixels,
				inputs[refIdx].HDU.Data.Width,
				inputs[refIdx].HDU.Data.Height,
				inputs[refIdx].HDU.Header,
				initOx, initOy,
			)
			if err != nil {
				continue
			}

			if refIdx == 0 {
				results[i] = StarAlignmentResult{
					OffsetX: inputs[i].OffsetX + dx,
					OffsetY: inputs[i].OffsetY + dy,
					Applied: true,
				}
			} else {
				bToA, err := processing.ComputeWCSTransform(
					inputs[refIdx].HDU.Header, inputs[0].HDU.Header)
				if err != nil {
					continue
				}
				results[i] = StarAlignmentResult{
					OffsetX: bToA.A*dx + bToA.B*dy + results[refIdx].OffsetX,
					OffsetY: bToA.D*dx + bToA.E*dy + results[refIdx].OffsetY,
					Applied: true,
				}
			}

			aligned[i] = true
			queue = append(queue, i)
		}
	}

	for i := 1; i < len(inputs); i++ {
		if !aligned[i] {
			results[i] = StarAlignmentResult{
				OffsetX: inputs[i].OffsetX,
				OffsetY: inputs[i].OffsetY,
				Error:   "no overlapping aligned image found",
			}
		}
	}

	return results, nil
}

func AlignInputsBySelectedStars(inputs []Input, refStars []processing.Star) ([]StarAlignmentResult, error) {
	if len(inputs) == 0 {
		return nil, fmt.Errorf("no FITS inputs selected")
	}
	if len(refStars) == 0 {
		return nil, fmt.Errorf("no reference stars provided")
	}
	results := make([]StarAlignmentResult, len(inputs))
	results[0] = StarAlignmentResult{OffsetX: inputs[0].OffsetX, OffsetY: inputs[0].OffsetY, Applied: true}

	aligned := make([]bool, len(inputs))
	aligned[0] = true
	queue := []int{0}

	for len(queue) > 0 {
		refIdx := queue[0]
		queue = queue[1:]

		for i := 0; i < len(inputs); i++ {
			if aligned[i] {
				continue
			}

			var dx, dy float64
			var err error

			if refIdx == 0 {
				dx, dy, err = processing.EstimateTranslationFromRefStars(
					refStars,
					inputs[i].HDU.Data.Pixels,
					inputs[i].HDU.Data.Width,
					inputs[i].HDU.Data.Height,
					inputs[i].HDU.Header,
					inputs[0].HDU.Data.Width,
					inputs[0].HDU.Data.Height,
					inputs[0].HDU.Header,
					inputs[i].OffsetX,
					inputs[i].OffsetY,
				)
				if err == nil {
					results[i] = StarAlignmentResult{
						OffsetX: inputs[i].OffsetX + dx,
						OffsetY: inputs[i].OffsetY + dy,
						Applied: true,
					}
					aligned[i] = true
					queue = append(queue, i)
				}
			} else {
				dx, dy, err = alignViaIntermediate(inputs, refStars, i, refIdx, results)
				if err != nil {
					dx, dy, err = processing.EstimateTranslationAfterWCS(
						inputs[i].HDU.Data.Pixels,
						inputs[i].HDU.Data.Width,
						inputs[i].HDU.Data.Height,
						inputs[i].HDU.Header,
						inputs[refIdx].HDU.Data.Pixels,
						inputs[refIdx].HDU.Data.Width,
						inputs[refIdx].HDU.Data.Height,
						inputs[refIdx].HDU.Header,
						0, 0,
					)
				}
				if err == nil {
					bToA, bErr := processing.ComputeWCSTransform(
						inputs[refIdx].HDU.Header, inputs[0].HDU.Header)
					if bErr != nil {
						continue
					}
					results[i] = StarAlignmentResult{
						OffsetX: bToA.A*dx + bToA.B*dy + results[refIdx].OffsetX,
						OffsetY: bToA.D*dx + bToA.E*dy + results[refIdx].OffsetY,
						Applied: true,
					}
					aligned[i] = true
					queue = append(queue, i)
				}
			}
		}
	}

	for i := 1; i < len(inputs); i++ {
		if !aligned[i] {
			results[i] = StarAlignmentResult{
				OffsetX: inputs[i].OffsetX,
				OffsetY: inputs[i].OffsetY,
				Error:   "no overlapping aligned image found",
			}
		}
	}

	return results, nil
}

func alignViaIntermediate(
	inputs []Input,
	refStars []processing.Star,
	targetIdx, intermediateIdx int,
	results []StarAlignmentResult,
) (float64, float64, error) {
	aToB, err := processing.ComputeWCSTransform(
		inputs[0].HDU.Header, inputs[intermediateIdx].HDU.Header)
	if err != nil {
		return 0, 0, err
	}

	bStars := make([]processing.Star, 0, len(refStars))
	bW := float64(inputs[intermediateIdx].HDU.Data.Width)
	bH := float64(inputs[intermediateIdx].HDU.Data.Height)
	for _, rs := range refStars {
		adjX := rs.X - results[intermediateIdx].OffsetX
		adjY := rs.Y - results[intermediateIdx].OffsetY
		bx, by := processing.ApplyAffineTransform(aToB, adjX, adjY)
		if bx >= 0 && bx < bW && by >= 0 && by < bH {
			bStars = append(bStars, processing.Star{X: bx, Y: by, Flux: rs.Flux})
		}
	}
	if len(bStars) == 0 {
		return 0, 0, fmt.Errorf("no reference stars fall within intermediate image")
	}

	return processing.EstimateTranslationFromRefStars(
		bStars,
		inputs[targetIdx].HDU.Data.Pixels,
		inputs[targetIdx].HDU.Data.Width,
		inputs[targetIdx].HDU.Data.Height,
		inputs[targetIdx].HDU.Header,
		inputs[intermediateIdx].HDU.Data.Width,
		inputs[intermediateIdx].HDU.Data.Height,
		inputs[intermediateIdx].HDU.Header,
		0, 0,
	)
}

func SaveResultFITS(path string, result *Result) error {
	if result == nil {
		return fmt.Errorf("no drizzle result available")
	}
	return fitsio.WriteFloat32Image(path, result.OutputHeader, fitsio.ImageData{
		Width:  result.Width,
		Height: result.Height,
		Pixels: result.Pixels,
	})
}

func planInputs(inputs []Input) ([]plannedInput, []InputStatus, float64, float64, float64, float64, error) {
	ref := inputs[0]
	statuses := make([]InputStatus, len(inputs))
	for i, input := range inputs {
		statuses[i] = InputStatus{Path: input.Path, Status: "loaded", OffsetX: input.OffsetX, OffsetY: input.OffsetY}
	}

	if _, err := processing.ComputeWCSTransform(ref.HDU.Header, ref.HDU.Header); err != nil {
		statuses[0].Status = "failed"
		statuses[0].Error = err.Error()
		return nil, statuses, 0, 0, 0, 0, fmt.Errorf("reference image failed WCS validation: %w", err)
	}

	planned := make([]plannedInput, 0, len(inputs))
	var minX, minY, maxX, maxY float64
	boundsInitialized := false

	for idx, input := range inputs {
		transform := processing.IdentityTransform()
		if idx == 0 {
			statuses[idx].Status = "reference"
		} else {
			refToSource, err := processing.ComputeWCSTransform(input.HDU.Header, ref.HDU.Header)
			if err != nil {
				statuses[idx].Status = "failed"
				statuses[idx].Error = err.Error()
				continue
			}
			transform, err = processing.InvertAffineTransform(refToSource)
			if err != nil {
				statuses[idx].Status = "failed"
				statuses[idx].Error = err.Error()
				continue
			}
			statuses[idx].Status = "aligned"
		}

		statuses[idx].Included = true
		planned = append(planned, plannedInput{input: input, sourceToRef: transform, statusIndex: idx})

		trimX := effectiveEdgeTrim(input.HDU.Data.Width)
		trimY := effectiveEdgeTrim(input.HDU.Data.Height)
		corners := imageCorners(input.HDU.Data.Width-trimX*2, input.HDU.Data.Height-trimY*2)
		for ci := range corners {
			corners[ci][0] += float64(trimX)
			corners[ci][1] += float64(trimY)
		}
		for _, corner := range corners {
			x, y := processing.ApplyAffineTransform(transform, corner[0], corner[1])
			x += input.OffsetX
			y += input.OffsetY
			if !boundsInitialized {
				minX, maxX = x, x
				minY, maxY = y, y
				boundsInitialized = true
				continue
			}
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

	if len(planned) == 0 {
		return nil, statuses, 0, 0, 0, 0, fmt.Errorf("unable to align any selected FITS inputs")
	}

	return planned, statuses, minX, minY, maxX, maxY, nil
}

func buildOutputHeader(ref Input, width, height int, originX, originY, scale float64, includedCount int) fitsio.Header {
	merged := mergeHeaders(ref.PrimaryHeader, ref.HDU.Header)
	cards := fitsio.CloneHeader(merged).Cards

	for _, key := range []string{
		"END", "SIMPLE", "BITPIX", "NAXIS", "NAXIS1", "NAXIS2", "XTENSION", "PCOUNT", "GCOUNT", "EXTNAME", "EXTVER",
		"CHECKSUM", "DATASUM", "BSCALE", "BZERO", "LTV1", "LTV2", "LTM1_1", "LTM2_2",
	} {
		delete(cards, key)
	}
	for key := range cards {
		if strings.HasPrefix(key, "NAXIS") && key != "NAXIS1" && key != "NAXIS2" {
			delete(cards, key)
		}
	}

	cards["OBJECT"] = firstNonEmpty(cards["OBJECT"], quotedString(filepath.Base(ref.Path)))
	cards["IMAGETYP"] = quotedString("DRIZZLE")
	cards["NCOMBINE"] = strconv.Itoa(includedCount)
	cards["DRIZSCAL"] = formatFloat(scale)
	cards["ORIGOFFX"] = formatFloat(originX)
	cards["ORIGOFFY"] = formatFloat(originY)
	cards["MANUOFFX"] = formatFloat(ref.OffsetX)
	cards["MANUOFFY"] = formatFloat(ref.OffsetY)
	cards["EXTEND"] = "T"

	if crpix1, ok := fitsio.HeaderFloat(ref.HDU.Header, "CRPIX1"); ok {
		cards["CRPIX1"] = formatFloat((crpix1-1-originX)*scale + 1)
	}
	if crpix2, ok := fitsio.HeaderFloat(ref.HDU.Header, "CRPIX2"); ok {
		cards["CRPIX2"] = formatFloat((crpix2-1-originY)*scale + 1)
	}

	if cd11, ok11 := fitsio.HeaderFloat(ref.HDU.Header, "CD1_1"); ok11 {
		if cd12, ok12 := fitsio.HeaderFloat(ref.HDU.Header, "CD1_2"); ok12 {
			if cd21, ok21 := fitsio.HeaderFloat(ref.HDU.Header, "CD2_1"); ok21 {
				if cd22, ok22 := fitsio.HeaderFloat(ref.HDU.Header, "CD2_2"); ok22 {
					cards["CD1_1"] = formatFloat(cd11 / scale)
					cards["CD1_2"] = formatFloat(cd12 / scale)
					cards["CD2_1"] = formatFloat(cd21 / scale)
					cards["CD2_2"] = formatFloat(cd22 / scale)
				}
			}
		}
	} else {
		if cdelt1, ok := fitsio.HeaderFloat(ref.HDU.Header, "CDELT1"); ok {
			cards["CDELT1"] = formatFloat(cdelt1 / scale)
		}
		if cdelt2, ok := fitsio.HeaderFloat(ref.HDU.Header, "CDELT2"); ok {
			cards["CDELT2"] = formatFloat(cdelt2 / scale)
		}
	}

	return fitsio.Header{Cards: cards}
}

func mergeHeaders(headers ...fitsio.Header) fitsio.Header {
	merged := fitsio.Header{Cards: map[string]string{}}
	for _, header := range headers {
		for key, value := range header.Cards {
			merged.Cards[key] = value
		}
	}
	return merged
}

func imageCorners(width, height int) [][2]float64 {
	maxX := float64(width - 1)
	maxY := float64(height - 1)
	return [][2]float64{{0, 0}, {maxX, 0}, {0, maxY}, {maxX, maxY}}
}

func effectiveEdgeTrim(size int) int {
	if size <= edgeTrim*2 {
		return 0
	}
	return edgeTrim
}

func drizzlePixel(sums, weights []float32, width, height int, cx, cy, dropSize float64, value float32) {
	if width <= 0 || height <= 0 {
		return
	}
	if dropSize <= 0 {
		dropSize = 1
	}

	half := dropSize / 2.0
	left := cx - half
	right := cx + half
	top := cy - half
	bottom := cy + half

	ix0 := int(math.Floor(left - 0.5))
	ix1 := int(math.Ceil(right + 0.5))
	iy0 := int(math.Floor(top - 0.5))
	iy1 := int(math.Ceil(bottom + 0.5))

	for y := iy0; y <= iy1; y++ {
		if y < 0 || y >= height {
			continue
		}
		cellTop := float64(y) - 0.5
		cellBottom := float64(y) + 0.5
		overlapY := math.Min(bottom, cellBottom) - math.Max(top, cellTop)
		if overlapY <= 0 {
			continue
		}
		for x := ix0; x <= ix1; x++ {
			if x < 0 || x >= width {
				continue
			}
			cellLeft := float64(x) - 0.5
			cellRight := float64(x) + 0.5
			overlapX := math.Min(right, cellRight) - math.Max(left, cellLeft)
			if overlapX <= 0 {
				continue
			}
			weight := float32(overlapX * overlapY)
			idx := y*width + x
			sums[idx] += value * weight
			weights[idx] += weight
		}
	}
}

func FormatStatusLines(inputs []InputStatus) []string {
	if len(inputs) == 0 {
		return []string{"No FITS files loaded."}
	}

	lines := make([]string, 0, len(inputs))
	for idx, input := range inputs {
		name := filepath.Base(input.Path)
		if name == "" {
			name = fmt.Sprintf("Input %d", idx+1)
		}
		line := fmt.Sprintf("%d. %s - %s", idx+1, name, input.Status)
		line += fmt.Sprintf(" | dX=%+.2f dY=%+.2f", input.OffsetX, input.OffsetY)
		if input.Cleaned {
			line += " (cleaned)"
		}
		if input.Error != "" {
			line += ": " + input.Error
		}
		lines = append(lines, line)
	}
	return lines
}

func LooksLikeFLC(path string) bool {
	return strings.Contains(strings.ToLower(filepath.Base(path)), "_flc")
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'g', 12, 64)
}

func quotedString(v string) string {
	return "'" + v + "'"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
