package mosaic

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	"gofitsv3/internal/debuglog"
	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/instrument"
	"gofitsv3/internal/processing"
)

// weightEpsilon is used when normalizing signed-kernel accumulators such as Lanczos.
// Very small denominators are treated as uncovered to avoid edge blow-ups.
const weightEpsilon float32 = 1e-12

type Input struct {
	Path               string
	SCIExt             int
	PrimaryHeader      fitsio.Header
	HDU                fitsio.HDU
	ExposureTime       float64
	DateObs            string
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
	// SourcePath is the original multi-chip file this combined working input was
	// generated from. Empty for ordinary inputs. Used for the display label,
	// dedupe, project persistence, and locating the working/ directory.
	SourcePath string
	// WeightPixels holds the per-pixel drizzle weight from a combined working
	// file's WHT extension, same dimensions as HDU.Data. When present it is used
	// verbatim as the WeightERR pixel weight in place of the ERR-derived inverse
	// variance. Nil for ordinary inputs.
	WeightPixels []float32
	// ReferenceOnly marks this input as a WCS anchor only. It participates in
	// alignment and coordinate-system setup but its pixels are not drizzled into
	// the output. Use this to align a new filter to a previously drizzled baseline.
	ReferenceOnly bool
	// BUnit is the BUNIT header value (e.g. "ELECTRONS", "ELECTRONS/S"), used to
	// decide automatically whether the pixels are total counts that must be
	// divided by exposure time. Empty when the header has no BUNIT.
	BUnit string
	// NormalizeExposure, when true, causes prepareFramePixels to convert this
	// frame's SCI (and ERR) pixels to a rate (per-second) by multiplying by
	// ExposureScale before any drizzle weighting. This is independent of
	// Options.WeightingMode. Off by default to preserve existing behavior.
	NormalizeExposure bool
	// ExposureScale is the per-frame normalization factor (1/EXPTIME) applied when
	// NormalizeExposure is set. Zero or non-finite disables normalization for the
	// frame even when NormalizeExposure is true.
	ExposureScale float64
}

func InputKey(input Input) string {
	if input.SCIExt > 0 {
		return fmt.Sprintf("%s[sci,%d]", input.Path, input.SCIExt)
	}
	return input.Path
}

func InputLabel(input Input) string {
	if input.SourcePath != "" {
		name := filepath.Base(input.SourcePath)
		if name == "" {
			name = input.SourcePath
		}
		return name + " [comb]"
	}
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
	// CRMethodDrizzle uses the AstroDrizzle-style multi-frame model/blot/flag
	// pipeline.  Requires ≥ 2 aligned exposures.
	CRMethodDrizzle
)

// BitMask stores one boolean pixel mask bit per pixel. This is used for cosmic
// ray masks in the final drizzle pass to avoid retaining one byte per pixel per
// frame.
//
// NOTE: processing.BuildCRMasksFromModel currently returns [][]bool. This file
// compresses those masks immediately after that call. To reduce peak memory too,
// change that function to produce BitMask-compatible masks directly or tile its work.
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

func isFinite64(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
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

// AlignmentStreamsPixels reports whether the given alignment mode runs entirely
// on streamed star catalogs (the TweakReg modes) and therefore does not require
// every input's pixel arrays to be resident in memory. The legacy warp-based
// modes (GeneralAffine, RScale) still warp full images and need resident pixels,
// so callers should preload them before alignment.
func AlignmentStreamsPixels(mode AlignmentMode) bool {
	switch normalizeAlignmentMode(mode) {
	case AlignmentModeTweakRegRScale, AlignmentModeTweakRegGeneral:
		return true
	default:
		return false
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
	// LockToReferenceFrame pins the output canvas to a ReferenceOnly baseline at
	// inputs[0]: the output dimensions, origin, and plate scale become exactly the
	// baseline's, so every channel drizzled against the same baseline is
	// pixel-identical (same size, orientation, and scale) for compositing.
	// When set, Scale is forced to 1 and FinalScale is ignored.
	LockToReferenceFrame bool
	CRMethod             CRMethod
	PixFrac              float64
	// SepKernel is the kernel used during the per-frame drizzle step.
	// Defaults to KernelSquare when zero.
	SepKernel DrizzleKernel
	// FinalKernel is reserved for a future two-pass pipeline's final combination
	// step. Currently unused; SepKernel governs all drizzling.
	FinalKernel DrizzleKernel
	// WeightingMode selects how each valid input pixel is weighted.
	WeightingMode WeightingMode
	// SurfaceBrightnessNorm converts each input from source-pixel count rate to
	// reference-grid pixel-area count rate before sky subtraction and drizzle.
	// This is useful for mixed-scale instruments such as WFPC2 PC+WF.
	SurfaceBrightnessNorm bool
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
	// Progress, when non-nil, is invoked at each major phase of Build with a
	// human-readable stage name and progress counters. total is 0 when a phase
	// has no meaningful unit count (the UI should show indeterminate progress).
	// It may be called from the Build goroutine; the callback must be safe to
	// invoke off the UI thread.
	Progress func(stage string, done, total int)
	// Ctx, when non-nil, allows Build to be cancelled. Build checks Ctx.Err()
	// between frames and returns ErrCancelled if the context is done.
	Ctx context.Context
	// DebugOutputDir, when not empty, causes the drizzle process to output an individual
	// FITS file for each input chip, exactly matching the footprint of the final combined mosaic.
	DebugOutputDir string
	// FrameLoader, when non-nil, supplies a frame's SCI (and optional ERR) pixels
	// on demand instead of reading them from disk. Build uses it only for inputs
	// whose in-memory pixels are nil, so callers can stream large mosaics without
	// holding every input array at once. When nil, such inputs are reloaded from
	// their FITS file via LoadInputsFromPath. Inputs that already carry pixels are
	// used directly regardless of this field.
	FrameLoader func(in Input) (sci []float32, errPix []float32, err error)
}

// ErrCancelled is returned by Build (or AlignInputsByStarsWithMode) when its
// context is cancelled.
var ErrCancelled = errors.New("operation cancelled")

// AlignProgress carries optional progress reporting and cancellation for the
// star-alignment routines. The zero value disables both.
type AlignProgress struct {
	// Progress, when non-nil, is invoked as each input finishes aligning.
	Progress func(done, total int)
	// Ctx, when non-nil, allows the alignment to be cancelled. Goroutines that
	// have not yet started are skipped and ErrCancelled is returned.
	Ctx context.Context
}

func (p AlignProgress) report(done, total int) {
	if p.Progress != nil {
		p.Progress(done, total)
	}
}

func (p AlignProgress) cancelled() bool {
	return p.Ctx != nil && p.Ctx.Err() != nil
}

// reportProgress invokes the Progress callback if one is set.
func (o Options) reportProgress(stage string, done, total int) {
	if o.Progress != nil {
		o.Progress(stage, done, total)
	}
}

// cancelled returns ErrCancelled if the context is set and done, else nil.
func (o Options) cancelled() error {
	if o.Ctx != nil && o.Ctx.Err() != nil {
		return ErrCancelled
	}
	return nil
}

// WeightingMode selects the drizzle weighting scheme.
type WeightingMode int

const (
	// WeightUniform gives each valid input pixel equal weight aside from
	// geometric overlap.
	WeightUniform WeightingMode = iota
	// WeightExposure normalizes each input by exposure time and weights it by
	// exposure time, matching classic drizzle EXP-style weighting.
	WeightExposure
	// WeightERR normalizes each input by exposure time and weights it by the
	// inverse variance derived from the ERR extension, converted to rate units.
	WeightERR
)

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
	input            Input
	sourceToRef      processing.AffineTransform // affine approximation, used for CR detection
	mapper           *processing.WCSMapper      // per-pixel WCS projection, used for drizzle
	sourcePixelScale float64                    // source pixel size in reference-pixel units
	statusIndex      int
	// cancel, when non-nil, is polled at row intervals inside the per-frame
	// drizzle kernels so a single large frame can be interrupted partway through
	// rather than only between frames. Returns true once the build is cancelled.
	cancel func() bool
}

// cancelled reports whether this frame's drizzle should abort early.
func (p *plannedInput) cancelled() bool {
	return p.cancel != nil && p.cancel()
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

func (p *plannedInput) mapOutputPixel(x, y, originX, originY, scale float64) (float64, float64) {
	refX, refY := p.mapPixel(x, y)
	return (refX - originX) * scale, (refY - originY) * scale
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

// NativePlateScaleArcsec returns an input's native plate scale in arcseconds per
// pixel. It prefers the WCS in the input's SCI header (CD matrix, then CDELT1)
// and falls back to the nominal per-detector scale from the instrument table.
// ok is false only when neither source yields a positive scale. Exported for the
// UI's "match finest input" scale preset.
func NativePlateScaleArcsec(in Input) (float64, bool) {
	if ps, ok := nativePlateScaleArcsec(in.HDU.Header); ok && ps > 0 {
		return ps, true
	}
	if info, ok := instrument.FromHeader(in.HDU.Header); ok && info.PixelScale > 0 {
		return info.PixelScale, true
	}
	return 0, false
}

// referenceFrameDims returns the pixel dimensions of a baseline frame, preferring
// the loaded image dimensions and falling back to the NAXIS1/NAXIS2 header cards
// (which stay valid after the pixel buffer is freed for streaming). Returns
// (0, 0) when neither source is available.
func referenceFrameDims(in Input) (int, int) {
	if in.HDU.Data.Width > 0 && in.HDU.Data.Height > 0 {
		return in.HDU.Data.Width, in.HDU.Data.Height
	}
	w, okW := fitsio.HeaderFloat(in.HDU.Header, "NAXIS1")
	h, okH := fitsio.HeaderFloat(in.HDU.Header, "NAXIS2")
	if okW && okH {
		return int(w), int(h)
	}
	return 0, 0
}

func Build(inputs []Input, options Options) (*Result, error) {
	debuglog.Log(fmt.Sprintf("Build: starting drizzle, %d inputs", len(inputs)))
	defer debuglog.Log("Build: finished")
	if len(inputs) == 0 {
		return nil, fmt.Errorf("no FITS inputs selected")
	}
	logMemStats("before planning")

	// Lock the output grid to a ReferenceOnly baseline at inputs[0]: the baseline
	// defines the exact output canvas (dimensions, origin, plate scale), so every
	// channel drizzled against the same baseline is pixel-identical. This forces
	// Scale = 1 (the baseline already encodes the target plate scale) and pins the
	// canvas bounds below, overriding FinalScale.
	lockFrame := options.LockToReferenceFrame && !inputs[0].Excluded && inputs[0].ReferenceOnly
	var lockW, lockH int
	if lockFrame {
		lockW, lockH = referenceFrameDims(inputs[0])
		if lockW < 1 || lockH < 1 {
			return nil, fmt.Errorf("lock to reference frame: baseline %s has no readable dimensions", InputKey(inputs[0]))
		}
		options.Scale = 1
	} else if options.FinalScale > 0 {
		// Resolve FinalScale (arcsec/pixel) → internal Scale multiplier.
		// Use the same WCS anchor that will be used for the output header, so a
		// ReferenceOnly baseline and same-scale filter runs produce matching grids.
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

	if err := options.cancelled(); err != nil {
		return nil, err
	}
	options.reportProgress("Planning inputs", 0, 0)
	debuglog.Log("Build: calling planInputs")
	planned, statuses, minX, minY, maxX, maxY, err := planInputs(inputs, options.Scale)
	if err != nil {
		return nil, err
	}

	// When locking to the baseline frame, discard the data-derived footprint and
	// pin the canvas to the baseline's exact pixel grid (origin 0,0 at Scale 1),
	// so the output header WCS equals the baseline's and dimensions match exactly.
	if lockFrame {
		minX, minY = 0, 0
		maxX, maxY = float64(lockW-1), float64(lockH-1)
	}

	// Let the heavy per-frame drizzle kernels (and the streamed CR median model)
	// poll for cancellation mid-frame so a single large frame doesn't have to
	// finish before the build aborts.
	cancelFn := func() bool { return options.cancelled() != nil }
	for i := range planned {
		planned[i].cancel = cancelFn
	}

	width := int(math.Ceil((maxX - minX + 1) * options.Scale))
	height := int(math.Ceil((maxY - minY + 1) * options.Scale))
	debuglog.Log(fmt.Sprintf("Build: planInputs done, %d planned inputs, output canvas %dx%d", len(planned), width, height))
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	// A bad WCS solution or a misaligned frame's manual transform can push
	// minX/minY/maxX/maxY far outside the real mosaic footprint, producing a
	// canvas so large the output allocation crashes the process with an OOM
	// panic instead of a reportable error. Reject implausible canvases here,
	// at the single chokepoint where width/height are derived from the
	// per-frame bounds.
	const maxCanvasPixels = 500_000_000 // ~2GB per float32 buffer
	if int64(width)*int64(height) > maxCanvasPixels {
		return nil, fmt.Errorf("output canvas too large (%dx%d = %d pixels); check for a misaligned or badly-WCS'd input frame", width, height, int64(width)*int64(height))
	}

	includedCount := 0

	effectiveCR := options.CRMethod

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
	// Surface-brightness normalization is now applied per frame inside
	// prepareFramePixels (streamed), so no whole-slice copy is made here.

	if err := options.cancelled(); err != nil {
		return nil, err
	}
	logMemStats("after planning")
	options.reportProgress("Sky subtraction", 0, 0)
	debuglog.Log("Build: calling planSkysub")
	skyOffset, skyPlanes, skyApplied, skyValues, err := planSkysub(planned, options)
	if err != nil {
		return nil, err
	}
	debuglog.Log("Build: planSkysub done")
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

	// Cosmic-ray rejection (streamed, temp-file backed): the separate-drizzle
	// products are written to disk and median-combined band-by-band so we never
	// hold one output-size image per frame in memory at once.
	var crMasks []BitMask
	if effectiveCR == CRMethodDrizzle && len(dataPlanned) > 1 {
		logMemStats("CR start")
		debuglog.Log(fmt.Sprintf("Build: starting streamed CR drizzle, %d frames", len(dataPlanned)))
		crMasks, err = buildCRMasksDrizzle(planned, dataPlanned, skyOffset, skyPlanes, options, width, height, minX, minY, options.Scale)
		if err != nil {
			return nil, err
		}
		logMemStats("CR done")
	}

	// Final drizzle pass: accumulate all frames into the output using FinalKernel.
	debuglog.Log(fmt.Sprintf("Build: starting final drizzle pass, %d planned inputs", len(planned)))
	sums := make([]float32, width*height)
	weights := make([]float32, width*height)
	finalKernel := options.FinalKernel
	finalSlot := 0

	var debugBaseHeader fitsio.Header
	if options.DebugOutputDir != "" {
		if err := os.MkdirAll(options.DebugOutputDir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create debug output directory: %w", err)
		}
		debugBaseHeader = buildOutputHeader(wcsReferenceInput(inputs), firstDataInput(inputs), width, height, minX, minY, options.Scale, 1)
	}

	for i := range planned {
		if planned[i].input.ReferenceOnly {
			continue
		}
		if err := options.cancelled(); err != nil {
			return nil, err
		}
		options.reportProgress("Drizzling", finalSlot, len(dataPlanned))
		finalSlot++
		debuglog.Log(fmt.Sprintf("Build: final drizzle frame %d (%s)", finalSlot, InputKey(planned[i].input)))
		pixels, errPix, whtPix, perr := prepareFramePixels(planned[i], options, skyOffset[i], skyPlanes[i])
		if perr != nil {
			return nil, fmt.Errorf("load frame %s: %w", InputKey(planned[i].input), perr)
		}
		// drizzlePlannedInput reads weights from planned[i].input.WeightPixels
		// (combined working frames) or ERRPixels (ordinary frames).
		planned[i].input.ERRPixels = errPix
		planned[i].input.WeightPixels = whtPix
		var crMask BitMask
		cleaned := false

		if effectiveCR != CRMethodNone {
			slot := crMaskIndex[i]
			if slot >= 0 && crMasks != nil {
				crMask = crMasks[slot]
				cleaned = len(crMask) > 0
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

		trimX := effectiveEdgeTrimForInput(planned[i], planned[i].input.HDU.Data.Width, options.Scale)
		trimY := effectiveEdgeTrimForInput(planned[i], planned[i].input.HDU.Data.Height, options.Scale)
		debuglog.Log(fmt.Sprintf("Build: frame %d input %dx%d kernel=%d trimX=%d trimY=%d", finalSlot,
			planned[i].input.HDU.Data.Width, planned[i].input.HDU.Data.Height, int(finalKernel), trimX, trimY))
		dropSize := inputDropSize(planned[i], options.Scale, options.PixFrac)
		drizzlePlannedInput(planned[i], sums, weights, width, height, minX, minY,
			options.Scale, dropSize, finalKernel, options.WeightingMode, crMask, pixels, trimX, trimY)
		debuglog.Log(fmt.Sprintf("Build: frame %d done", finalSlot))

		if options.DebugOutputDir != "" {
			dbgSums := make([]float32, width*height)
			dbgWeights := make([]float32, width*height)
			drizzlePlannedInput(planned[i], dbgSums, dbgWeights, width, height, minX, minY,
				options.Scale, dropSize, finalKernel, options.WeightingMode, crMask, pixels, trimX, trimY)
			normalizeAccumulatedImage(dbgSums, dbgWeights)

			safeName := strings.ReplaceAll(InputLabel(planned[i].input), "[", "_")
			safeName = strings.ReplaceAll(safeName, "]", "_")
			safeName = strings.ReplaceAll(safeName, ",", "_")
			debugPath := filepath.Join(options.DebugOutputDir, fmt.Sprintf("debug_%s.fits", safeName))

			err := fitsio.WriteFloat32Image(debugPath, debugBaseHeader, fitsio.ImageData{Pixels: dbgSums, Width: width, Height: height})
			if err != nil {
				debuglog.Log(fmt.Sprintf("Build: failed to write debug image %s: %v", debugPath, err))
			} else {
				debuglog.Log(fmt.Sprintf("Build: wrote debug image %s", debugPath))
			}
		}

		// Release this frame's pixels before loading the next one so peak memory
		// stays at roughly one input frame plus the output accumulators.
		pixels = nil
		planned[i].input.ERRPixels = nil
		planned[i].input.WeightPixels = nil
		if finalSlot%8 == 0 {
			logMemStats(fmt.Sprintf("drizzled %d/%d frames", finalSlot, len(dataPlanned)))
		}
	}

	options.reportProgress("Finalizing", len(dataPlanned), len(dataPlanned))
	debuglog.Log("Build: normalizing accumulated image")
	normalizeAccumulatedImage(sums, weights)
	logMemStats("finalized")
	debuglog.Log("Build: normalization done, computing footprints")

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
			trimX := float64(effectiveEdgeTrimForInput(p, p.input.HDU.Data.Width, options.Scale))
			trimY := float64(effectiveEdgeTrimForInput(p, p.input.HDU.Data.Height, options.Scale))
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
		OutputHeader:        buildOutputHeader(wcsReferenceInput(inputs), firstDataInput(inputs), width, height, minX, minY, options.Scale, includedCount),
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

// propagateSameExposureAlignment gives any still-unaligned chip the alignment
// solution of an aligned sibling chip from the same exposure (same file Path,
// different SCIExt). The chips of one exposure are rigid on the focal plane and
// share a single pointing residual, but adjacent chips barely overlap each other
// so they cannot be star-matched directly — without this, a chip that doesn't
// overlap the reference is either left unrefined or, worse, pushed by a false
// cross-chip match. It returns the number of chips filled in.
func propagateSameExposureAlignment(inputs []Input, results []StarAlignmentResult, aligned []bool) int {
	filled := 0
	for i := range inputs {
		if aligned[i] || inputs[i].Excluded {
			continue
		}
		for j := range inputs {
			if j == i || !aligned[j] || inputs[j].Excluded {
				continue
			}
			if inputs[j].Path != inputs[i].Path {
				continue
			}
			results[i] = StarAlignmentResult{
				OffsetX:            results[j].OffsetX,
				OffsetY:            results[j].OffsetY,
				ManualTransform:    results[j].ManualTransform,
				HasManualTransform: results[j].HasManualTransform,
				Applied:            true,
				MatchedStars:       results[j].MatchedStars,
				RMS:                results[j].RMS,
				MaxError:           results[j].MaxError,
			}
			aligned[i] = true
			filled++
			break
		}
	}
	return filled
}

// framesMayOverlap reports whether two inputs' footprints could share any
// pixels, using a cheap WCS center-distance test (bounding-circle criterion, no
// image warp). It is deliberately conservative — it never rules out a genuine
// overlap, only skips pairs clearly too far apart — so it is safe to gate the
// expensive warp+star-match chain fallback on it.
func framesMayOverlap(a, b Input) bool {
	dist, err := processing.CenterDistInRefPixels(
		a.HDU.Header, a.HDU.Data.Width, a.HDU.Data.Height,
		b.HDU.Header, b.HDU.Data.Width, b.HDU.Data.Height,
	)
	if err != nil {
		return true // can't determine geometry → don't skip
	}
	diagA := math.Hypot(float64(a.HDU.Data.Width), float64(a.HDU.Data.Height))
	diagB := math.Hypot(float64(b.HDU.Data.Width), float64(b.HDU.Data.Height))
	return dist <= (diagA+diagB)/2
}

func AlignInputsByStarsWithMode(inputs []Input, numRefs int, mode AlignmentMode, searchRadiusArcsec float64, progress ...AlignProgress) ([]StarAlignmentResult, error) {
	debuglog.Log("AlignInputsByStarsWithMode: starting")
	defer debuglog.Log("AlignInputsByStarsWithMode: finished")
	var prog AlignProgress
	if len(progress) > 0 {
		prog = progress[0]
	}
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
	lastErr := make([]string, len(inputs))
	ordered := sortedByDistFromRef(inputs)

	// For TweakReg modes, extract reference stars from each designated reference
	// image once up front.
	isTweakReg := mode == AlignmentModeTweakRegRScale || mode == AlignmentModeTweakRegGeneral
	fitgeom := "rscale"
	if mode == AlignmentModeTweakRegGeneral {
		fitgeom = "general"
	}

	// Build every input's star catalog up front, streaming pixels from disk so
	// the whole dataset never needs to be resident at once. All downstream
	// alignment (primary TweakReg fit, chain fallback, bundle adjustment) runs on
	// these catalogs; pixels are reloaded on demand only for the legacy warp modes
	// and the alignment debug hook.
	catalogs := extractStarCatalogsForAlignment(inputs)

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
		refCaches[0].stars = catalogs[0]
		debuglog.Log(fmt.Sprintf("AlignInputsByStarsWithMode: ref[0] has %d stars", len(refCaches[0].stars)))
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
			rc.stars = catalogs[r]
			debuglog.Log(fmt.Sprintf("AlignInputsByStarsWithMode: ref[%d] has %d stars", r, len(rc.stars)))
		}
		refCaches[r] = rc
	}

	// alignOneToAnyRef tries each designated reference in order and returns on
	// the first success.  For secondary references (r>0) the refinement is
	// composed back to inputs[0] pixel space.
	type alignOneResult struct {
		i          int
		refinement processing.AffineTransform
		stats      processing.AlignStats
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
				stats      processing.AlignStats
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
				if processing.AlignmentDebugHook != nil {
					// Debug visualization needs the actual pixels; reload the pair
					// on demand so the common (non-debug) path stays pixel-free.
					srcPix, _, _, _, srcErr := resolveFramePixels(inputs[i], Options{})
					refPix, _, _, _, refErr := resolveFramePixels(rc.input, Options{})
					if srcErr != nil || refErr != nil {
						err = fmt.Errorf("debug pixel reload: %v / %v", srcErr, refErr)
						break
					}
					refinement, stats, err = processing.EstimateTweakRegAlignmentWithRefStars(
						srcPix,
						inputs[i].HDU.Data.Width,
						inputs[i].HDU.Data.Height,
						mapper,
						refPix,
						rc.stars,
						rc.input.HDU.Data.Width,
						rc.input.HDU.Data.Height,
						rc.input.HDU.Header,
						searchRadiusArcsec,
						fitgeom,
					)
				} else {
					refinement, stats, err = processing.EstimateTweakRegAlignmentFromCatalogs(
						catalogs[i],
						mapper,
						rc.stars,
						rc.input.HDU.Data.Width,
						rc.input.HDU.Data.Height,
						rc.input.HDU.Header,
						searchRadiusArcsec,
						fitgeom,
					)
				}
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
			return alignOneResult{i: i, refinement: refinement, stats: stats, ok: true}
		}
		return alignOneResult{i: i, errMsg: lastErr[i]}
	}

	// getStars returns an input's pre-extracted catalog (built once, up front, via
	// streaming). The chain fallback matches against these catalogs — no image
	// warp and no repeated extraction.
	getStars := func(idx int) []processing.Star {
		return catalogs[idx]
	}

	// mapperCache caches each input's WCS mapper into inputs[0] pixel space.
	type mapperEntry struct {
		m  *processing.WCSMapper
		ok bool
	}
	mapperCache := make(map[int]mapperEntry)
	getMapper := func(idx int) (*processing.WCSMapper, bool) {
		if e, seen := mapperCache[idx]; seen {
			return e.m, e.ok
		}
		m, err := processing.NewWCSMapper(
			inputs[idx].HDU.Header, inputs[idx].D2IX, inputs[idx].D2IY,
			inputs[0].HDU.Header, inputs[0].D2IX, inputs[0].D2IY,
		)
		e := mapperEntry{m: m, ok: err == nil}
		mapperCache[idx] = e
		return e.m, e.ok
	}
	// projectRaw maps a frame's catalog into inputs[0] pixel space using only its
	// WCS placement (mapper + base offset), i.e. before any residual correction —
	// the "projected source" role for a residual fit.
	projectRaw := func(idx int) ([]processing.Star, bool) {
		m, ok := getMapper(idx)
		if !ok {
			return nil, false
		}
		src := getStars(idx)
		out := make([]processing.Star, len(src))
		for k, s := range src {
			rx, ry := m.MapPixel(s.X, s.Y)
			out[k] = processing.Star{X: rx + inputs[idx].OffsetX, Y: ry + inputs[idx].OffsetY, Flux: s.Flux}
		}
		return out, true
	}
	// projectCorrected maps an already-aligned frame's catalog into inputs[0] pixel
	// space using its FULL solution (mapper + offset + ManualTransform), exactly as
	// plannedInput.mapPixel does at render. Chaining off this (not the raw WCS
	// placement) propagates the intermediate's own residual into the new frame.
	projectCorrected := func(idx int) ([]processing.Star, bool) {
		m, ok := getMapper(idx)
		if !ok {
			return nil, false
		}
		src := getStars(idx)
		// Use the computed solution once it exists; otherwise fall back to the
		// frame's accepted placement (the path designated references take, since
		// their results entry is only filled at the very end).
		offX, offY := inputs[idx].OffsetX, inputs[idx].OffsetY
		mt, hasMT := inputs[idx].ManualTransform, inputs[idx].HasManualTransform
		if results[idx].Applied {
			offX, offY = results[idx].OffsetX, results[idx].OffsetY
			mt, hasMT = results[idx].ManualTransform, results[idx].HasManualTransform
		}
		out := make([]processing.Star, len(src))
		for k, s := range src {
			rx, ry := m.MapPixel(s.X, s.Y)
			rx += offX
			ry += offY
			if hasMT {
				rx, ry = processing.ApplyAffineTransform(mt, rx, ry)
			}
			out[k] = processing.Star{X: rx, Y: ry, Flux: s.Flux}
		}
		return out, true
	}

	refW := inputs[0].HDU.Data.Width
	refH := inputs[0].HDU.Data.Height
	chainSearchRadiusPx := 30.0
	if ps, ok := nativePlateScaleArcsec(inputs[0].HDU.Header); ok && ps > 0 {
		chainSearchRadiusPx = searchRadiusArcsec / ps
	}

	// --- Primary pass: align every non-reference frame directly to a designated
	// reference, concurrently. alignOneToRef tries all references internally, and
	// results are stored by index, so goroutine completion order cannot affect the
	// outcome.
	var toAlign []int
	for _, i := range ordered {
		if !aligned[i] && !inputs[i].Excluded {
			toAlign = append(toAlign, i)
		}
	}
	if len(toAlign) > 0 {
		prog.report(0, len(toAlign))
		ch := make(chan alignOneResult, len(toAlign))
		sem := make(chan struct{}, runtime.NumCPU())
		var wg sync.WaitGroup
		for _, i := range toAlign {
			if prog.cancelled() {
				break
			}
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
		if prog.cancelled() {
			return nil, ErrCancelled
		}
		done := 0
		for r := range ch {
			done++
			prog.report(done, len(toAlign))
			if r.ok {
				results[r.i] = StarAlignmentResult{
					OffsetX:            inputs[r.i].OffsetX,
					OffsetY:            inputs[r.i].OffsetY,
					ManualTransform:    r.refinement,
					HasManualTransform: true,
					Applied:            true,
					MatchedStars:       r.stats.MatchedStars,
					RMS:                r.stats.RMS,
					MaxError:           r.stats.MaxError,
				}
				aligned[r.i] = true
			} else {
				lastErr[r.i] = r.errMsg
			}
		}
	}

	// --- Chain fallback: frames that did not align directly to a reference are
	// aligned to an already-aligned intermediate. Each frame fits a FULL residual
	// (rscale/general, including rotation/scale — not the old translation-only
	// correction) against the intermediate's fully-corrected catalog, and chooses
	// the intermediate that corroborates with the most catalog stars (ties broken
	// by lower RMS, then lower index — all deterministic). Iterated to a fixed
	// point so a frame aligned in one pass can be an intermediate in the next.
	//
	// evaluated memoizes (frame, intermediate) pairs already fit-attempted: an
	// intermediate's solution is fixed once set, so a failed pair never succeeds
	// later, which keeps the total work bounded (≈ unaligned × aligned, pruned by
	// framesMayOverlap) instead of re-fitting every pass.
	evaluated := make(map[[2]int]bool)
	for progressed := true; progressed; {
		progressed = false
		for _, i := range ordered {
			if aligned[i] || inputs[i].Excluded {
				continue
			}
			if prog.cancelled() {
				return nil, ErrCancelled
			}
			srcProj, ok := projectRaw(i)
			if !ok {
				continue
			}
			bestJ := -1
			bestSupport := -1
			bestRMS := math.Inf(1)
			var bestT processing.AffineTransform
			var bestStats processing.AlignStats
			for _, j := range ordered {
				if j == i || !aligned[j] || !results[j].Applied || inputs[j].Excluded {
					continue
				}
				// Chips of the same exposure are rigid and barely overlap; never
				// star-match them — propagateSameExposureAlignment handles siblings.
				if inputs[i].Path == inputs[j].Path {
					continue
				}
				if evaluated[[2]int{i, j}] {
					continue
				}
				evaluated[[2]int{i, j}] = true
				if !framesMayOverlap(inputs[i], inputs[j]) {
					continue
				}
				intProj, ok := projectCorrected(j)
				if !ok {
					continue
				}
				t, stats, err := processing.FitCatalogResidual(srcProj, intProj, refW, refH, chainSearchRadiusPx, fitgeom)
				if err != nil {
					lastErr[i] = err.Error()
					continue
				}
				if stats.GlobalInliers > bestSupport ||
					(stats.GlobalInliers == bestSupport && stats.RMS < bestRMS) {
					bestJ = j
					bestSupport = stats.GlobalInliers
					bestRMS = stats.RMS
					bestT = t
					bestStats = stats
				}
			}
			if bestJ >= 0 {
				results[i] = StarAlignmentResult{
					OffsetX:            inputs[i].OffsetX,
					OffsetY:            inputs[i].OffsetY,
					ManualTransform:    bestT,
					HasManualTransform: true,
					Applied:            true,
					MatchedStars:       bestStats.MatchedStars,
					RMS:                bestStats.RMS,
					MaxError:           bestStats.MaxError,
				}
				aligned[i] = true
				progressed = true
				debuglog.Log(fmt.Sprintf("AlignInputsByStarsWithMode: chain-aligned input[%d] via intermediate[%d] (support=%d rms=%.2f)", i, bestJ, bestSupport, bestRMS))
			}
		}
	}

	// Global bundle adjustment: every frame so far was fit to the reference (or an
	// intermediate) independently, so overlapping non-reference frames can disagree
	// with each other even while each agrees with the reference. This simultaneously
	// minimizes the cross-frame star residual over all overlaps, holding the
	// references fixed. It runs before same-exposure propagation so rigid sibling
	// chips inherit the adjusted solution rather than being adjusted independently.
	// The adjustment is applied only when it strictly reduces the global residual,
	// so it can never make an alignment worse.
	{
		cats := make([][]processing.Star, len(inputs))
		fixed := make([]bool, len(inputs))
		anyAdjustable := false
		for i := range inputs {
			if inputs[i].Excluded || !aligned[i] {
				fixed[i] = true
				continue
			}
			if c, ok := projectCorrected(i); ok {
				cats[i] = c
			} else {
				fixed[i] = true
				continue
			}
			// References and frames without a fitted residual are held fixed; only
			// star-aligned non-reference frames may move.
			if i < numRefs || !results[i].Applied || !results[i].HasManualTransform {
				fixed[i] = true
			} else {
				anyAdjustable = true
			}
		}
		if anyAdjustable {
			mayOverlap := func(a, b int) bool {
				return inputs[a].Path != inputs[b].Path && framesMayOverlap(inputs[a], inputs[b])
			}
			if updates, ok := processing.GlobalBundleAdjust(cats, fixed, mayOverlap, fitgeom, refW, refH, 5); ok {
				adjusted := 0
				for i := range updates {
					if fixed[i] || updates[i] == processing.IdentityTransform() {
						continue
					}
					results[i].ManualTransform = processing.ComposeAffineTransforms(updates[i], results[i].ManualTransform)
					adjusted++
				}
				debuglog.Log(fmt.Sprintf("AlignInputsByStarsWithMode: global bundle adjustment improved %d frame(s)", adjusted))
			}
		}
	}

	// Chips of a multi-chip exposure are rigid and share one residual; let an
	// unaligned chip inherit an aligned sibling's solution rather than be left
	// unrefined (or, before the chain-fallback gate, mis-shoved by a false match).
	if filled := propagateSameExposureAlignment(inputs, results, aligned); filled > 0 {
		debuglog.Log(fmt.Sprintf("AlignInputsByStarsWithMode: propagated alignment to %d same-exposure sibling chip(s)", filled))
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

	// Build each target's star catalog up front by streaming pixels, so the whole
	// dataset is never resident at once. The reference catalogs come from the
	// user-picked refStars (and their WCS projections), so only the non-reference
	// targets are extracted here.
	catalogs := extractStarCatalogsForAlignment(inputs)

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
				stats      processing.AlignStats
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
				if processing.AlignmentDebugHook != nil {
					// Debug visualization needs the actual pixels; reload the pair
					// on demand so the common (non-debug) path stays pixel-free.
					srcPix, _, _, _, srcErr := resolveFramePixels(inputs[i], Options{})
					refPix, _, _, _, refErr := resolveFramePixels(ref.input, Options{})
					if srcErr != nil || refErr != nil {
						err = fmt.Errorf("debug pixel reload: %v / %v", srcErr, refErr)
						break
					}
					refinement, stats, err = processing.EstimateTweakRegAlignmentWithRefStars(
						srcPix,
						inputs[i].HDU.Data.Width,
						inputs[i].HDU.Data.Height,
						mapper,
						refPix,
						ref.stars,
						ref.input.HDU.Data.Width,
						ref.input.HDU.Data.Height,
						ref.input.HDU.Header,
						searchRadiusArcsec,
						fitgeom,
					)
				} else {
					refinement, stats, err = processing.EstimateTweakRegAlignmentFromCatalogs(
						catalogs[i],
						mapper,
						ref.stars,
						ref.input.HDU.Data.Width,
						ref.input.HDU.Data.Height,
						ref.input.HDU.Header,
						searchRadiusArcsec,
						fitgeom,
					)
				}
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
				MatchedStars:       stats.MatchedStars,
				RMS:                stats.RMS,
				MaxError:           stats.MaxError,
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
	debuglog.Log(fmt.Sprintf("planInputs: %d inputs, scale=%.4f", len(inputs), scale))
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
		debuglog.Log(fmt.Sprintf("planInputs: processing input %d/%d (%s)", idx+1, len(inputs), InputKey(input)))
		transform := processing.IdentityTransform()
		var mapper *processing.WCSMapper
		if idx == 0 {
			statuses[idx].Status = "reference"
			// The reference must be drizzled through its own distortion model
			// (SIP + D2IM) onto the linear output plane, exactly like every
			// other input. Leaving it on the identity affine would place the
			// reference chip in distorted pixel space while all other chips
			// land undistorted, so a single multi-chip exposure's chips no
			// longer line up (non-uniform chip gap, mismatched outer edges).
			var mapperErr error
			mapper, mapperErr = processing.NewWCSMapperToLinearRef(input.HDU.Header, input.D2IX, input.D2IY, ref.HDU.Header)
			if mapperErr != nil {
				statuses[idx].Status = "failed"
				statuses[idx].Error = mapperErr.Error()
				debuglog.Log(fmt.Sprintf("planInputs: FAILED reference %s - WCSMapper: %v", InputKey(input), mapperErr))
				return nil, statuses, 0, 0, 0, 0, fmt.Errorf("reference WCS mapper: %w", mapperErr)
			}
		} else {
			// Build the per-pixel WCS mapper for all non-reference inputs,
			// regardless of whether they share a file with the reference.
			// This replaces the old chipPlacementTransform hack which stripped
			// inter-chip rotation and produced a rotational offset in SCI[2].
			var mapperErr error
			mapper, mapperErr = processing.NewWCSMapperToLinearRef(input.HDU.Header, input.D2IX, input.D2IY, ref.HDU.Header)
			if mapperErr != nil {
				statuses[idx].Status = "failed"
				statuses[idx].Error = mapperErr.Error()
				debuglog.Log(fmt.Sprintf("planInputs: FAILED %s - WCSMapper: %v", InputKey(input), mapperErr))
				continue
			}

			// Also compute a linear affine approximation for CR detection.
			refToSource, wcsErr := processing.ComputeWCSTransform(input.HDU.Header, ref.HDU.Header)
			if wcsErr != nil {
				statuses[idx].Status = "failed"
				statuses[idx].Error = wcsErr.Error()
				debuglog.Log(fmt.Sprintf("planInputs: FAILED %s - WCSTransform: %v", InputKey(input), wcsErr))
				continue
			}
			var invertErr error
			transform, invertErr = processing.InvertAffineTransform(refToSource)
			if invertErr != nil {
				statuses[idx].Status = "failed"
				statuses[idx].Error = invertErr.Error()
				debuglog.Log(fmt.Sprintf("planInputs: FAILED %s - InvertAffine: %v", InputKey(input), invertErr))
				continue
			}
			statuses[idx].Status = "aligned"
		}

		transform = composePlacementTransform(transform, input)

		statuses[idx].Included = true
		p := plannedInput{input: input, sourceToRef: transform, mapper: mapper, statusIndex: idx}
		p.sourcePixelScale = mappedSourcePixelScale(p)
		if !isFinite64(p.sourcePixelScale) || p.sourcePixelScale <= 0 {
			p.sourcePixelScale = 1
		}
		planned = append(planned, p)

		// Reference-only inputs anchor the coordinate system but don't contribute
		// pixels, so we skip them when computing the output canvas bounds.
		if input.ReferenceOnly {
			continue
		}

		// Use mapPixel for canvas bounds so they match the actual pixel
		// placement in the drizzle loop. The affine approximation can diverge
		// from the WCS mapper for inputs far from the linearisation point
		// (e.g. large mosaic offsets with SIP distortion), which shifts those
		// frames in the output canvas. Round to 1e-6 pixels to eliminate
		// floating-point noise from the WCS round-trip (the true error is
		// at the sub-arcsecond level, so 1e-6 pixels is safe).
		const boundsRound = 1e6
		for _, corner := range imageCorners(input.HDU.Data.Width, input.HDU.Data.Height) {
			rx, ry := p.mapPixel(corner[0], corner[1])
			x := math.Round(rx*boundsRound) / boundsRound
			y := math.Round(ry*boundsRound) / boundsRound
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

	failed := 0
	for _, s := range statuses {
		if s.Status == "failed" {
			failed++
		}
	}
	if failed > 0 {
		debuglog.Log(fmt.Sprintf("planInputs: %d/%d inputs planned, %d FAILED", len(planned), len(inputs), failed))
	} else {
		debuglog.Log(fmt.Sprintf("planInputs: all %d inputs planned successfully", len(planned)))
	}

	return planned, statuses, minX, minY, maxX, maxY, nil
}

// buildOutputHeader assembles the drizzled output header. WCS geometry
// (CTYPE/CRPIX/CD/CDELT) is anchored to ref, which with lock-to-reference
// drizzle may be a WCS-only frame from a different filter/instrument than what
// was actually combined. Identity metadata (filter, instrument, detector) is
// therefore taken from metaSource, the first real science input, instead of
// ref, so the output header describes what was drizzled rather than the WCS
// anchor.
func buildOutputHeader(ref, metaSource Input, width, height int, originX, originY, scale float64, includedCount int) fitsio.Header {
	merged := mergeHeaders(ref.PrimaryHeader, ref.HDU.Header)
	cards := fitsio.CloneHeader(merged).Cards

	metaMerged := mergeHeaders(metaSource.PrimaryHeader, metaSource.HDU.Header)
	for _, key := range []string{"FILTER", "FILTNAM1", "FILTNAM2", "FILTER1", "FILTER2", "PUPIL", "INSTRUME", "DETECTOR"} {
		if v, ok := metaMerged.Cards[key]; ok {
			cards[key] = v
		} else {
			delete(cards, key)
		}
	}

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
		if isDistortionHeaderCard(key) {
			delete(cards, key)
		}
	}
	cards["CTYPE1"] = stripSIPSuffix(cards["CTYPE1"])
	cards["CTYPE2"] = stripSIPSuffix(cards["CTYPE2"])

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

func isDistortionHeaderCard(key string) bool {
	return key == "A_ORDER" || key == "B_ORDER" || key == "AP_ORDER" || key == "BP_ORDER" ||
		strings.HasPrefix(key, "A_") || strings.HasPrefix(key, "B_") ||
		strings.HasPrefix(key, "AP_") || strings.HasPrefix(key, "BP_") ||
		strings.HasPrefix(key, "D2IM")
}

func stripSIPSuffix(value string) string {
	if value == "" {
		return value
	}
	if strings.Contains(value, "TAN-SIP") {
		return strings.Replace(value, "TAN-SIP", "TAN", 1)
	}
	return value
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

// effectiveEdgeTrimForInput reports how many pixels to exclude from each edge of
// an input before drizzling. Edge trimming is disabled: every input contributes
// all of its data, so this always returns 0. Kept as the single chokepoint so a
// trim can be reinstated here without touching the drizzle loops or footprints.
func effectiveEdgeTrimForInput(p plannedInput, size int, scale float64) int {
	return 0
}

// SepFrame holds a single drizzled frame in output space, normalized to flux
// units. Uncovered pixels are marked NaN so the median-model builder can ignore
// them without a separate coverage map.
type SepFrame struct {
	Image []float32
}

// drizzleSepFrame drizzles a single planned input into its own output-size
// accumulator and returns the normalized image (NaN where uncovered). Used to
// build per-frame images for the AstroDrizzle-style separate CR pass.
func drizzleSepFrame(p plannedInput, pixels []float32, outW, outH int, minX, minY, scale, dropSize float64, kernel DrizzleKernel, weightingMode WeightingMode) SepFrame {
	// Use out as the flux accumulator directly; weights tracks coverage.
	// This avoids allocating a separate sums array.
	out := make([]float32, outW*outH)
	weights := make([]float32, outW*outH)
	// Keep the original behavior of not trimming the separate CR-model frames.
	trimX, trimY := 0, 0
	drizzlePlannedInput(p, out, weights, outW, outH, minX, minY, scale, dropSize, kernel, weightingMode, nil, pixels, trimX, trimY)

	for i := range out {
		if abs32(weights[i]) <= weightEpsilon {
			out[i] = float32(math.NaN())
			continue
		}
		out[i] /= weights[i]
	}
	return SepFrame{Image: out}
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

func drizzlePlannedInput(p plannedInput, sums, weights []float32, width, height int, minX, minY, scale, dropSize float64, kernel DrizzleKernel, weightingMode WeightingMode, crMask BitMask, pixels []float32, trimX, trimY int) {
	switch kernel {
	case KernelPoint:
		drizzlePlannedInputPoint(p, sums, weights, width, height, minX, minY, scale, weightingMode, crMask, pixels, trimX, trimY)
	case KernelTurbo:
		drizzlePlannedInputTurbo(p, sums, weights, width, height, minX, minY, scale, dropSize, weightingMode, crMask, pixels, trimX, trimY)
	case KernelGaussian:
		drizzlePlannedInputGaussian(p, sums, weights, width, height, minX, minY, scale, dropSize, weightingMode, crMask, pixels, trimX, trimY)
	case KernelTophat:
		drizzlePlannedInputTophat(p, sums, weights, width, height, minX, minY, scale, dropSize, weightingMode, crMask, pixels, trimX, trimY)
	case KernelLanczos2:
		drizzlePlannedInputLanczos(p, sums, weights, width, height, minX, minY, scale, 2, weightingMode, crMask, pixels, trimX, trimY)
	case KernelLanczos3:
		drizzlePlannedInputLanczos(p, sums, weights, width, height, minX, minY, scale, 3, weightingMode, crMask, pixels, trimX, trimY)
	default:
		drizzlePlannedInputSquare(p, sums, weights, width, height, minX, minY, scale, dropSize, weightingMode, crMask, pixels, trimX, trimY)
	}
}

func inputDropSize(p plannedInput, outputScale, pixFrac float64) float64 {
	if pixFrac <= 0 {
		pixFrac = 1
	}
	sourceScale := p.sourcePixelScale
	if !isFinite64(sourceScale) || sourceScale <= 0 {
		sourceScale = 1
	}
	return sourceScale * outputScale * pixFrac
}

func mappedSourcePixelScale(p plannedInput) float64 {
	data := p.input.HDU.Data
	x := float64(data.Width-1) / 2
	y := float64(data.Height-1) / 2
	if data.Width < 2 || data.Height < 2 {
		return 1
	}

	x0, y0 := p.mapPixel(x, y)
	x1, y1 := p.mapPixel(x+1, y)
	x2, y2 := p.mapPixel(x, y+1)
	dx := math.Hypot(x1-x0, y1-y0)
	dy := math.Hypot(x2-x0, y2-y0)
	switch {
	case isFinite64(dx) && dx > 0 && isFinite64(dy) && dy > 0:
		return 0.5 * (dx + dy)
	case isFinite64(dx) && dx > 0:
		return dx
	case isFinite64(dy) && dy > 0:
		return dy
	default:
		return 1
	}
}

func inputExposureTime(input Input) float64 {
	if input.ExposureTime > 0 && !math.IsNaN(input.ExposureTime) && !math.IsInf(input.ExposureTime, 0) {
		return input.ExposureTime
	}
	return 0
}

// frameExposureNormalized reports whether prepareFramePixels already converted
// this frame's SCI (and ERR) pixels to a per-second rate. When true, the drizzle
// weighting must NOT divide by EXPTIME a second time, otherwise exposure
// normalization combined with Exposure/ERR weighting double-divides and (for ERR)
// skews the inter-frame weighting. Mirrors the gate prepareFramePixels applies.
func frameExposureNormalized(input Input) bool {
	return input.NormalizeExposure && !input.ReferenceOnly &&
		isFinite64(input.ExposureScale) && input.ExposureScale > 0
}

// bunitIsAlreadyRate reports whether a BUNIT denotes data that is already in
// per-time rate or absolutely-calibrated flux/surface-brightness units (e.g.
// "ELECTRONS/S", JWST "MJy/sr"), as opposed to total detector counts. Such data
// must never be divided by EXPTIME for Exposure/ERR weighting.
func bunitIsAlreadyRate(bunit string) bool {
	if rate, known := bunitIsRate(bunit); known && rate {
		return true
	}
	return bunitIsCalibratedFlux(bunit)
}

// framePixelsAreRate reports whether this frame's SCI/ERR pixels are already a
// rate (or calibrated flux) and so must NOT be divided by EXPTIME during
// Exposure/ERR weighting. True when prepareFramePixels already normalized the
// frame, or when BUNIT indicates a rate/calibrated-flux unit. This is broader
// than frameExposureNormalized, which only reports the explicit pre-scale path.
func framePixelsAreRate(input Input) bool {
	return frameExposureNormalized(input) || bunitIsAlreadyRate(input.BUnit)
}

func normalizedPixelsForWeighting(input Input, pixels []float32, weightingMode WeightingMode) []float32 {
	if framePixelsAreRate(input) {
		// Pixels are already a rate; no further per-exptime scaling.
		return pixels
	}
	switch weightingMode {
	case WeightExposure, WeightERR:
		if exptime := inputExposureTime(input); exptime > 0 {
			scaled := make([]float32, len(pixels))
			invExp := float32(1.0 / exptime)
			for i, v := range pixels {
				if !isFinite32(v) {
					scaled[i] = v
					continue
				}
				scaled[i] = v * invExp
			}
			return scaled
		}
	}
	return pixels
}

func drizzlePixelWeight(p plannedInput, idx int, weightingMode WeightingMode) float32 {
	exptime := inputExposureTime(p.input)
	switch weightingMode {
	case WeightExposure:
		if exptime > 0 {
			return float32(exptime)
		}
	case WeightERR:
		// A combined working frame carries its per-pixel drizzle weight (the
		// rate-space inverse variance accumulated when the chips were combined)
		// directly in WeightPixels; use it verbatim rather than re-deriving one
		// from ERR.
		if wht := p.input.WeightPixels; wht != nil && idx < len(wht) {
			if w := wht[idx]; w > 0 && isFinite32(w) {
				return w
			}
		}
		if errPix := p.input.ERRPixels; errPix != nil && idx < len(errPix) {
			if e := errPix[idx]; e > 0 && isFinite32(e) {
				// When the frame is already a rate (pre-normalized or calibrated
				// flux), ERR is in the same units, so use it directly. Otherwise
				// convert the count-based sigma to a rate sigma.
				if !framePixelsAreRate(p.input) && exptime > 0 {
					rateErr := e / float32(exptime)
					if rateErr > 0 && isFinite32(rateErr) {
						return 1.0 / (rateErr * rateErr)
					}
				}
				return 1.0 / (e * e)
			}
		}
	}
	return 1
}

func drizzlePixelValue(p plannedInput, idx int, value float32, weightingMode WeightingMode) float32 {
	if framePixelsAreRate(p.input) {
		// Pixels are already a rate (pre-normalized or calibrated flux units);
		// do not divide by EXPTIME.
		return value
	}
	switch weightingMode {
	case WeightExposure, WeightERR:
		if exptime := inputExposureTime(p.input); exptime > 0 {
			return value / float32(exptime)
		}
	}
	return value
}

func drizzlePlannedInputPoint(p plannedInput, sums, weights []float32, width, height int, minX, minY, scale float64, weightingMode WeightingMode, crMask BitMask, pixels []float32, trimX, trimY int) {
	data := p.input.HDU.Data
	const rowInterval = 200
	for y := trimY; y < data.Height-trimY; y++ {
		if (y-trimY)%rowInterval == 0 {
			if p.cancelled() {
				return
			}
			debuglog.Log(fmt.Sprintf("drizzle point: row %d/%d (%s)", y, data.Height, InputKey(p.input)))
		}
		row := y * data.Width
		for x := trimX; x < data.Width-trimX; x++ {
			idx := row + x
			if crMask != nil && crMask.Get(idx) {
				continue
			}
			value := drizzlePixelValue(p, idx, pixels[idx], weightingMode)
			if !isFinite32(value) {
				continue
			}
			outX, outY := p.mapOutputPixel(float64(x), float64(y), minX, minY, scale)
			drizzlePixelPoint(sums, weights, width, height, outX, outY, value, drizzlePixelWeight(p, idx, weightingMode))
		}
	}
}

func drizzlePlannedInputSquare(p plannedInput, sums, weights []float32, width, height int, minX, minY, scale, dropSize float64, weightingMode WeightingMode, crMask BitMask, pixels []float32, trimX, trimY int) {
	data := p.input.HDU.Data
	half := normalizedDropSize(dropSize) / 2.0
	const rowInterval = 200
	extremeCount := 0
	const maxExtremeLog = 3
	for y := trimY; y < data.Height-trimY; y++ {
		if (y-trimY)%rowInterval == 0 {
			if p.cancelled() {
				return
			}
			debuglog.Log(fmt.Sprintf("drizzle square: row %d/%d extreme=%d (%s)", y, data.Height, extremeCount, InputKey(p.input)))
		}
		row := y * data.Width
		for x := trimX; x < data.Width-trimX; x++ {
			idx := row + x
			if crMask != nil && crMask.Get(idx) {
				continue
			}
			value := drizzlePixelValue(p, idx, pixels[idx], weightingMode)
			if !isFinite32(value) {
				continue
			}
			outX, outY := p.mapOutputPixel(float64(x), float64(y), minX, minY, scale)
			if outX < -1e9 || outX > 1e9 || outY < -1e9 || outY > 1e9 || math.IsNaN(outX) || math.IsNaN(outY) {
				extremeCount++
				if extremeCount == 1 && p.mapper != nil {
					_, _, diag := p.mapper.MapPixelDiag(float64(x), float64(y))
					debuglog.Log(fmt.Sprintf("drizzle square: first extreme DIAG %s (%s)", diag, InputKey(p.input)))
				} else if extremeCount <= maxExtremeLog {
					debuglog.Log(fmt.Sprintf("drizzle square: extreme outX=%.3g outY=%.3g at pixel (%d,%d) (%s)", outX, outY, x, y, InputKey(p.input)))
				}
				continue
			}
			drizzlePixelSquarePrepared(sums, weights, width, height, outX, outY, half, value, drizzlePixelWeight(p, idx, weightingMode))
		}
	}
	if extremeCount > 0 {
		debuglog.Log(fmt.Sprintf("drizzle square: done, %d extreme pixels skipped (%s)", extremeCount, InputKey(p.input)))
	}
}

func drizzlePlannedInputTurbo(p plannedInput, sums, weights []float32, width, height int, minX, minY, scale, dropSize float64, weightingMode WeightingMode, crMask BitMask, pixels []float32, trimX, trimY int) {
	data := p.input.HDU.Data
	half := normalizedDropSize(dropSize) / 2.0
	const rowInterval = 200
	for y := trimY; y < data.Height-trimY; y++ {
		if (y-trimY)%rowInterval == 0 {
			if p.cancelled() {
				return
			}
			debuglog.Log(fmt.Sprintf("drizzle turbo: row %d/%d (%s)", y, data.Height, InputKey(p.input)))
		}
		row := y * data.Width
		for x := trimX; x < data.Width-trimX; x++ {
			idx := row + x
			if crMask != nil && crMask.Get(idx) {
				continue
			}
			value := drizzlePixelValue(p, idx, pixels[idx], weightingMode)
			if !isFinite32(value) {
				continue
			}
			outX, outY := p.mapOutputPixel(float64(x), float64(y), minX, minY, scale)
			drizzlePixelTurboPrepared(sums, weights, width, height, outX, outY, half, value, drizzlePixelWeight(p, idx, weightingMode))
		}
	}
}

func drizzlePlannedInputGaussian(p plannedInput, sums, weights []float32, width, height int, minX, minY, scale, dropSize float64, weightingMode WeightingMode, crMask BitMask, pixels []float32, trimX, trimY int) {
	data := p.input.HDU.Data
	sigma := normalizedDropSize(dropSize) / (2 * math.Sqrt(2*math.Log(2)))
	params := gaussianParams{twoSigSq: 2 * sigma * sigma, radius: 3 * sigma}
	const rowInterval = 200
	for y := trimY; y < data.Height-trimY; y++ {
		if (y-trimY)%rowInterval == 0 {
			if p.cancelled() {
				return
			}
			debuglog.Log(fmt.Sprintf("drizzle gaussian: row %d/%d (%s)", y, data.Height, InputKey(p.input)))
		}
		row := y * data.Width
		for x := trimX; x < data.Width-trimX; x++ {
			idx := row + x
			if crMask != nil && crMask.Get(idx) {
				continue
			}
			value := drizzlePixelValue(p, idx, pixels[idx], weightingMode)
			if !isFinite32(value) {
				continue
			}
			outX, outY := p.mapOutputPixel(float64(x), float64(y), minX, minY, scale)
			drizzlePixelGaussianPrepared(sums, weights, width, height, outX, outY, params, value, drizzlePixelWeight(p, idx, weightingMode))
		}
	}
}

func drizzlePlannedInputTophat(p plannedInput, sums, weights []float32, width, height int, minX, minY, scale, dropSize float64, weightingMode WeightingMode, crMask BitMask, pixels []float32, trimX, trimY int) {
	data := p.input.HDU.Data
	radius := normalizedDropSize(dropSize) / 2.0
	params := tophatParams{radius: radius, radSq: radius * radius}
	const rowInterval = 200
	for y := trimY; y < data.Height-trimY; y++ {
		if (y-trimY)%rowInterval == 0 {
			if p.cancelled() {
				return
			}
			debuglog.Log(fmt.Sprintf("drizzle tophat: row %d/%d (%s)", y, data.Height, InputKey(p.input)))
		}
		row := y * data.Width
		for x := trimX; x < data.Width-trimX; x++ {
			idx := row + x
			if crMask != nil && crMask.Get(idx) {
				continue
			}
			value := drizzlePixelValue(p, idx, pixels[idx], weightingMode)
			if !isFinite32(value) {
				continue
			}
			outX, outY := p.mapOutputPixel(float64(x), float64(y), minX, minY, scale)
			drizzlePixelTophatPrepared(sums, weights, width, height, outX, outY, params, value, drizzlePixelWeight(p, idx, weightingMode))
		}
	}
}

func drizzlePlannedInputLanczos(p plannedInput, sums, weights []float32, width, height int, minX, minY, scale float64, n int, weightingMode WeightingMode, crMask BitMask, pixels []float32, trimX, trimY int) {
	data := p.input.HDU.Data
	const rowInterval = 200
	for y := trimY; y < data.Height-trimY; y++ {
		if (y-trimY)%rowInterval == 0 {
			if p.cancelled() {
				return
			}
			debuglog.Log(fmt.Sprintf("drizzle lanczos%d: row %d/%d (%s)", n, y, data.Height, InputKey(p.input)))
		}
		row := y * data.Width
		for x := trimX; x < data.Width-trimX; x++ {
			idx := row + x
			if crMask != nil && crMask.Get(idx) {
				continue
			}
			value := drizzlePixelValue(p, idx, pixels[idx], weightingMode)
			if !isFinite32(value) {
				continue
			}
			outX, outY := p.mapOutputPixel(float64(x), float64(y), minX, minY, scale)
			drizzlePixelLanczosPrepared(sums, weights, width, height, outX, outY, value, n, drizzlePixelWeight(p, idx, weightingMode))
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

// drizzlePixelSquare is the classic drizzle box-overlap kernel.
func drizzlePixelSquare(sums, weights []float32, width, height int, cx, cy, dropSize float64, value float32, pixelWeight float32) {
	drizzlePixelSquarePrepared(sums, weights, width, height, cx, cy, normalizedDropSize(dropSize)/2.0, value, pixelWeight)
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
	base := strings.ToLower(filepath.Base(path))
	return strings.Contains(base, "_flc") || strings.Contains(base, "_flt")
}

// LooksLikeCal reports whether path is a JWST Stage-2 calibrated product
// (_cal.fits), the per-exposure input used to build mosaics.
func LooksLikeCal(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	return strings.Contains(base, "_cal")
}

// LooksLikeCalibratedInput reports whether path is a supported calibrated
// science exposure: HST _flc/_flt or JWST _cal.
func LooksLikeCalibratedInput(path string) bool {
	return LooksLikeFLC(path) || LooksLikeCal(path)
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
