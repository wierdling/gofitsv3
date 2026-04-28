package mosaic

import (
	"fmt"
	"math"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	"gofitsv3/internal/debuglog"
	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/processing"
)

// edgeTrim is the number of pixels to exclude from each edge of every input
// image before drizzling, to avoid border artifacts.
const edgeTrim = 20

// weightEpsilon is used when normalizing signed-kernel accumulators such as Lanczos.
// Very small denominators are treated as uncovered to avoid edge blow-ups.
const weightEpsilon float32 = 1e-12

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
	Excluded           bool
	// D2IX and D2IY hold the detector-to-image correction lookup tables parsed
	// from the D2IMARR FITS extensions of HST calibrated files. Either or both
	// may be nil if the file has no such corrections.
	D2IX *processing.D2ITable
	D2IY *processing.D2ITable
	// ChipFootprints holds the 4 trimmed corners (TL, TR, BL, BR) of each
	// individual SCI chip in combined-canvas pixel coordinates. Set only when
	// multiple SCI extensions were merged. Empty means use the full image bounds.
	ChipFootprints [][4][2]float64
	// ERRPixels holds the per-pixel noise (sigma) from the ERR FITS extension,
	// same dimensions as HDU.Data. Nil when the file has no ERR extension.
	// Used as inverse-variance weights during drizzle: weight *= 1/err².
	ERRPixels []float32
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

// BitMask stores one boolean pixel mask bit per pixel. This is used for cosmic
// ray masks in the final drizzle pass to avoid retaining one byte per pixel per
// frame.
//
// NOTE: processing.BuildCRMasksFromModel and processing.BuildCosmicRayMasks
// currently return [][]bool. This file compresses those masks immediately after
// those calls. To reduce peak memory too, change those processing-package
// functions to produce BitMask-compatible masks directly or tile their work.
type BitMask []uint64

func NewBitMask(n int) BitMask {
	if n <= 0 {
		return nil
	}
	return make([]uint64, (n+63)/64)
}

func (m BitMask) Set(i int) {
	if i < 0 {
		return
	}
	word := i >> 6
	if word >= len(m) {
		return
	}
	m[word] |= uint64(1) << uint(i&63)
}

func (m BitMask) Get(i int) bool {
	if len(m) == 0 || i < 0 {
		return false
	}
	word := i >> 6
	if word >= len(m) {
		return false
	}
	return (m[word] & (uint64(1) << uint(i&63))) != 0
}

func compressCRMasks(boolMasks [][]bool) []BitMask {
	if len(boolMasks) == 0 {
		return nil
	}
	masks := make([]BitMask, len(boolMasks))
	for i, bm := range boolMasks {
		if len(bm) == 0 {
			continue
		}
		mask := NewBitMask(len(bm))
		for px, flagged := range bm {
			if flagged {
				mask.Set(px)
			}
		}
		masks[i] = mask
	}
	return masks
}

func isFinite32(v float32) bool {
	return math.Float32bits(v)&0x7f800000 != 0x7f800000
}

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

// defaultSearchRadiusArcsec is the TweakReg catalog-matching search radius used
// when the caller supplies zero (i.e. when loading projects that predate the setting).
const defaultSearchRadiusArcsec = 1.5

// AlignmentMode selects the residual star-alignment model used after WCS placement.
type AlignmentMode int

const (
	// AlignmentModeGeneralAffine fits a full 6-parameter affine using the
	// legacy warp-based strategy (kept for backward compatibility).
	AlignmentModeGeneralAffine AlignmentMode = iota
	// AlignmentModeRScale fits a similarity transform using the legacy
	// warp-based strategy (kept for backward compatibility).
	AlignmentModeRScale
	// AlignmentModeTweakRegRScale is the default: catalog-based matching with
	// full WCS projection (no image warp) + similarity transform fitting.
	AlignmentModeTweakRegRScale
	// AlignmentModeTweakRegGeneral is catalog-based matching with full WCS
	// projection + full 6-parameter affine fitting.
	AlignmentModeTweakRegGeneral
)

func normalizeAlignmentMode(mode AlignmentMode) AlignmentMode {
	switch mode {
	case AlignmentModeRScale, AlignmentModeTweakRegRScale, AlignmentModeTweakRegGeneral:
		return mode
	default:
		return AlignmentModeGeneralAffine
	}
}

type Options struct {
	// Scale is the internal output/input pixel size ratio used directly when
	// FinalScale is zero.  A value of 2.0 doubles the output dimensions.
	Scale float64
	// FinalScale is the desired output plate scale in arcseconds per pixel,
	// matching AstroDrizzle's final_scale parameter.  When > 0, Build reads
	// the native plate scale from the reference image WCS and computes:
	//
	//   Scale = nativePlateScale / FinalScale
	//
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
	// UseERRWeighting, when true, weights each input pixel by 1/err² using the
	// ERR FITS extension. Pixels with higher noise contribute less to the output.
	// When false (the default) all pixels are weighted equally by geometric overlap.
	UseERRWeighting bool
	// KeepWeights retains the final output weight/coverage image in Result.Weights.
	// Leave false for lower memory use. The final drizzle pass still allocates
	// weights while normalizing the image, but setting this to false releases that
	// full-size buffer before returning the Result.
	KeepWeights bool
	// CRSeedSNR and CRDerivScale tune the drizzle-style CR detection.
	// Zero values fall back to the defaults (4.0 and 1.2 respectively).
	// Only used when CRMethod == CRMethodDrizzle.
	CRSeedSNR    float64
	CRDerivScale float64
	// Skysub controls optional AstroDrizzle-style sky subtraction applied to
	// non-reference inputs before CR rejection and final drizzle.
	Skysub SkysubOptions
}

type InputStatus struct {
	Path          string
	Status        string
	Error         string
	Included      bool
	Cleaned       bool
	SkySubtracted bool
	SkyValue      float64
	OffsetX       float64
	OffsetY       float64
	HasAffine     bool
	AffineRotDeg  float64
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
	// Alignment diagnostics are intentionally part of the public result shape,
	// but this file cannot fully populate them until the processing-package
	// alignment estimators return match/residual statistics. Zero means unknown.
	MatchedStars int
	RMS          float64
	MedianError  float64
	MaxError     float64
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
	debuglog.Log("Build: starting drizzle")
	defer debuglog.Log("Build: finished")
	if len(inputs) == 0 {
		return nil, fmt.Errorf("no FITS inputs selected")
	}

	// Resolve FinalScale (arcsec/pixel) → internal Scale multiplier.
	// Use the same WCS anchor that will be used for the output header, so a
	// ReferenceOnly baseline and same-scale filter runs produce matching grids.
	if options.FinalScale > 0 {
		ref := wcsReferenceInput(inputs)
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
	if len(dataPlanned) == 0 {
		return nil, fmt.Errorf("no data-bearing FITS inputs selected")
	}

	workingPixels, skyApplied, skyValues, err := prepareSkysubWorkingPixels(planned, options.Skysub)
	if err != nil {
		return nil, err
	}
	for i := range planned {
		if planned[i].input.ReferenceOnly || !skyApplied[i] {
			continue
		}
		statuses[planned[i].statusIndex].SkySubtracted = true
		statuses[planned[i].statusIndex].SkyValue = skyValues[i]
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
			_, sigma := processing.EstimateBackground(workingPixels[pi])
			frameInfos[slot] = processing.FrameInfo{
				Pixels:      workingPixels[pi],
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

	var crMasks []BitMask
	switch {
	case effectiveCR == CRMethodDrizzle && len(dataPlanned) > 1:
		// Separate pass: drizzle each frame individually with SepKernel to build
		// per-frame images, then median-combine into a clean model. This gives the
		// model better fidelity than inverse-blot when the sep kernel is non-trivial.
		sepFrames := make([]SepFrame, len(dataPlanned))
		for slot, pi := range dataPlanned {
			sepFrames[slot] = drizzleSepFrame(planned[pi], workingPixels[pi], width, height, minX, minY, options.Scale, dropSize, options.SepKernel, options.UseERRWeighting)
		}
		model := buildMedianModel(sepFrames, width, height, len(dataPlanned))
		sepFrames = nil // allow GC before final drizzle pass
		// Wire full WCS mappers into each FrameInfo so BuildCRMasksFromModel
		// blots using the same per-pixel mapping as drizzleSepFrame did.
		// Without this, the affine SourceToRef approximation can be off by
		// several pixels when SIP distortion is present, causing stars to be
		// falsely flagged (blot samples background instead of the star peak).
		for slot, pi := range dataPlanned {
			pi := pi // capture for closure
			frameInfos[slot].MapFunc = func(x, y float64) (float64, float64) {
				return planned[pi].mapPixel(x, y)
			}
		}
		crSeedSNR := options.CRSeedSNR
		if crSeedSNR <= 0 {
			crSeedSNR = 4.0
		}
		crDerivScale := options.CRDerivScale
		if crDerivScale <= 0 {
			crDerivScale = 1.2
		}
		boolMasks := processing.BuildCRMasksFromModel(
			frameInfos, model, width, height, minX, minY, options.Scale,
			processing.DrizzleStyleCROptions{SeedSNR: crSeedSNR, DerivScale: crDerivScale},
		)
		crMasks = compressCRMasks(boolMasks)
		boolMasks = nil
	case effectiveCR == CRMethodLegacy && len(dataPlanned) > 1:
		boolMasks := processing.BuildCosmicRayMasks(frameInfos, 5.0, 2.0)
		crMasks = compressCRMasks(boolMasks)
		boolMasks = nil
	}

	// Final drizzle pass: accumulate all frames into the output using FinalKernel.
	sums := make([]float32, width*height)
	weights := make([]float32, width*height)
	finalKernel := options.FinalKernel

	for i := range planned {
		if planned[i].input.ReferenceOnly {
			continue
		}
		pixels := workingPixels[i]
		var crMask BitMask
		cleaned := false

		if effectiveCR != CRMethodNone {
			slot := crMaskIndex[i]
			if slot >= 0 && crMasks != nil {
				crMask = crMasks[slot]
				cleaned = len(crMask) > 0
			} else if len(dataPlanned) == 1 {
				// Only one data frame — no inter-frame comparison possible.
				// Do not apply any CR removal (a lone frame has no reference to
				// distinguish a real bright pixel from a cosmic ray).
			} else {
				// Single-frame fallback: always use legacy Laplacian detector.
				_, sigma := processing.EstimateBackground(pixels)
				pixels = processing.RemoveCosmicRays(pixels, planned[i].input.HDU.Data.Width, planned[i].input.HDU.Data.Height, sigma, 2, nil)
				cleaned = true
			}
			if cleaned {
				statuses[planned[i].statusIndex].Cleaned = true
			}
			statuses[planned[i].statusIndex].Status = drizzleBuildStatus(statuses[planned[i].statusIndex].SkySubtracted, cleaned)
		} else if statuses[planned[i].statusIndex].Status == "reference" {
			if statuses[planned[i].statusIndex].SkySubtracted {
				statuses[planned[i].statusIndex].Status = drizzleBuildStatus(true, false)
			} else {
				statuses[planned[i].statusIndex].Status = "reference drizzled"
			}
		} else {
			statuses[planned[i].statusIndex].Status = drizzleBuildStatus(statuses[planned[i].statusIndex].SkySubtracted, false)
		}
		includedCount++

		trimX := effectiveEdgeTrim(planned[i].input.HDU.Data.Width, options.Scale)
		trimY := effectiveEdgeTrim(planned[i].input.HDU.Data.Height, options.Scale)
		drizzlePlannedInput(planned[i], sums, weights, width, height, minX, minY,
			options.Scale, dropSize, finalKernel, options.UseERRWeighting, crMask, pixels, trimX, trimY)
	}

	normalizeAccumulatedImage(sums, weights)

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
			trimX := float64(effectiveEdgeTrim(p.input.HDU.Data.Width, options.Scale))
			trimY := float64(effectiveEdgeTrim(p.input.HDU.Data.Height, options.Scale))
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
		Pixels:              sums,
		Weights:             weights,
		Width:               width,
		Height:              height,
		OriginX:             minX,
		OriginY:             minY,
		Scale:               options.Scale,
		OutputHeader:        buildOutputHeader(wcsReferenceInput(inputs), width, height, minX, minY, options.Scale, includedCount),
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

// SortInputsByWCSDistance reorders inputs (and the parallel statuses slice,
// if provided and the same length) in-place, grouping chips from the same
// source file together.  File groups are ordered by the WCS distance of their
// lowest-SCIExt chip from inputs[0]; chips within each group are ordered by
// SCIExt so [sci,1] always precedes [sci,2].  inputs[0]'s file group always
// comes first and inputs[0] itself is always the first element.
// If statuses is nil or a different length it is ignored.
func SortInputsByWCSDistance(inputs []Input, statuses []InputStatus) {
	if len(inputs) < 2 {
		return
	}

	ref := inputs[0]

	// Build file groups preserving the original per-group insertion order.
	type fileGroup struct {
		path    string
		indices []int // original indices into inputs
		dist    float64
	}
	var groups []fileGroup
	groupIdx := make(map[string]int, len(inputs))
	for i, inp := range inputs {
		gi, ok := groupIdx[inp.Path]
		if !ok {
			gi = len(groups)
			groupIdx[inp.Path] = gi
			groups = append(groups, fileGroup{path: inp.Path})
		}
		groups[gi].indices = append(groups[gi].indices, i)
	}

	// Sort chips within each group by SCIExt.
	for g := range groups {
		idxs := groups[g].indices
		sort.Slice(idxs, func(a, b int) bool {
			return inputs[idxs[a]].SCIExt < inputs[idxs[b]].SCIExt
		})
	}

	// Compute each group's WCS distance once. Do not do this in the sort
	// comparator; WCS projection is substantially more expensive than compare.
	for g := range groups {
		if groups[g].path == ref.Path {
			groups[g].dist = -1
			continue
		}
		rep := inputs[groups[g].indices[0]]
		d, err := processing.CenterDistInRefPixels(
			rep.HDU.Header, rep.HDU.Data.Width, rep.HDU.Data.Height,
			ref.HDU.Header, ref.HDU.Data.Width, ref.HDU.Data.Height,
		)
		if err != nil {
			d = math.MaxFloat64
		}
		groups[g].dist = d
	}

	// Sort file groups by WCS distance of their representative chip from ref.
	// The group containing inputs[0] always sorts first.
	sort.SliceStable(groups, func(a, b int) bool {
		return groups[a].dist < groups[b].dist
	})

	// Flatten into a flat index order derived from the sorted groups.
	flatIdx := make([]int, 0, len(inputs))
	for _, g := range groups {
		flatIdx = append(flatIdx, g.indices...)
	}
	// Ensure the original inputs[0] is literally first (handles edge case where
	// another chip of the same file has a lower SCIExt than the reference).
	for i, origIdx := range flatIdx {
		if origIdx == 0 {
			flatIdx[0], flatIdx[i] = flatIdx[i], flatIdx[0]
			break
		}
	}

	sortedIn := make([]Input, len(inputs))
	for dst, src := range flatIdx {
		sortedIn[dst] = inputs[src]
	}
	copy(inputs, sortedIn)

	if len(statuses) == len(inputs) {
		origSt := make([]InputStatus, len(statuses))
		copy(origSt, statuses)
		sortedSt := make([]InputStatus, len(statuses))
		for dst, src := range flatIdx {
			sortedSt[dst] = origSt[src]
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
		if inputs[i].Excluded {
			entries[i-1] = entry{idx: i, dist: math.MaxFloat64}
			continue
		}
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
	return AlignInputsByStarsWithMode(inputs, 1, AlignmentModeTweakRegRScale, 0)
}

func AlignInputsByStarsWithMode(inputs []Input, numRefs int, mode AlignmentMode, searchRadiusArcsec float64) ([]StarAlignmentResult, error) {
	debuglog.Log("AlignInputsByStarsWithMode: starting")
	defer debuglog.Log("AlignInputsByStarsWithMode: finished")
	if len(inputs) == 0 {
		return nil, fmt.Errorf("no FITS inputs selected")
	}
	if inputs[0].Excluded {
		return nil, fmt.Errorf("reference image is excluded")
	}
	mode = normalizeAlignmentMode(mode)
	if searchRadiusArcsec <= 0 {
		searchRadiusArcsec = defaultSearchRadiusArcsec
	}

	if numRefs < 1 {
		numRefs = 1
	}
	if numRefs > len(inputs) {
		numRefs = len(inputs)
	}

	results := make([]StarAlignmentResult, len(inputs))
	for i := range inputs {
		if inputs[i].Excluded {
			results[i] = StarAlignmentResult{
				OffsetX:            inputs[i].OffsetX,
				OffsetY:            inputs[i].OffsetY,
				ManualTransform:    inputs[i].ManualTransform,
				HasManualTransform: inputs[i].HasManualTransform,
				Error:              "excluded",
				Applied:            false,
			}
		}
	}
	for r := 0; r < numRefs; r++ {
		if inputs[r].Excluded {
			continue
		}
		results[r] = StarAlignmentResult{
			OffsetX:            inputs[r].OffsetX,
			OffsetY:            inputs[r].OffsetY,
			ManualTransform:    inputs[r].ManualTransform,
			HasManualTransform: inputs[r].HasManualTransform,
			Applied:            true,
		}
	}

	aligned := make([]bool, len(inputs))
	for i := range inputs {
		if inputs[i].Excluded {
			aligned[i] = true
		}
	}
	for r := 0; r < numRefs; r++ {
		if !inputs[r].Excluded {
			aligned[r] = true
		}
	}
	queue := make([]int, 0, numRefs)
	for r := 0; r < numRefs; r++ {
		if !inputs[r].Excluded {
			queue = append(queue, r)
		}
	}
	lastErr := make([]string, len(inputs))
	ordered := sortedByDistFromRef(inputs)

	// For TweakReg modes, extract reference stars from each designated reference
	// image once up front.
	isTweakReg := mode == AlignmentModeTweakRegRScale || mode == AlignmentModeTweakRegGeneral
	fitgeom := "rscale"
	if mode == AlignmentModeTweakRegGeneral {
		fitgeom = "general"
	}

	type refCache struct {
		input  Input
		stars  []processing.Star
		w0toR  processing.AffineTransform
		wRto0  processing.AffineTransform
		hasWCS bool
	}
	refCaches := make([]refCache, numRefs)
	refCaches[0] = refCache{input: inputs[0], hasWCS: true}
	if isTweakReg {
		refCaches[0].stars = processing.ExtractAndLimitStars(
			inputs[0].HDU.Data.Pixels, inputs[0].HDU.Data.Width, inputs[0].HDU.Data.Height, 4.0, 3, 200)
		debuglog.Log(fmt.Sprintf("AlignInputsByStarsWithMode: ref[0] extracted %d stars", len(refCaches[0].stars)))
	}
	for r := 1; r < numRefs; r++ {
		if inputs[r].Excluded {
			refCaches[r] = refCache{input: inputs[r]}
			continue
		}
		w0toR, err0 := processing.ComputeWCSTransform(inputs[r].HDU.Header, inputs[0].HDU.Header)
		wRto0, err1 := processing.ComputeWCSTransform(inputs[0].HDU.Header, inputs[r].HDU.Header)
		if err0 != nil || err1 != nil {
			debuglog.Log(fmt.Sprintf("AlignInputsByStarsWithMode: ref[%d] WCS error: %v / %v", r, err0, err1))
			refCaches[r] = refCache{input: inputs[r]}
			continue
		}
		rc := refCache{input: inputs[r], w0toR: w0toR, wRto0: wRto0, hasWCS: true}
		if isTweakReg {
			rc.stars = processing.ExtractAndLimitStars(
				inputs[r].HDU.Data.Pixels, inputs[r].HDU.Data.Width, inputs[r].HDU.Data.Height, 4.0, 3, 200)
			debuglog.Log(fmt.Sprintf("AlignInputsByStarsWithMode: ref[%d] extracted %d stars", r, len(rc.stars)))
		}
		refCaches[r] = rc
	}

	// alignOneToAnyRef tries each designated reference in order and returns on
	// the first success.  For secondary references (r>0) the refinement is
	// composed back to inputs[0] pixel space.
	type alignOneResult struct {
		i          int
		refinement processing.AffineTransform
		errMsg     string
		ok         bool
	}

	alignOneToRef := func(i int) alignOneResult {
		for r := 0; r < numRefs; r++ {
			rc := refCaches[r]
			if !rc.hasWCS {
				continue
			}
			var (
				refinement processing.AffineTransform
				err        error
			)
			switch mode {
			case AlignmentModeTweakRegRScale, AlignmentModeTweakRegGeneral:
				mapper, mapErr := processing.NewWCSMapper(
					inputs[i].HDU.Header, inputs[i].D2IX, inputs[i].D2IY,
					rc.input.HDU.Header, rc.input.D2IX, rc.input.D2IY,
				)
				if mapErr != nil {
					err = fmt.Errorf("WCSMapper: %v", mapErr)
					break
				}
				refinement, err = processing.EstimateTweakRegAlignmentWithRefStars(
					inputs[i].HDU.Data.Pixels,
					inputs[i].HDU.Data.Width,
					inputs[i].HDU.Data.Height,
					mapper,
					rc.stars,
					rc.input.HDU.Data.Width,
					rc.input.HDU.Data.Height,
					rc.input.HDU.Header,
					searchRadiusArcsec,
					fitgeom,
				)
			case AlignmentModeRScale:
				refinement, err = processing.EstimateRScaleAfterWCS(
					inputs[i].HDU.Data.Pixels, inputs[i].HDU.Data.Width, inputs[i].HDU.Data.Height, inputs[i].HDU.Header,
					rc.input.HDU.Data.Pixels, rc.input.HDU.Data.Width, rc.input.HDU.Data.Height, rc.input.HDU.Header,
					inputs[i].OffsetX, inputs[i].OffsetY,
				)
			default:
				refinement, err = processing.EstimateAffineAfterWCS(
					inputs[i].HDU.Data.Pixels, inputs[i].HDU.Data.Width, inputs[i].HDU.Data.Height, inputs[i].HDU.Header,
					rc.input.HDU.Data.Pixels, rc.input.HDU.Data.Width, rc.input.HDU.Data.Height, rc.input.HDU.Header,
					inputs[i].OffsetX, inputs[i].OffsetY,
				)
			}
			if err != nil {
				debuglog.Log(fmt.Sprintf("AlignInputsByStarsWithMode: input[%d] vs ref[%d]: %v", i, r, err))
				continue
			}
			if r > 0 {
				refinement = processing.ComposeAffineTransforms(rc.wRto0,
					processing.ComposeAffineTransforms(refinement, rc.w0toR))
			}
			return alignOneResult{i: i, refinement: refinement, ok: true}
		}
		return alignOneResult{i: i, errMsg: lastErr[i]}
	}

	primaryPassDone := false
	for len(queue) > 0 {
		refIdx := queue[0]
		queue = queue[1:]

		if refIdx < numRefs {
			// Designated reference: run (or re-use) the full multi-ref TweakReg pass.
			// Only run once — alignOneToRef already tries every reference internally.
			if primaryPassDone {
				continue
			}
			primaryPassDone = true
			// Collect unaligned inputs and run them concurrently against the reference.
			var toAlign []int
			for _, i := range ordered {
				if !aligned[i] && !inputs[i].Excluded {
					toAlign = append(toAlign, i)
				}
			}
			if len(toAlign) == 0 {
				continue
			}

			ch := make(chan alignOneResult, len(toAlign))
			sem := make(chan struct{}, runtime.NumCPU())
			var wg sync.WaitGroup
			for _, i := range toAlign {
				wg.Add(1)
				sem <- struct{}{}
				go func(i int) {
					defer wg.Done()
					defer func() { <-sem }()
					ch <- alignOneToRef(i)
				}(i)
			}
			wg.Wait()
			close(ch)

			for r := range ch {
				if r.ok {
					results[r.i] = StarAlignmentResult{
						OffsetX:            inputs[r.i].OffsetX,
						OffsetY:            inputs[r.i].OffsetY,
						ManualTransform:    r.refinement,
						HasManualTransform: true,
						Applied:            true,
					}
					aligned[r.i] = true
					queue = append(queue, r.i)
				} else {
					lastErr[r.i] = r.errMsg
				}
			}
		} else {
			// Chain fallback: translate unaligned images against an already-aligned intermediate.
			// (refIdx here is always a non-reference intermediate, never a designated reference.)
			for _, i := range ordered {
				if aligned[i] || inputs[i].Excluded {
					continue
				}
				dx, dy, err := processing.EstimateTranslationAfterWCS(
					inputs[i].HDU.Data.Pixels, inputs[i].HDU.Data.Width, inputs[i].HDU.Data.Height, inputs[i].HDU.Header,
					inputs[refIdx].HDU.Data.Pixels, inputs[refIdx].HDU.Data.Width, inputs[refIdx].HDU.Data.Height, inputs[refIdx].HDU.Header,
					0, 0,
				)
				if err != nil {
					lastErr[i] = err.Error()
					continue
				}
				bToA, err := processing.ComputeWCSTransform(inputs[refIdx].HDU.Header, inputs[0].HDU.Header)
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
				aligned[i] = true
				queue = append(queue, i)
			}
		}
	}

	for i := 1; i < len(inputs); i++ {
		if inputs[i].Excluded {
			continue
		}
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
	return AlignInputsBySelectedStarsWithMode(inputs, refStars, 1, AlignmentModeTweakRegRScale, 0)
}

// AlignInputsBySelectedStarsWithMode aligns all non-reference inputs to the
// nearest reference image that has enough star overlap.  numRefs designates the
// first numRefs inputs as pre-aligned references (they are passed through
// unchanged).  refStars are positions in inputs[0] pixel space; they are
// automatically projected into each secondary reference's pixel space via the
// linear WCS when needed.  The returned ManualTransform for every aligned image
// is always expressed in inputs[0] pixel space.
func AlignInputsBySelectedStarsWithMode(inputs []Input, refStars []processing.Star, numRefs int, mode AlignmentMode, searchRadiusArcsec float64) ([]StarAlignmentResult, error) {
	debuglog.Log("AlignInputsBySelectedStarsWithMode: starting")
	defer debuglog.Log("AlignInputsBySelectedStarsWithMode: finished")
	if len(inputs) == 0 {
		return nil, fmt.Errorf("no FITS inputs selected")
	}
	if inputs[0].Excluded {
		return nil, fmt.Errorf("reference image is excluded")
	}
	if len(refStars) == 0 {
		return nil, fmt.Errorf("no reference stars provided")
	}
	mode = normalizeAlignmentMode(mode)
	if searchRadiusArcsec <= 0 {
		searchRadiusArcsec = defaultSearchRadiusArcsec
	}
	if numRefs < 1 {
		numRefs = 1
	}
	if numRefs > len(inputs) {
		numRefs = len(inputs)
	}

	results := make([]StarAlignmentResult, len(inputs))
	for i := range inputs {
		if inputs[i].Excluded {
			results[i] = StarAlignmentResult{
				OffsetX:            inputs[i].OffsetX,
				OffsetY:            inputs[i].OffsetY,
				ManualTransform:    inputs[i].ManualTransform,
				HasManualTransform: inputs[i].HasManualTransform,
				Error:              "excluded",
				Applied:            false,
			}
		}
	}
	for r := 0; r < numRefs; r++ {
		if inputs[r].Excluded {
			continue
		}
		results[r] = StarAlignmentResult{
			OffsetX:            inputs[r].OffsetX,
			OffsetY:            inputs[r].OffsetY,
			ManualTransform:    inputs[r].ManualTransform,
			HasManualTransform: inputs[r].HasManualTransform,
			Applied:            true,
		}
	}

	// For each secondary reference, compute:
	//   starsInR  – refStars projected into inputs[r] pixel space
	//   w0toR     – linear WCS transform: inputs[0] pixels → inputs[r] pixels
	//   wRto0     – linear WCS transform: inputs[r] pixels → inputs[0] pixels
	// These are used to convert a refinement solved in inputs[r] space back to
	// inputs[0] space via:  ManualTransform = wRto0 ∘ T_r ∘ w0toR
	type refEntry struct {
		input  Input
		stars  []processing.Star
		w0toR  processing.AffineTransform
		wRto0  processing.AffineTransform
		hasWCS bool
	}
	refs := make([]refEntry, numRefs)
	refs[0] = refEntry{input: inputs[0], stars: refStars, hasWCS: true}
	for r := 1; r < numRefs; r++ {
		if inputs[r].Excluded {
			refs[r] = refEntry{input: inputs[r]}
			continue
		}
		w0toR, err0 := processing.ComputeWCSTransform(inputs[r].HDU.Header, inputs[0].HDU.Header)
		wRto0, err1 := processing.ComputeWCSTransform(inputs[0].HDU.Header, inputs[r].HDU.Header)
		if err0 != nil || err1 != nil {
			debuglog.Log(fmt.Sprintf("AlignInputsBySelectedStarsWithMode: ref[%d] WCS error: %v / %v", r, err0, err1))
			refs[r] = refEntry{input: inputs[r]}
			continue
		}
		starsR := make([]processing.Star, len(refStars))
		for j, s := range refStars {
			sx, sy := processing.ApplyAffineTransform(w0toR, s.X, s.Y)
			starsR[j] = processing.Star{X: sx, Y: sy, Flux: s.Flux}
		}
		refs[r] = refEntry{input: inputs[r], stars: starsR, w0toR: w0toR, wRto0: wRto0, hasWCS: true}
	}

	fitgeom := "rscale"
	if mode == AlignmentModeTweakRegGeneral {
		fitgeom = "general"
	}

	// Align closest images first so results are more stable across runs.
	for _, i := range sortedByDistFromRef(inputs) {
		if i < numRefs || inputs[i].Excluded {
			continue
		}

		var lastErr string
		aligned := false

		for r := 0; r < numRefs && !aligned; r++ {
			ref := refs[r]
			if !ref.hasWCS {
				continue
			}

			var (
				refinement processing.AffineTransform
				err        error
			)
			switch mode {
			case AlignmentModeTweakRegRScale, AlignmentModeTweakRegGeneral:
				mapper, mapErr := processing.NewWCSMapper(
					inputs[i].HDU.Header, inputs[i].D2IX, inputs[i].D2IY,
					ref.input.HDU.Header, ref.input.D2IX, ref.input.D2IY,
				)
				if mapErr != nil {
					err = fmt.Errorf("WCSMapper: %v", mapErr)
					break
				}
				refinement, err = processing.EstimateTweakRegAlignmentWithRefStars(
					inputs[i].HDU.Data.Pixels,
					inputs[i].HDU.Data.Width,
					inputs[i].HDU.Data.Height,
					mapper,
					ref.stars,
					ref.input.HDU.Data.Width,
					ref.input.HDU.Data.Height,
					ref.input.HDU.Header,
					searchRadiusArcsec,
					fitgeom,
				)
			case AlignmentModeRScale:
				refinement, err = processing.EstimateRScaleFromRefStars(
					ref.stars,
					inputs[i].HDU.Data.Pixels,
					inputs[i].HDU.Data.Width,
					inputs[i].HDU.Data.Height,
					inputs[i].HDU.Header,
					ref.input.HDU.Header,
					inputs[i].OffsetX,
					inputs[i].OffsetY,
					manualTransformPtr(inputs[i]),
				)
			default:
				refinement, err = processing.EstimateAffineFromRefStars(
					ref.stars,
					inputs[i].HDU.Data.Pixels,
					inputs[i].HDU.Data.Width,
					inputs[i].HDU.Data.Height,
					inputs[i].HDU.Header,
					ref.input.HDU.Header,
					inputs[i].OffsetX,
					inputs[i].OffsetY,
					manualTransformPtr(inputs[i]),
				)
			}
			if err != nil {
				debuglog.Log(fmt.Sprintf("AlignInputsBySelectedStarsWithMode: input[%d] vs ref[%d]: %v", i, r, err))
				lastErr = err.Error()
				continue
			}

			// Convert refinement from inputs[r] space to inputs[0] space.
			manualT := refinement
			if r > 0 {
				manualT = processing.ComposeAffineTransforms(ref.wRto0,
					processing.ComposeAffineTransforms(refinement, ref.w0toR))
			}
			if inputs[i].HasManualTransform {
				manualT = processing.ComposeAffineTransforms(manualT, inputs[i].ManualTransform)
			}
			results[i] = StarAlignmentResult{
				OffsetX:            inputs[i].OffsetX,
				OffsetY:            inputs[i].OffsetY,
				ManualTransform:    manualT,
				HasManualTransform: true,
				Applied:            true,
			}
			aligned = true
		}

		if !aligned {
			results[i] = StarAlignmentResult{
				OffsetX:            inputs[i].OffsetX,
				OffsetY:            inputs[i].OffsetY,
				ManualTransform:    inputs[i].ManualTransform,
				HasManualTransform: inputs[i].HasManualTransform,
				Error:              lastErr,
				Applied:            false,
			}
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
		status := "loaded"
		if input.Excluded {
			status = "excluded"
		}
		statuses[i] = InputStatus{Path: InputKey(input), Status: status, OffsetX: input.OffsetX, OffsetY: input.OffsetY,
			HasAffine: input.HasManualTransform, AffineRotDeg: affineRotationDeg(input.ManualTransform)}
	}
	if ref.Excluded {
		statuses[0].Status = "failed"
		statuses[0].Error = "reference image is excluded"
		return nil, statuses, 0, 0, 0, 0, fmt.Errorf("reference image is excluded")
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
		if input.Excluded {
			continue
		}
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

func imageCorners(width, height int) [4][2]float64 {
	maxX := float64(width - 1)
	maxY := float64(height - 1)
	return [4][2]float64{{0, 0}, {maxX, 0}, {0, maxY}, {maxX, maxY}}
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

// SepFrame holds a singly drizzled frame and a compact coverage mask used by
// the drizzle-style CR median model. A bitset keeps the median model from
// treating uncovered pixels as real samples without retaining a second full
// weight image per input.
type SepFrame struct {
	Image   []float32
	Covered BitMask
}

// drizzleSepFrame drizzles a single planned input into its own output-size
// accumulator and returns the normalized image plus a compact coverage mask.
// Used to build per-frame images for the AstroDrizzle-style separate pass.
func drizzleSepFrame(p plannedInput, pixels []float32, outW, outH int, minX, minY, scale, dropSize float64, kernel DrizzleKernel, useERRWeighting bool) SepFrame {
	// Use out as the flux accumulator directly; weights tracks coverage.
	// This avoids allocating a separate sums array.
	out := make([]float32, outW*outH)
	weights := make([]float32, outW*outH)
	// Keep the original behavior of not trimming the separate CR-model frames.
	trimX, trimY := 0, 0
	drizzlePlannedInput(p, out, weights, outW, outH, minX, minY, scale, dropSize, kernel, useERRWeighting, nil, pixels, trimX, trimY)

	covered := NewBitMask(len(out))
	for i := range out {
		if abs32(weights[i]) <= weightEpsilon {
			out[i] = float32(math.NaN())
			continue
		}
		covered.Set(i)
		out[i] /= weights[i]
	}
	return SepFrame{Image: out, Covered: covered}
}

// buildMedianModel combines n per-frame drizzled images into a single clean
// model using minmed (n ≤ 3) or a true median (n > 3) at each pixel. The
// per-frame coverage mask prevents uncovered sep-frame pixels from biasing the
// model, while avoiding full retained weight maps for every input.
func buildMedianModel(frames []SepFrame, outW, outH, n int) []float32 {
	model := make([]float32, outW*outH)
	vals := make([]float32, 0, n)
	for i := range model {
		vals = vals[:0]
		for _, frame := range frames {
			if len(frame.Image) <= i || !frame.Covered.Get(i) {
				continue
			}
			v := frame.Image[i]
			if isFinite32(v) {
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
		median := medianSorted(vals)
		if n <= 3 {
			var sum float32
			for _, v := range vals {
				sum += v
			}
			mean := sum / float32(len(vals))
			if mean < median {
				model[i] = mean
			} else {
				model[i] = median
			}
		} else {
			model[i] = median
		}
	}
	return model
}

func medianSorted(vals []float32) float32 {
	n := len(vals)
	if n == 0 {
		return float32(math.NaN())
	}
	mid := n / 2
	if n%2 == 1 {
		return vals[mid]
	}
	return 0.5 * (vals[mid-1] + vals[mid])
}

func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

func normalizeAccumulatedImage(sums, weights []float32) {
	for i := range sums {
		if i >= len(weights) || abs32(weights[i]) <= weightEpsilon {
			sums[i] = float32(math.NaN())
			continue
		}
		sums[i] /= weights[i]
	}
}

func drizzlePlannedInput(p plannedInput, sums, weights []float32, width, height int, minX, minY, scale, dropSize float64, kernel DrizzleKernel, useERRWeighting bool, crMask BitMask, pixels []float32, trimX, trimY int) {
	switch kernel {
	case KernelPoint:
		drizzlePlannedInputPoint(p, sums, weights, width, height, minX, minY, scale, useERRWeighting, crMask, pixels, trimX, trimY)
	case KernelTurbo:
		drizzlePlannedInputTurbo(p, sums, weights, width, height, minX, minY, scale, dropSize, useERRWeighting, crMask, pixels, trimX, trimY)
	case KernelGaussian:
		drizzlePlannedInputGaussian(p, sums, weights, width, height, minX, minY, scale, dropSize, useERRWeighting, crMask, pixels, trimX, trimY)
	case KernelTophat:
		drizzlePlannedInputTophat(p, sums, weights, width, height, minX, minY, scale, dropSize, useERRWeighting, crMask, pixels, trimX, trimY)
	case KernelLanczos2:
		drizzlePlannedInputLanczos(p, sums, weights, width, height, minX, minY, scale, 2, useERRWeighting, crMask, pixels, trimX, trimY)
	case KernelLanczos3:
		drizzlePlannedInputLanczos(p, sums, weights, width, height, minX, minY, scale, 3, useERRWeighting, crMask, pixels, trimX, trimY)
	default:
		drizzlePlannedInputSquare(p, sums, weights, width, height, minX, minY, scale, dropSize, useERRWeighting, crMask, pixels, trimX, trimY)
	}
}

func drizzlePixelWeight(p plannedInput, idx int, useERRWeighting bool) float32 {
	if !useERRWeighting {
		return 1
	}
	if errPix := p.input.ERRPixels; errPix != nil && idx < len(errPix) {
		if e := errPix[idx]; e > 0 && isFinite32(e) {
			return 1.0 / (e * e)
		}
	}
	return 1
}

func drizzlePlannedInputPoint(p plannedInput, sums, weights []float32, width, height int, minX, minY, scale float64, useERRWeighting bool, crMask BitMask, pixels []float32, trimX, trimY int) {
	data := p.input.HDU.Data
	for y := trimY; y < data.Height-trimY; y++ {
		row := y * data.Width
		for x := trimX; x < data.Width-trimX; x++ {
			idx := row + x
			if crMask != nil && crMask.Get(idx) {
				continue
			}
			value := pixels[idx]
			if !isFinite32(value) {
				continue
			}
			refX, refY := p.mapPixel(float64(x), float64(y))
			outX := (refX - minX) * scale
			outY := (refY - minY) * scale
			drizzlePixelPoint(sums, weights, width, height, outX, outY, value, drizzlePixelWeight(p, idx, useERRWeighting))
		}
	}
}

func drizzlePlannedInputSquare(p plannedInput, sums, weights []float32, width, height int, minX, minY, scale, dropSize float64, useERRWeighting bool, crMask BitMask, pixels []float32, trimX, trimY int) {
	data := p.input.HDU.Data
	half := normalizedDropSize(dropSize) / 2.0
	for y := trimY; y < data.Height-trimY; y++ {
		row := y * data.Width
		for x := trimX; x < data.Width-trimX; x++ {
			idx := row + x
			if crMask != nil && crMask.Get(idx) {
				continue
			}
			value := pixels[idx]
			if !isFinite32(value) {
				continue
			}
			refX, refY := p.mapPixel(float64(x), float64(y))
			outX := (refX - minX) * scale
			outY := (refY - minY) * scale
			drizzlePixelSquarePrepared(sums, weights, width, height, outX, outY, half, value, drizzlePixelWeight(p, idx, useERRWeighting))
		}
	}
}

func drizzlePlannedInputTurbo(p plannedInput, sums, weights []float32, width, height int, minX, minY, scale, dropSize float64, useERRWeighting bool, crMask BitMask, pixels []float32, trimX, trimY int) {
	data := p.input.HDU.Data
	half := normalizedDropSize(dropSize) / 2.0
	for y := trimY; y < data.Height-trimY; y++ {
		row := y * data.Width
		for x := trimX; x < data.Width-trimX; x++ {
			idx := row + x
			if crMask != nil && crMask.Get(idx) {
				continue
			}
			value := pixels[idx]
			if !isFinite32(value) {
				continue
			}
			refX, refY := p.mapPixel(float64(x), float64(y))
			outX := (refX - minX) * scale
			outY := (refY - minY) * scale
			drizzlePixelTurboPrepared(sums, weights, width, height, outX, outY, half, value, drizzlePixelWeight(p, idx, useERRWeighting))
		}
	}
}

func drizzlePlannedInputGaussian(p plannedInput, sums, weights []float32, width, height int, minX, minY, scale, dropSize float64, useERRWeighting bool, crMask BitMask, pixels []float32, trimX, trimY int) {
	data := p.input.HDU.Data
	sigma := normalizedDropSize(dropSize) / (2 * math.Sqrt(2*math.Log(2)))
	params := gaussianParams{twoSigSq: 2 * sigma * sigma, radius: 3 * sigma}
	for y := trimY; y < data.Height-trimY; y++ {
		row := y * data.Width
		for x := trimX; x < data.Width-trimX; x++ {
			idx := row + x
			if crMask != nil && crMask.Get(idx) {
				continue
			}
			value := pixels[idx]
			if !isFinite32(value) {
				continue
			}
			refX, refY := p.mapPixel(float64(x), float64(y))
			outX := (refX - minX) * scale
			outY := (refY - minY) * scale
			drizzlePixelGaussianPrepared(sums, weights, width, height, outX, outY, params, value, drizzlePixelWeight(p, idx, useERRWeighting))
		}
	}
}

func drizzlePlannedInputTophat(p plannedInput, sums, weights []float32, width, height int, minX, minY, scale, dropSize float64, useERRWeighting bool, crMask BitMask, pixels []float32, trimX, trimY int) {
	data := p.input.HDU.Data
	radius := normalizedDropSize(dropSize) / 2.0
	params := tophatParams{radius: radius, radSq: radius * radius}
	for y := trimY; y < data.Height-trimY; y++ {
		row := y * data.Width
		for x := trimX; x < data.Width-trimX; x++ {
			idx := row + x
			if crMask != nil && crMask.Get(idx) {
				continue
			}
			value := pixels[idx]
			if !isFinite32(value) {
				continue
			}
			refX, refY := p.mapPixel(float64(x), float64(y))
			outX := (refX - minX) * scale
			outY := (refY - minY) * scale
			drizzlePixelTophatPrepared(sums, weights, width, height, outX, outY, params, value, drizzlePixelWeight(p, idx, useERRWeighting))
		}
	}
}

func drizzlePlannedInputLanczos(p plannedInput, sums, weights []float32, width, height int, minX, minY, scale float64, n int, useERRWeighting bool, crMask BitMask, pixels []float32, trimX, trimY int) {
	data := p.input.HDU.Data
	for y := trimY; y < data.Height-trimY; y++ {
		row := y * data.Width
		for x := trimX; x < data.Width-trimX; x++ {
			idx := row + x
			if crMask != nil && crMask.Get(idx) {
				continue
			}
			value := pixels[idx]
			if !isFinite32(value) {
				continue
			}
			refX, refY := p.mapPixel(float64(x), float64(y))
			outX := (refX - minX) * scale
			outY := (refY - minY) * scale
			drizzlePixelLanczosPrepared(sums, weights, width, height, outX, outY, value, n, drizzlePixelWeight(p, idx, useERRWeighting))
		}
	}
}

func normalizedDropSize(dropSize float64) float64 {
	if dropSize <= 0 {
		return 1
	}
	return dropSize
}

func clampKernelBounds(x0, x1, y0, y1, width, height int) (int, int, int, int, bool) {
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}
	if x1 >= width {
		x1 = width - 1
	}
	if y1 >= height {
		y1 = height - 1
	}
	return x0, x1, y0, y1, x0 <= x1 && y0 <= y1
}

// drizzlePixelPoint deposits flux into the single nearest output pixel.
func drizzlePixelPoint(sums, weights []float32, width, height int, cx, cy float64, value float32, pixelWeight float32) {
	x := int(math.Round(cx))
	y := int(math.Round(cy))
	if x < 0 || x >= width || y < 0 || y >= height {
		return
	}
	idx := y*width + x
	sums[idx] += value * pixelWeight
	weights[idx] += pixelWeight
}

// drizzlePixelSquarePrepared is the classic drizzle box-overlap kernel with
// precomputed half drop size.
func drizzlePixelSquarePrepared(sums, weights []float32, width, height int, cx, cy, half float64, value float32, pixelWeight float32) {
	if width <= 0 || height <= 0 {
		return
	}

	left := cx - half
	right := cx + half
	top := cy - half
	bottom := cy + half

	ix0 := int(math.Floor(left-0.5)) + 1
	ix1 := int(math.Ceil(right+0.5)) - 1
	iy0 := int(math.Floor(top-0.5)) + 1
	iy1 := int(math.Ceil(bottom+0.5)) - 1

	var ok bool
	ix0, ix1, iy0, iy1, ok = clampKernelBounds(ix0, ix1, iy0, iy1, width, height)
	if !ok {
		return
	}

	for y := iy0; y <= iy1; y++ {
		cellTop := float64(y) - 0.5
		cellBottom := float64(y) + 0.5
		overlapY := math.Min(bottom, cellBottom) - math.Max(top, cellTop)
		if overlapY <= 0 {
			continue
		}
		row := y * width
		for x := ix0; x <= ix1; x++ {
			cellLeft := float64(x) - 0.5
			cellRight := float64(x) + 0.5
			overlapX := math.Min(right, cellRight) - math.Max(left, cellLeft)
			if overlapX <= 0 {
				continue
			}
			w := float32(overlapX*overlapY) * pixelWeight
			idx := row + x
			sums[idx] += value * w
			weights[idx] += w
		}
	}
}

// drizzlePixelTurboPrepared is a fast axis-aligned approximation: it accumulates
// into every output pixel whose center falls within the drop footprint with
// equal weight, skipping fractional-edge overlap computation.
func drizzlePixelTurboPrepared(sums, weights []float32, width, height int, cx, cy, half float64, value float32, pixelWeight float32) {
	x0 := int(math.Ceil(cx - half))
	x1 := int(math.Floor(cx + half))
	y0 := int(math.Ceil(cy - half))
	y1 := int(math.Floor(cy + half))
	if x0 > x1 || y0 > y1 {
		// Drop is smaller than one pixel: fall back to nearest.
		drizzlePixelPoint(sums, weights, width, height, cx, cy, value, pixelWeight)
		return
	}
	var ok bool
	x0, x1, y0, y1, ok = clampKernelBounds(x0, x1, y0, y1, width, height)
	if !ok {
		return
	}
	for y := y0; y <= y1; y++ {
		row := y * width
		for x := x0; x <= x1; x++ {
			idx := row + x
			sums[idx] += value * pixelWeight
			weights[idx] += pixelWeight
		}
	}
}

type gaussianParams struct {
	twoSigSq float64
	radius   float64
}

// drizzlePixelGaussianPrepared spreads flux with a Gaussian footprint.
func drizzlePixelGaussianPrepared(sums, weights []float32, width, height int, cx, cy float64, params gaussianParams, value float32, pixelWeight float32) {
	x0 := int(math.Floor(cx - params.radius))
	x1 := int(math.Ceil(cx + params.radius))
	y0 := int(math.Floor(cy - params.radius))
	y1 := int(math.Ceil(cy + params.radius))
	var ok bool
	x0, x1, y0, y1, ok = clampKernelBounds(x0, x1, y0, y1, width, height)
	if !ok {
		return
	}
	for y := y0; y <= y1; y++ {
		dy := float64(y) - cy
		row := y * width
		for x := x0; x <= x1; x++ {
			dx := float64(x) - cx
			gw := float32(math.Exp(-(dx*dx + dy*dy) / params.twoSigSq))
			if gw < 1e-6 {
				continue
			}
			w := gw * pixelWeight
			idx := row + x
			sums[idx] += value * w
			weights[idx] += w
		}
	}
}

type tophatParams struct {
	radius float64
	radSq  float64
}

// drizzlePixelTophatPrepared spreads flux uniformly within a circular aperture.
func drizzlePixelTophatPrepared(sums, weights []float32, width, height int, cx, cy float64, params tophatParams, value float32, pixelWeight float32) {
	x0 := int(math.Floor(cx - params.radius))
	x1 := int(math.Ceil(cx + params.radius))
	y0 := int(math.Floor(cy - params.radius))
	y1 := int(math.Ceil(cy + params.radius))
	var ok bool
	x0, x1, y0, y1, ok = clampKernelBounds(x0, x1, y0, y1, width, height)
	if !ok {
		return
	}
	for y := y0; y <= y1; y++ {
		dy := float64(y) - cy
		row := y * width
		for x := x0; x <= x1; x++ {
			dx := float64(x) - cx
			if dx*dx+dy*dy > params.radSq {
				continue
			}
			idx := row + x
			sums[idx] += value * pixelWeight
			weights[idx] += pixelWeight
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

// drizzlePixelLanczosPrepared uses a separable Lanczos-n resampling kernel.
// It accumulates signed kernel weights. Using abs(weight) here changes the
// interpolation normalization and can damp/alter sharp features.
func drizzlePixelLanczosPrepared(sums, weights []float32, width, height int, cx, cy float64, value float32, n int, pixelWeight float32) {
	x0 := int(math.Floor(cx)) - n + 1
	x1 := int(math.Floor(cx)) + n
	y0 := int(math.Floor(cy)) - n + 1
	y1 := int(math.Floor(cy)) + n
	var ok bool
	x0, x1, y0, y1, ok = clampKernelBounds(x0, x1, y0, y1, width, height)
	if !ok {
		return
	}
	for y := y0; y <= y1; y++ {
		wy := lanczos(float64(y)-cy, n)
		if wy == 0 {
			continue
		}
		row := y * width
		for x := x0; x <= x1; x++ {
			wx := lanczos(float64(x)-cx, n)
			lw := float32(wx * wy)
			if lw == 0 {
				continue
			}
			w := lw * pixelWeight
			idx := row + x
			sums[idx] += value * w
			weights[idx] += w
		}
	}
}

// drizzlePixelKernel is kept as a compatibility wrapper for other files in the
// mosaic package. The main drizzle loops dispatch by kernel once per input
// instead of calling this per pixel.
func drizzlePixelKernel(sums, weights []float32, width, height int, cx, cy, dropSize float64, value float32, kernel DrizzleKernel, pixelWeight float32) {
	switch kernel {
	case KernelPoint:
		drizzlePixelPoint(sums, weights, width, height, cx, cy, value, pixelWeight)
	case KernelTurbo:
		drizzlePixelTurbo(sums, weights, width, height, cx, cy, dropSize, value, pixelWeight)
	case KernelGaussian:
		drizzlePixelGaussian(sums, weights, width, height, cx, cy, dropSize, value, pixelWeight)
	case KernelTophat:
		drizzlePixelTophat(sums, weights, width, height, cx, cy, dropSize, value, pixelWeight)
	case KernelLanczos2:
		drizzlePixelLanczos(sums, weights, width, height, cx, cy, value, 2, pixelWeight)
	case KernelLanczos3:
		drizzlePixelLanczos(sums, weights, width, height, cx, cy, value, 3, pixelWeight)
	default:
		drizzlePixelSquare(sums, weights, width, height, cx, cy, dropSize, value, pixelWeight)
	}
}

// drizzlePixelSquare is the classic drizzle box-overlap kernel.
func drizzlePixelSquare(sums, weights []float32, width, height int, cx, cy, dropSize float64, value float32, pixelWeight float32) {
	drizzlePixelSquarePrepared(sums, weights, width, height, cx, cy, normalizedDropSize(dropSize)/2.0, value, pixelWeight)
}

func drizzlePixelTurbo(sums, weights []float32, width, height int, cx, cy, dropSize float64, value float32, pixelWeight float32) {
	drizzlePixelTurboPrepared(sums, weights, width, height, cx, cy, normalizedDropSize(dropSize)/2.0, value, pixelWeight)
}

// drizzlePixelGaussian spreads flux with a Gaussian footprint.
// sigma = dropSize / (2 * sqrt(2*ln2)) so that FWHM == dropSize.
func drizzlePixelGaussian(sums, weights []float32, width, height int, cx, cy, dropSize float64, value float32, pixelWeight float32) {
	sigma := normalizedDropSize(dropSize) / (2 * math.Sqrt(2*math.Log(2)))
	drizzlePixelGaussianPrepared(sums, weights, width, height, cx, cy, gaussianParams{twoSigSq: 2 * sigma * sigma, radius: 3 * sigma}, value, pixelWeight)
}

// drizzlePixelTophat spreads flux uniformly within a circular aperture of
// radius dropSize/2.
func drizzlePixelTophat(sums, weights []float32, width, height int, cx, cy, dropSize float64, value float32, pixelWeight float32) {
	radius := normalizedDropSize(dropSize) / 2.0
	drizzlePixelTophatPrepared(sums, weights, width, height, cx, cy, tophatParams{radius: radius, radSq: radius * radius}, value, pixelWeight)
}

func drizzlePixelLanczos(sums, weights []float32, width, height int, cx, cy float64, value float32, n int, pixelWeight float32) {
	drizzlePixelLanczosPrepared(sums, weights, width, height, cx, cy, value, n, pixelWeight)
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
		if input.SkySubtracted {
			line += fmt.Sprintf(" | sky=%+.4f", input.SkyValue)
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

func drizzleBuildStatus(skysubApplied, cleaned bool) string {
	if skysubApplied && cleaned {
		return "sky-subtracted, cleaned, and drizzled"
	}
	if skysubApplied {
		return "sky-subtracted and drizzled"
	}
	if cleaned {
		return "cleaned and drizzled"
	}
	return "aligned and drizzled"
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

// firstDataInput returns the first non-excluded input that is not marked
// ReferenceOnly, falling back to inputs[0] if all inputs are reference-only or
// excluded.
func firstDataInput(inputs []Input) Input {
	for _, inp := range inputs {
		if !inp.Excluded && !inp.ReferenceOnly {
			return inp
		}
	}
	return inputs[0]
}

// wcsReferenceInput returns the input whose WCS should anchor the output header.
// If the first input is a non-excluded ReferenceOnly image it defines the output
// coordinate frame, so use it. Otherwise fall back to the first data-bearing
// non-excluded input.
func wcsReferenceInput(inputs []Input) Input {
	if len(inputs) > 0 && !inputs[0].Excluded && inputs[0].ReferenceOnly {
		return inputs[0]
	}
	return firstDataInput(inputs)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
