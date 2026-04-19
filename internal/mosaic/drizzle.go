package mosaic

import (
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/processing"
)

// edgeTrim is the number of pixels to exclude from each edge of every input
// image before drizzling, to avoid border artifacts.
const edgeTrim = 20

type Input struct {
	Path               string
	SCIExt             int
	PrimaryHeader      fitsio.Header
	HDU                fitsio.HDU
	OffsetX            float64
	OffsetY            float64
	ManualTransform    processing.AffineTransform
	HasManualTransform bool
	OffsetLocked       bool
	// D2IX and D2IY hold the detector-to-image correction lookup tables parsed
	// from the D2IMARR FITS extensions of HST calibrated files. Either or both
	// may be nil if the file has no such corrections.
	D2IX *processing.D2ITable
	D2IY *processing.D2ITable
	// ChipFootprints holds the 4 trimmed corners (TL, TR, BL, BR) of each
	// individual SCI chip in combined-canvas pixel coordinates. Set only when
	// multiple SCI extensions were merged. Empty means use the full image bounds.
	ChipFootprints [][4][2]float64
	// ReferenceOnly marks this input as a WCS anchor only. It participates in
	// alignment and coordinate-system setup but its pixels are not drizzled into
	// the output. Use this to align a new filter to a previously drizzled baseline.
	ReferenceOnly bool
}

func InputKey(input Input) string {
	if input.SCIExt > 0 {
		return fmt.Sprintf("%s[sci,%d]", input.Path, input.SCIExt)
	}
	return input.Path
}

func InputLabel(input Input) string {
	name := filepath.Base(input.Path)
	if name == "" {
		name = input.Path
	}
	if input.SCIExt > 0 {
		return fmt.Sprintf("%s[sci,%d]", name, input.SCIExt)
	}
	return name
}

// CRMethod selects the cosmic-ray removal algorithm used during drizzle.
type CRMethod int

const (
	// CRMethodNone disables cosmic-ray removal.
	CRMethodNone CRMethod = iota
	// CRMethodLegacy uses the single-frame Laplacian detector (original method).
	CRMethodLegacy
	// CRMethodDrizzle uses the AstroDrizzle-style multi-frame model/blot/flag
	// pipeline.  Requires ≥ 2 aligned exposures; falls back to CRMethodLegacy
	// when only one frame is available.
	CRMethodDrizzle
)

// DrizzleKernel selects how each input pixel's flux is distributed onto the
// output grid during the drizzle step.
type DrizzleKernel int

const (
	// KernelSquare is the classic drizzle kernel: flux is spread by area
	// overlap between the shrunken input pixel box and each output pixel cell.
	KernelSquare DrizzleKernel = iota
	// KernelPoint deposits flux only into the single nearest output pixel.
	KernelPoint
	// KernelTurbo is a faster axis-aligned approximation of the square kernel;
	// it skips fractional-edge overlap and writes to all fully-covered pixels
	// with equal weight.
	KernelTurbo
	// KernelGaussian spreads flux with a Gaussian footprint whose sigma is
	// proportional to pixfrac.
	KernelGaussian
	// KernelTophat spreads flux uniformly within a circular aperture of
	// diameter pixfrac in output pixels.
	KernelTophat
	// KernelLanczos2 uses a 2-lobe Lanczos (damped-sinc) resampling kernel.
	// Only meaningful when pixfrac == 1 and scale == 1.
	KernelLanczos2
	// KernelLanczos3 uses a 3-lobe Lanczos resampling kernel.
	// Only meaningful when pixfrac == 1 and scale == 1.
	KernelLanczos3
)

type Options struct {
	// Scale is the internal output/input pixel size ratio used directly when
	// FinalScale is zero.  A value of 2.0 doubles the output dimensions.
	Scale float64
	// FinalScale is the desired output plate scale in arcseconds per pixel,
	// matching AstroDrizzle's final_scale parameter.  When > 0, Build reads
	// the native plate scale from the reference image WCS and computes:
	//   Scale = nativePlateScale / FinalScale
	// Smaller FinalScale → finer sampling → larger output (e.g. 0.02 arcsec/px
	// on a 0.04 arcsec/px camera yields Scale = 2, doubling each dimension).
	// If WCS plate scale cannot be determined, FinalScale is treated as Scale.
	FinalScale float64
	// CleanCosmicRays is kept for backwards compatibility; it selects CRMethodLegacy
	// when CRMethod is CRMethodNone.  Prefer setting CRMethod directly.
	CleanCosmicRays bool
	CRMethod        CRMethod
	PixFrac         float64
	// SepKernel is the kernel used during the per-frame drizzle step.
	// Defaults to KernelSquare when zero.
	SepKernel DrizzleKernel
	// FinalKernel is reserved for a future two-pass pipeline's final combination
	// step. Currently unused; SepKernel governs all drizzling.
	FinalKernel DrizzleKernel
}

type InputStatus struct {
	Path         string
	Status       string
	Error        string
	Included     bool
	Cleaned      bool
	OffsetX      float64
	OffsetY      float64
	HasAffine    bool
	AffineRotDeg float64
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
	// InputFootprints holds the 4 output-space corners (TL, TR, BL, BR) for
	// each successfully drizzled input, in [x, y] order.
	InputFootprints [][4][2]float64
	// InputFootprintPaths holds the source file path for each entry in
	// InputFootprints (multi-chip files produce multiple footprints with the same path).
	InputFootprintPaths []string
}

type StarAlignmentResult struct {
	OffsetX            float64
	OffsetY            float64
	ManualTransform    processing.AffineTransform
	HasManualTransform bool
	Error              string
	Applied            bool
}

type plannedInput struct {
	input       Input
	sourceToRef processing.AffineTransform // affine approximation, used for CR detection
	mapper      *processing.WCSMapper      // per-pixel WCS projection, used for drizzle
	statusIndex int
}

// mapPixel projects a 0-indexed source pixel through the full WCS pipeline
// then applies any manual offset/affine correction in reference-pixel space.
func (p *plannedInput) mapPixel(x, y float64) (float64, float64) {
	if p.mapper != nil {
		rx, ry := p.mapper.MapPixel(x, y)
		rx += p.input.OffsetX
		ry += p.input.OffsetY
		if p.input.HasManualTransform {
			rx, ry = processing.ApplyAffineTransform(p.input.ManualTransform, rx, ry)
		}
		return rx, ry
	}
	return processing.ApplyAffineTransform(p.sourceToRef, x, y)
}

// nativePlateScaleArcsec returns the native plate scale of the image described
// by header, in arcseconds per pixel.  It tries the CD matrix first, then
// CDELT1.  The second return value is false when WCS data is absent.
func nativePlateScaleArcsec(header fitsio.Header) (float64, bool) {
	cd11, ok11 := fitsio.HeaderFloat(header, "CD1_1")
	cd12, ok12 := fitsio.HeaderFloat(header, "CD1_2")
	cd21, ok21 := fitsio.HeaderFloat(header, "CD2_1")
	cd22, ok22 := fitsio.HeaderFloat(header, "CD2_2")
	if ok11 && ok12 && ok21 && ok22 {
		det := math.Abs(cd11*cd22 - cd12*cd21)
		if det > 0 {
			return math.Sqrt(det) * 3600, true
		}
	}
	if cdelt1, ok := fitsio.HeaderFloat(header, "CDELT1"); ok && cdelt1 != 0 {
		return math.Abs(cdelt1) * 3600, true
	}
	return 0, false
}

func Build(inputs []Input, options Options) (*Result, error) {
	if len(inputs) == 0 {
		return nil, fmt.Errorf("no FITS inputs selected")
	}

	// Resolve FinalScale (arcsec/pixel) → internal Scale multiplier.
	if options.FinalScale > 0 {
		ref := firstDataInput(inputs)
		if ps, ok := nativePlateScaleArcsec(ref.HDU.Header); ok && ps > 0 {
			options.Scale = ps / options.FinalScale
		} else {
			// No WCS: fall back to treating FinalScale as a raw multiplier.
			options.Scale = options.FinalScale
		}
	}

	if options.Scale <= 0 {
		options.Scale = 1
	}
	if options.PixFrac <= 0 || options.PixFrac > 1 {
		options.PixFrac = 1
	}

	planned, statuses, minX, minY, maxX, maxY, err := planInputs(inputs, options.Scale)
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

	dropSize := options.Scale * options.PixFrac
	includedCount := 0

	// Resolve effective CR method: honour CRMethod if set, otherwise fall back
	// to CleanCosmicRays for backwards compatibility.
	effectiveCR := options.CRMethod
	if effectiveCR == CRMethodNone && options.CleanCosmicRays {
		effectiveCR = CRMethodLegacy
	}

	// Build the list of data-only planned inputs (excluding reference-only
	// frames) that will actually contribute pixels. CR detection only makes
	// sense when at least two such frames overlap; comparing against a
	// reference-only baseline (different filter) would produce false positives.
	dataPlanned := make([]int, 0, len(planned))
	for i := range planned {
		if !planned[i].input.ReferenceOnly {
			dataPlanned = append(dataPlanned, i)
		}
	}

	// crMaskIndex[plannedIdx] = index into crMasks; -1 if not included.
	crMaskIndex := make([]int, len(planned))
	for i := range crMaskIndex {
		crMaskIndex[i] = -1
	}
	for slot, pi := range dataPlanned {
		crMaskIndex[pi] = slot
	}

	// Build per-frame FrameInfo for any multi-frame CR method.
	var frameInfos []processing.FrameInfo
	if effectiveCR != CRMethodNone && len(dataPlanned) > 1 {
		frameInfos = make([]processing.FrameInfo, len(dataPlanned))
		for slot, pi := range dataPlanned {
			refToSource, err := processing.InvertAffineTransform(planned[pi].sourceToRef)
			if err != nil {
				refToSource = processing.IdentityTransform()
			}
			_, sigma := processing.EstimateBackground(planned[pi].input.HDU.Data.Pixels)
			frameInfos[slot] = processing.FrameInfo{
				Pixels:      planned[pi].input.HDU.Data.Pixels,
				Width:       planned[pi].input.HDU.Data.Width,
				Height:      planned[pi].input.HDU.Data.Height,
				SourceToRef: planned[pi].sourceToRef,
				RefToSource: refToSource,
				OffsetX:     0,
				OffsetY:     0,
				Sigma:       sigma,
			}
		}
	}

	var crMasks [][]bool
	switch {
	case effectiveCR == CRMethodDrizzle && len(dataPlanned) > 1:
		// Separate pass: drizzle each frame individually with SepKernel to build
		// per-frame images, then median-combine into a clean model. This gives the
		// model better fidelity than inverse-blot when the sep kernel is non-trivial.
		sepImages := make([][]float32, len(dataPlanned))
		for slot, pi := range dataPlanned {
			sepImages[slot] = drizzleSepFrame(planned[pi], width, height, minX, minY, options.Scale, dropSize, options.SepKernel)
		}
		model := buildMedianModel(sepImages, width, height, len(dataPlanned))
		sepImages = nil // allow GC before final drizzle pass
		crMasks = processing.BuildCRMasksFromModel(
			frameInfos, model, width, height, minX, minY, options.Scale,
			processing.DrizzleStyleCROptions{SeedSNR: 4.0, DerivScale: 1.2},
		)
	case effectiveCR == CRMethodLegacy && len(dataPlanned) > 1:
		crMasks = processing.BuildCosmicRayMasks(frameInfos, 5.0, 2.0)
	}

	// Final drizzle pass: accumulate all frames into the output using FinalKernel.
	sums := make([]float32, width*height)
	weights := make([]float32, width*height)
	finalKernel := options.FinalKernel

	for i := range planned {
		if planned[i].input.ReferenceOnly {
			continue
		}
		pixels := planned[i].input.HDU.Data.Pixels
		var crMask []bool

		if effectiveCR != CRMethodNone {
			slot := crMaskIndex[i]
			if slot >= 0 && crMasks != nil {
				crMask = crMasks[slot]
			} else if len(dataPlanned) == 1 {
				// Only one data frame — no inter-frame comparison possible.
				// Do not apply any CR removal (a lone frame has no reference to
				// distinguish a real bright pixel from a cosmic ray).
			} else {
				// Single-frame fallback: always use legacy Laplacian detector.
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

		// trimX := effectiveEdgeTrim(planned[i].input.HDU.Data.Width, options.Scale)
		// trimY := effectiveEdgeTrim(planned[i].input.HDU.Data.Height, options.Scale)
		trimX, trimY := 0, 0
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

				refX, refY := planned[i].mapPixel(float64(x), float64(y))
				outX := (refX - minX) * options.Scale
				outY := (refY - minY) * options.Scale
				drizzlePixelKernel(sums, weights, width, height, outX, outY, dropSize, float32(val), finalKernel)
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

	// Compute output-space footprints for preview border drawing.
	// If the input had multiple SCI chips merged, expand one footprint per chip;
	// otherwise use the trimmed bounds of the whole combined image.
	var footprints [][4][2]float64
	var footprintPaths []string
	for _, p := range planned {
		if p.input.ReferenceOnly {
			continue
		}
		chipCorners := p.input.ChipFootprints
		if len(chipCorners) == 0 {
			// trimX := float64(effectiveEdgeTrim(p.input.HDU.Data.Width, options.Scale))
			// trimY := float64(effectiveEdgeTrim(p.input.HDU.Data.Height, options.Scale))
			trimX, trimY := 0.0, 0.0
			w := float64(p.input.HDU.Data.Width)
			h := float64(p.input.HDU.Data.Height)
			chipCorners = [][4][2]float64{{
				{trimX, trimY},
				{w - trimX - 1, trimY},
				{trimX, h - trimY - 1},
				{w - trimX - 1, h - trimY - 1},
			}}
		}
		for _, chip := range chipCorners {
			var fp [4][2]float64
			for ci, sc := range chip {
				rx, ry := p.mapPixel(sc[0], sc[1])
				fp[ci] = [2]float64{
					(rx - minX) * options.Scale,
					(ry - minY) * options.Scale,
				}
			}
			footprints = append(footprints, fp)
			footprintPaths = append(footprintPaths, InputKey(p.input))
		}
	}

	return &Result{
		Pixels:              out,
		Weights:             weights,
		Width:               width,
		Height:              height,
		OriginX:             minX,
		OriginY:             minY,
		Scale:               options.Scale,
		OutputHeader:        buildOutputHeader(firstDataInput(inputs), width, height, minX, minY, options.Scale, includedCount),
		Inputs:              statuses,
		InputFootprints:     footprints,
		InputFootprintPaths: footprintPaths,
	}, nil
}

// DrizzleOrder returns the indices 1..len(inputs)-1 sorted by ascending WCS
// distance from inputs[0]. Index 0 (the reference) is never included in the
// returned slice. Use this to determine processing order.
func DrizzleOrder(inputs []Input) []int {
	return sortedByDistFromRef(inputs)
}

// SortInputsByWCSDistance reorders inputs[1:] (and the parallel statuses slice,
// if provided and the same length) in-place by ascending WCS distance from
// inputs[0]. inputs[0] is never moved. If statuses is nil or a different length
// it is ignored.
func SortInputsByWCSDistance(inputs []Input, statuses []InputStatus) {
	if len(inputs) < 2 {
		return
	}
	order := sortedByDistFromRef(inputs)
	sortedIn := make([]Input, len(inputs))
	sortedIn[0] = inputs[0]
	for rank, srcIdx := range order {
		sortedIn[rank+1] = inputs[srcIdx]
	}
	copy(inputs, sortedIn)

	if len(statuses) == len(inputs) {
		sortedSt := make([]InputStatus, len(statuses))
		sortedSt[0] = statuses[0]
		for rank, srcIdx := range order {
			sortedSt[rank+1] = statuses[srcIdx]
		}
		copy(statuses, sortedSt)
	}
}

// sortedByDistFromRef returns indices 1..len(inputs)-1 sorted by ascending
// distance of each input's image center from the reference image center (index 0),
// computed via WCS projection.  Inputs whose WCS cannot be parsed sort last.
func sortedByDistFromRef(inputs []Input) []int {
	ref := inputs[0]
	type entry struct {
		idx  int
		dist float64
	}
	entries := make([]entry, len(inputs)-1)
	for i := 1; i < len(inputs); i++ {
		d, err := processing.CenterDistInRefPixels(
			inputs[i].HDU.Header,
			inputs[i].HDU.Data.Width,
			inputs[i].HDU.Data.Height,
			ref.HDU.Header,
			ref.HDU.Data.Width,
			ref.HDU.Data.Height,
		)
		if err != nil {
			d = math.MaxFloat64
		}
		entries[i-1] = entry{idx: i, dist: d}
	}
	sort.Slice(entries, func(a, b int) bool { return entries[a].dist < entries[b].dist })
	out := make([]int, len(entries))
	for i, e := range entries {
		out[i] = e.idx
	}
	return out
}

func AlignInputsByStars(inputs []Input) ([]StarAlignmentResult, error) {
	if len(inputs) == 0 {
		return nil, fmt.Errorf("no FITS inputs selected")
	}
	results := make([]StarAlignmentResult, len(inputs))
	results[0] = StarAlignmentResult{
		OffsetX:            inputs[0].OffsetX,
		OffsetY:            inputs[0].OffsetY,
		ManualTransform:    inputs[0].ManualTransform,
		HasManualTransform: inputs[0].HasManualTransform,
		Applied:            true,
	}

	aligned := make([]bool, len(inputs))
	aligned[0] = true
	queue := []int{0}
	// Track the last error per image so users see the real failure reason.
	lastErr := make([]string, len(inputs))
	// Process closest-to-reference images first for better chain alignment.
	ordered := sortedByDistFromRef(inputs)

	for len(queue) > 0 {
		refIdx := queue[0]
		queue = queue[1:]

		for _, i := range ordered {
			if aligned[i] {
				continue
			}

			var initOx, initOy float64
			if refIdx == 0 {
				initOx = inputs[i].OffsetX
				initOy = inputs[i].OffsetY
			}

			if refIdx == 0 {
				affine, err := processing.EstimateAffineAfterWCS(
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
					lastErr[i] = err.Error()
					continue
				}
				results[i] = StarAlignmentResult{
					OffsetX:            inputs[i].OffsetX,
					OffsetY:            inputs[i].OffsetY,
					ManualTransform:    affine,
					HasManualTransform: true,
					Applied:            true,
				}
			} else {
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
					lastErr[i] = err.Error()
					continue
				}
				bToA, err := processing.ComputeWCSTransform(
					inputs[refIdx].HDU.Header, inputs[0].HDU.Header)
				if err != nil {
					lastErr[i] = err.Error()
					continue
				}
				results[i] = StarAlignmentResult{
					OffsetX:            bToA.A*dx + bToA.B*dy + results[refIdx].OffsetX,
					OffsetY:            bToA.D*dx + bToA.E*dy + results[refIdx].OffsetY,
					ManualTransform:    processing.IdentityTransform(),
					HasManualTransform: false,
					Applied:            true,
				}
			}

			aligned[i] = true
			queue = append(queue, i)
		}
	}

	for i := 1; i < len(inputs); i++ {
		if !aligned[i] {
			errMsg := "no overlapping aligned image found"
			if lastErr[i] != "" {
				errMsg = lastErr[i]
			}
			results[i] = StarAlignmentResult{
				OffsetX:            inputs[i].OffsetX,
				OffsetY:            inputs[i].OffsetY,
				ManualTransform:    inputs[i].ManualTransform,
				HasManualTransform: inputs[i].HasManualTransform,
				Error:              errMsg,
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
	results[0] = StarAlignmentResult{
		OffsetX:            inputs[0].OffsetX,
		OffsetY:            inputs[0].OffsetY,
		ManualTransform:    inputs[0].ManualTransform,
		HasManualTransform: inputs[0].HasManualTransform,
		Applied:            true,
	}

	// Align closest images first so results are more stable across runs.
	for _, i := range sortedByDistFromRef(inputs) {
		refinement, err := processing.EstimateAffineFromRefStars(
			refStars,
			inputs[i].HDU.Data.Pixels,
			inputs[i].HDU.Data.Width,
			inputs[i].HDU.Data.Height,
			inputs[i].HDU.Header,
			inputs[0].HDU.Header,
			inputs[i].OffsetX,
			inputs[i].OffsetY,
			manualTransformPtr(inputs[i]),
		)
		if err != nil {
			results[i] = StarAlignmentResult{
				OffsetX:            inputs[i].OffsetX,
				OffsetY:            inputs[i].OffsetY,
				ManualTransform:    inputs[i].ManualTransform,
				HasManualTransform: inputs[i].HasManualTransform,
				Error:              err.Error(),
				Applied:            false,
			}
			continue
		}

		storedT := refinement
		if inputs[i].HasManualTransform {
			storedT = processing.ComposeAffineTransforms(refinement, inputs[i].ManualTransform)
		}

		results[i] = StarAlignmentResult{
			OffsetX:            inputs[i].OffsetX,
			OffsetY:            inputs[i].OffsetY,
			ManualTransform:    storedT,
			HasManualTransform: true,
			Applied:            true,
		}
	}

	return results, nil
}

func manualTransformPtr(input Input) *processing.AffineTransform {
	if !input.HasManualTransform {
		return nil
	}
	return &input.ManualTransform
}

func composePlacementTransform(base processing.AffineTransform, input Input) processing.AffineTransform {
	placed := processing.ComposeAffineTransforms(
		processing.AffineTransform{A: 1, E: 1, C: input.OffsetX, F: input.OffsetY},
		base,
	)
	if input.HasManualTransform {
		placed = processing.ComposeAffineTransforms(input.ManualTransform, placed)
	}
	return placed
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

func planInputs(inputs []Input, scale float64) ([]plannedInput, []InputStatus, float64, float64, float64, float64, error) {
	ref := inputs[0]
	statuses := make([]InputStatus, len(inputs))
	for i, input := range inputs {
		statuses[i] = InputStatus{Path: InputKey(input), Status: "loaded", OffsetX: input.OffsetX, OffsetY: input.OffsetY,
			HasAffine: input.HasManualTransform, AffineRotDeg: affineRotationDeg(input.ManualTransform)}
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
		var mapper *processing.WCSMapper
		if idx == 0 {
			statuses[idx].Status = "reference"
			// mapper stays nil; reference pixels map through the identity
			// affine (sourceToRef) so no WCS round-trip is needed.
		} else {
			// Build the per-pixel WCS mapper for all non-reference inputs,
			// regardless of whether they share a file with the reference.
			// This replaces the old chipPlacementTransform hack which stripped
			// inter-chip rotation and produced a rotational offset in SCI[2].
			var mapperErr error
			mapper, mapperErr = processing.NewWCSMapper(
				input.HDU.Header, input.D2IX, input.D2IY,
				ref.HDU.Header, ref.D2IX, ref.D2IY,
			)
			if mapperErr != nil {
				statuses[idx].Status = "failed"
				statuses[idx].Error = mapperErr.Error()
				continue
			}

			// Also compute a linear affine approximation for CR detection.
			refToSource, wcsErr := processing.ComputeWCSTransform(input.HDU.Header, ref.HDU.Header)
			if wcsErr != nil {
				statuses[idx].Status = "failed"
				statuses[idx].Error = wcsErr.Error()
				continue
			}
			var invertErr error
			transform, invertErr = processing.InvertAffineTransform(refToSource)
			if invertErr != nil {
				statuses[idx].Status = "failed"
				statuses[idx].Error = invertErr.Error()
				continue
			}
			statuses[idx].Status = "aligned"
		}

		transform = composePlacementTransform(transform, input)

		statuses[idx].Included = true
		p := plannedInput{input: input, sourceToRef: transform, mapper: mapper, statusIndex: idx}
		planned = append(planned, p)

		// Reference-only inputs anchor the coordinate system but don't contribute
		// pixels, so we skip them when computing the output canvas bounds.
		if input.ReferenceOnly {
			continue
		}

		// Use the linear affine approximation for canvas bounds: it is exact for
		// linear WCS and a good-enough envelope for distorted chips. Per-pixel
		// accuracy comes from the mapper in the drizzle loop below.
		for _, corner := range imageCorners(input.HDU.Data.Width, input.HDU.Data.Height) {
			x, y := processing.ApplyAffineTransform(transform, corner[0], corner[1])
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

// effectiveEdgeTrim returns the number of input pixels to exclude from each
// edge so that the trim covers edgeTrim output pixels regardless of scale.
// It is capped at 10 % of the image dimension to avoid over-trimming at very
// small scale values.
func effectiveEdgeTrim(size int, scale float64) int {
	if scale <= 0 {
		scale = 1
	}
	t := int(math.Ceil(float64(edgeTrim) / scale))
	if max := size / 10; t > max {
		t = max
	}
	if size <= t*2 {
		return 0
	}
	return t
}

// drizzleSepFrame drizzles a single planned input into its own output-size
// accumulator and returns the normalized image. Used to build per-frame images
// for the AstroDrizzle-style separate pass.
func drizzleSepFrame(p plannedInput, outW, outH int, minX, minY, scale, dropSize float64, kernel DrizzleKernel) []float32 {
	// Use out as the flux accumulator directly; weights tracks coverage.
	// This avoids allocating a separate sums array.
	out := make([]float32, outW*outH)
	weights := make([]float32, outW*outH)
	// trimX := effectiveEdgeTrim(p.input.HDU.Data.Width, scale)
	// trimY := effectiveEdgeTrim(p.input.HDU.Data.Height, scale)
	trimX, trimY := 0, 0
	pixels := p.input.HDU.Data.Pixels
	for y := trimY; y < p.input.HDU.Data.Height-trimY; y++ {
		for x := trimX; x < p.input.HDU.Data.Width-trimX; x++ {
			idx := y*p.input.HDU.Data.Width + x
			val := float64(pixels[idx])
			if math.IsNaN(val) || math.IsInf(val, 0) {
				continue
			}
			refX, refY := p.mapPixel(float64(x), float64(y))
			outX := (refX - minX) * scale
			outY := (refY - minY) * scale
			drizzlePixelKernel(out, weights, outW, outH, outX, outY, dropSize, float32(val), kernel)
		}
	}
	// Normalize in-place: weight-divide where covered, NaN elsewhere.
	for i := range out {
		if weights[i] == 0 {
			out[i] = float32(math.NaN())
		} else {
			out[i] /= weights[i]
		}
	}
	return out
}

// buildMedianModel combines n per-frame drizzled images into a single clean
// model using minmed (n ≤ 3) or median (n > 3) at each pixel.
func buildMedianModel(images [][]float32, outW, outH, n int) []float32 {
	model := make([]float32, outW*outH)
	vals := make([]float64, 0, n)
	for i := range model {
		vals = vals[:0]
		for _, img := range images {
			v := float64(img[i])
			if !math.IsNaN(v) {
				vals = append(vals, v)
			}
		}
		if len(vals) == 0 {
			model[i] = float32(math.NaN())
			continue
		}
		// sort in-place using a simple insertion sort (n is small)
		for j := 1; j < len(vals); j++ {
			for k := j; k > 0 && vals[k] < vals[k-1]; k-- {
				vals[k], vals[k-1] = vals[k-1], vals[k]
			}
		}
		median := vals[(len(vals)-1)/2]
		if n <= 3 {
			var sum float64
			for _, v := range vals {
				sum += v
			}
			mean := sum / float64(len(vals))
			if mean < median {
				model[i] = float32(mean)
			} else {
				model[i] = float32(median)
			}
		} else {
			model[i] = float32(median)
		}
	}
	return model
}

func drizzlePixelKernel(sums, weights []float32, width, height int, cx, cy, dropSize float64, value float32, kernel DrizzleKernel) {
	switch kernel {
	case KernelPoint:
		drizzlePixelPoint(sums, weights, width, height, cx, cy, value)
	case KernelTurbo:
		drizzlePixelTurbo(sums, weights, width, height, cx, cy, dropSize, value)
	case KernelGaussian:
		drizzlePixelGaussian(sums, weights, width, height, cx, cy, dropSize, value)
	case KernelTophat:
		drizzlePixelTophat(sums, weights, width, height, cx, cy, dropSize, value)
	case KernelLanczos2:
		drizzlePixelLanczos(sums, weights, width, height, cx, cy, value, 2)
	case KernelLanczos3:
		drizzlePixelLanczos(sums, weights, width, height, cx, cy, value, 3)
	default: // KernelSquare
		drizzlePixelSquare(sums, weights, width, height, cx, cy, dropSize, value)
	}
}

// drizzlePixelSquare is the classic drizzle box-overlap kernel.
func drizzlePixelSquare(sums, weights []float32, width, height int, cx, cy, dropSize float64, value float32) {
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
			w := float32(overlapX * overlapY)
			idx := y*width + x
			sums[idx] += value * w
			weights[idx] += w
		}
	}
}

// drizzlePixelPoint deposits flux into the single nearest output pixel.
func drizzlePixelPoint(sums, weights []float32, width, height int, cx, cy float64, value float32) {
	x := int(math.Round(cx))
	y := int(math.Round(cy))
	if x < 0 || x >= width || y < 0 || y >= height {
		return
	}
	idx := y*width + x
	sums[idx] += value
	weights[idx] += 1
}

// drizzlePixelTurbo is a fast axis-aligned approximation: it accumulates into
// every output pixel whose center falls within the drop footprint with equal
// weight, skipping fractional-edge overlap computation.
func drizzlePixelTurbo(sums, weights []float32, width, height int, cx, cy, dropSize float64, value float32) {
	if dropSize <= 0 {
		dropSize = 1
	}
	half := dropSize / 2.0
	x0 := int(math.Ceil(cx - half))
	x1 := int(math.Floor(cx + half))
	y0 := int(math.Ceil(cy - half))
	y1 := int(math.Floor(cy + half))
	if x0 > x1 {
		// Drop is smaller than one pixel: fall back to nearest.
		drizzlePixelPoint(sums, weights, width, height, cx, cy, value)
		return
	}
	for y := y0; y <= y1; y++ {
		if y < 0 || y >= height {
			continue
		}
		for x := x0; x <= x1; x++ {
			if x < 0 || x >= width {
				continue
			}
			idx := y*width + x
			sums[idx] += value
			weights[idx] += 1
		}
	}
}

// drizzlePixelGaussian spreads flux with a Gaussian footprint.
// sigma = dropSize / (2 * sqrt(2*ln2)) so that FWHM == dropSize.
func drizzlePixelGaussian(sums, weights []float32, width, height int, cx, cy, dropSize float64, value float32) {
	if dropSize <= 0 {
		dropSize = 1
	}
	sigma := dropSize / (2 * math.Sqrt(2*math.Log(2)))
	twoSigSq := 2 * sigma * sigma
	radius := 3 * sigma
	x0 := int(math.Floor(cx - radius))
	x1 := int(math.Ceil(cx + radius))
	y0 := int(math.Floor(cy - radius))
	y1 := int(math.Ceil(cy + radius))
	for y := y0; y <= y1; y++ {
		if y < 0 || y >= height {
			continue
		}
		dy := float64(y) - cy
		for x := x0; x <= x1; x++ {
			if x < 0 || x >= width {
				continue
			}
			dx := float64(x) - cx
			w := float32(math.Exp(-(dx*dx + dy*dy) / twoSigSq))
			if w < 1e-6 {
				continue
			}
			idx := y*width + x
			sums[idx] += value * w
			weights[idx] += w
		}
	}
}

// drizzlePixelTophat spreads flux uniformly within a circular aperture of
// radius dropSize/2.
func drizzlePixelTophat(sums, weights []float32, width, height int, cx, cy, dropSize float64, value float32) {
	if dropSize <= 0 {
		dropSize = 1
	}
	radius := dropSize / 2.0
	radSq := radius * radius
	x0 := int(math.Floor(cx - radius))
	x1 := int(math.Ceil(cx + radius))
	y0 := int(math.Floor(cy - radius))
	y1 := int(math.Ceil(cy + radius))
	for y := y0; y <= y1; y++ {
		if y < 0 || y >= height {
			continue
		}
		dy := float64(y) - cy
		for x := x0; x <= x1; x++ {
			if x < 0 || x >= width {
				continue
			}
			dx := float64(x) - cx
			if dx*dx+dy*dy > radSq {
				continue
			}
			idx := y*width + x
			sums[idx] += value
			weights[idx] += 1
		}
	}
}

// lanczos evaluates the Lanczos kernel of order n at x.
func lanczos(x float64, n int) float64 {
	if x == 0 {
		return 1
	}
	fn := float64(n)
	if math.Abs(x) >= fn {
		return 0
	}
	pix := math.Pi * x
	return math.Sin(pix) / pix * math.Sin(pix/fn) / (pix / fn)
}

// drizzlePixelLanczos uses a separable Lanczos-n resampling kernel.
func drizzlePixelLanczos(sums, weights []float32, width, height int, cx, cy float64, value float32, n int) {
	x0 := int(math.Floor(cx)) - n + 1
	x1 := int(math.Floor(cx)) + n
	y0 := int(math.Floor(cy)) - n + 1
	y1 := int(math.Floor(cy)) + n
	for y := y0; y <= y1; y++ {
		if y < 0 || y >= height {
			continue
		}
		wy := lanczos(float64(y)-cy, n)
		if wy == 0 {
			continue
		}
		for x := x0; x <= x1; x++ {
			if x < 0 || x >= width {
				continue
			}
			wx := lanczos(float64(x)-cx, n)
			w := float32(wx * wy)
			if w == 0 {
				continue
			}
			idx := y*width + x
			sums[idx] += value * w
			weights[idx] += float32(math.Abs(float64(w)))
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
		if input.HasAffine {
			line += fmt.Sprintf(" | affine(rot=%.4f°)", input.AffineRotDeg)
		}
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

func affineRotationDeg(t processing.AffineTransform) float64 {
	return math.Atan2(t.D, t.A) * 180 / math.Pi
}

// firstDataInput returns the first input that is not marked ReferenceOnly,
// falling back to inputs[0] if all are reference-only.
func firstDataInput(inputs []Input) Input {
	for _, inp := range inputs {
		if !inp.ReferenceOnly {
			return inp
		}
	}
	return inputs[0]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
