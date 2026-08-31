package mosaic

import (
	"context"
	"fmt"
	"math"
	"runtime"
	"sync"
	"sync/atomic"

	"gofitsv3/internal/debuglog"
	"gofitsv3/internal/processing"
)

// alignStarThresholdSigma and alignStarMinArea are the star extraction
// parameters used for alignment catalogs. They match the values the
// pixel-based TweakReg path (and the former getStars cache) used, so a catalog
// built here is identical to one the old code extracted inline.
const (
	alignStarThresholdSigma = 4.0
	alignStarMinArea        = 3
)

// extractStarCatalogsForAlignment extracts each input's star catalog, streaming
// pixels from disk when they are not already resident so peak memory stays
// bounded to roughly the worker count rather than the whole dataset. Catalogs
// are indexed by input position (deterministic regardless of completion order);
// excluded inputs and inputs whose pixels cannot be read get a nil catalog.
//
// When an input already carries in-memory pixels (e.g. the legacy warp-based
// alignment modes preload them) those are used directly; otherwise the frame is
// loaded from disk, its catalog extracted, and the pixels dropped before the next
// frame on that worker.
func extractStarCatalogsForAlignment(inputs []Input, maxStars int) [][]processing.Star {
	catalogs, _ := extractStarCatalogsForAlignmentCtx(context.Background(), inputs, maxStars)
	return catalogs
}

func extractStarCatalogsForAlignmentCtx(ctx context.Context, inputs []Input, maxStars int) ([][]processing.Star, error) {
	caps := make([]int, len(inputs))
	for i := range caps {
		caps[i] = maxStars
	}
	return extractStarCatalogsForAlignmentCapsCtx(ctx, inputs, caps)
}

// extractStarCatalogsForAlignmentCapsCtx is the per-input variant used when a
// ReferenceOnly baseline must retain every detected source. A zero cap means
// uncapped; positive caps retain the existing bounded behavior.
func extractStarCatalogsForAlignmentCapsCtx(ctx context.Context, inputs []Input, caps []int) ([][]processing.Star, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	catalogs := make([][]processing.Star, len(inputs))
	workers := alignExtractionWorkers(inputs)
	if workers > len(inputs) {
		workers = len(inputs)
	}
	if workers < 1 {
		return catalogs, ctx.Err()
	}

	debuglog.Log(fmt.Sprintf("extractStarCatalogsForAlignment: %d input(s), %d worker(s)", len(inputs), workers))
	logMemStats("align: before catalog extraction")
	var next int64 = -1
	var firstErr error
	var errMu sync.Mutex
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if err := ctx.Err(); err != nil {
					errMu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					errMu.Unlock()
					return
				}
				i := int(atomic.AddInt64(&next, 1))
				if i >= len(inputs) {
					return
				}
				if inputs[i].Excluded {
					continue
				}
				// Use in-memory pixels when present (legacy warp modes preload
				// them); otherwise read just this chip's cleaned SCI from disk.
				sci := inputs[i].HDU.Data.Pixels
				w, h := inputs[i].HDU.Data.Width, inputs[i].HDU.Data.Height
				if sci == nil {
					var err error
					sci, w, h, err = loadCleanedSCIForExtraction(inputs[i])
					if err != nil {
						debuglog.Log(fmt.Sprintf("extractStarCatalogsForAlignment: input[%d] %s: %v", i, InputKey(inputs[i]), err))
						continue
					}
				}
				if err := ctx.Err(); err != nil {
					errMu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					errMu.Unlock()
					return
				}
				maxStars := processing.TweakRegCatalogMaxStars
				if i < len(caps) {
					maxStars = caps[i]
				}
				catalogs[i] = processing.ExtractAndLimitStars(sci, w, h,
					alignStarThresholdSigma, alignStarMinArea, maxStars)
			}
		}()
	}
	wg.Wait()
	logMemStats("align: after catalog extraction")
	errMu.Lock()
	err := firstErr
	errMu.Unlock()
	if err == nil {
		err = ctx.Err()
	}
	return catalogs, err
}

// alignExtractionMemoryBudget caps the transient memory the concurrent catalog
// extraction may use. Star catalogs are tiny; the cost is the frames loaded to
// build them, so this bounds peak usage regardless of CPU count.
const alignExtractionMemoryBudget = int64(768) << 20 // 768 MiB

// alignExtractionWorkers chooses how many frames to extract concurrently. Each
// worker transiently loads a full FITS frame (its SCI/ERR/DQ HDUs plus a decode
// scratch buffer) while extracting stars, so concurrency must be bounded by a
// memory budget, not just CPU count — otherwise a wide machine loading many
// large frames at once can spike to several GB. The per-frame estimate is
// derived from the reference frame's dimensions.
func alignExtractionWorkers(inputs []Input) int {
	workers := runtime.NumCPU()

	var w, h int
	for i := range inputs {
		if !inputs[i].Excluded && inputs[i].HDU.Data.Width > 0 && inputs[i].HDU.Data.Height > 0 {
			w, h = inputs[i].HDU.Data.Width, inputs[i].HDU.Data.Height
			break
		}
	}
	if w > 0 && h > 0 {
		// Catalog extraction now reads only one SCI chip plus its DQ extensions
		// (see loadCleanedSCIForExtraction), so the transient cost per worker is
		// roughly the SCI plane + DQ + a decode scratch buffer. ~4x the
		// single-plane float32 size is a conservative upper bound.
		perFrame := int64(w) * int64(h) * 4 * 4
		if perFrame > 0 {
			if byBudget := int(alignExtractionMemoryBudget / perFrame); byBudget < workers {
				workers = byBudget
			}
		}
	}
	if workers < 1 {
		workers = 1
	}
	return workers
}

// resolveFramePixels returns the SCI pixels (and optional ERR pixels) for an
// input. When the input already carries in-memory pixels (the small-mosaic and
// test path) they are returned directly and owned is false, signalling the
// caller must copy before mutating. Otherwise the pixels are loaded fresh via
// Options.FrameLoader (when set) or from disk, and owned is true (safe to mutate
// in place).
func resolveFramePixels(in Input, opts Options) (sci, errPix, whtPix []float32, owned bool, err error) {
	if in.HDU.Data.Pixels != nil {
		return in.HDU.Data.Pixels, in.ERRPixels, in.WeightPixels, false, nil
	}
	if opts.FrameLoaderCtx != nil {
		ctx := opts.Ctx
		if ctx == nil {
			ctx = context.Background()
		}
		sci, errPix, err = opts.FrameLoaderCtx(ctx, in)
		return sci, errPix, nil, true, err
	}
	if opts.FrameLoader != nil {
		// FrameLoader is a test/legacy seam that supplies SCI and ERR only; a
		// combined working frame served this way carries no WHT plane.
		sci, errPix, err = opts.FrameLoader(in)
		return sci, errPix, nil, true, err
	}
	sci, errPix, whtPix, err = loadFrameFromDisk(in)
	return sci, errPix, whtPix, true, err
}

// loadFrameFromDisk reloads a single frame's SCI, ERR, and (for a combined
// working file) WHT pixels from its FITS file, selecting the chip matching
// in.SCIExt and decoding only that chip (plus the DQ needed for cleaning) so
// multi-chip exposures are not re-read in full per chip. The DQ cleaning/repair
// is identical to a normal LoadInputsFromPath.
func loadFrameFromDisk(in Input) (sci, errPix, whtPix []float32, err error) {
	sci, errPix, whtPix, w, h, err := loadChipFromDisk(in, true)
	if err != nil {
		return nil, nil, nil, err
	}
	// Guard against silently drizzling a differently-shaped reload (e.g. a
	// synthesized combined-chip input whose Path points at the raw file).
	if in.HDU.Data.Width > 0 && in.HDU.Data.Height > 0 &&
		(w != in.HDU.Data.Width || h != in.HDU.Data.Height) {
		return nil, nil, nil, fmt.Errorf("frame loader: dimension mismatch for %s (reloaded %dx%d, expected %dx%d)",
			InputKey(in), w, h, in.HDU.Data.Width, in.HDU.Data.Height)
	}
	return sci, errPix, whtPix, nil
}

// surfaceBrightnessScale returns the 1/area scale factor for converting a frame
// from source-pixel count rate to reference-grid pixel-area count rate, and
// whether it is meaningfully different from 1.
func surfaceBrightnessScale(p plannedInput) (float32, bool) {
	area := p.sourcePixelScale * p.sourcePixelScale
	if !isFinite64(area) || area <= 0 {
		return 0, false
	}
	if math.Abs(area-1) < 1e-6 {
		return 0, false
	}
	return float32(1.0 / area), true
}

func applyScaleInPlace(pixels []float32, scale float32) {
	for i, v := range pixels {
		if isFinite32(v) {
			pixels[i] = v * scale
		}
	}
}

// prepareFramePixels resolves a frame's pixels and applies, in place,
// surface-brightness normalization (when enabled) and the supplied sky offset.
// When the underlying pixels are borrowed in-memory arrays they are copied
// before any mutation, so the caller's originals are never altered. Pass
// skyOffset == 0 (or NaN) and an invalid skyPlane skip sky subtraction (e.g.
// during sky planning, where only the surface-brightness step is wanted).
func prepareFramePixels(p plannedInput, opts Options, skyOffset float64, plane skyPlane) (sci, errPix, whtPix []float32, err error) {
	sci, errPix, whtPix, owned, err := resolveFramePixels(p.input, opts)
	if err != nil {
		return nil, nil, nil, err
	}

	var sbScale float32
	needSB := false
	if opts.SurfaceBrightnessNorm && !p.input.ReferenceOnly {
		sbScale, needSB = surfaceBrightnessScale(p)
	}

	// Exposure normalization: convert total-count pixels to a per-second rate by
	// multiplying by ExposureScale (1/EXPTIME). Independent of WeightingMode and
	// applied to both SCI and ERR (which is in the same units). Disabled unless
	// the frame opted in with a finite, positive scale.
	var expScale float32
	needExp := false
	if p.input.NormalizeExposure && !p.input.ReferenceOnly {
		if s := p.input.ExposureScale; isFinite64(s) && s > 0 {
			expScale = float32(s)
			needExp = true
		}
	}
	needSky := skyOffset != 0 && isFinite64(skyOffset)
	needPlane := plane.Valid
	needWisp := opts.Skysub.NIRCamWisp && !p.input.ReferenceOnly
	needAmp := opts.Skysub.AmpPedestal && !p.input.ReferenceOnly
	needStripe := opts.Skysub.RowDestripe && !p.input.ReferenceOnly
	needMIRIArtifactMask := opts.Skysub.MIRIArtifactMask && !p.input.ReferenceOnly

	if (needSB || needExp || needSky || needPlane || needWisp || needAmp || needStripe || needMIRIArtifactMask) && !owned {
		sci = append([]float32(nil), sci...)
		if errPix != nil {
			errPix = append([]float32(nil), errPix...)
		}
		// WeightPixels is inverse variance; only the SCI-scaling steps touch it,
		// so copy it only when a scale is actually applied below.
		if whtPix != nil && (needSB || needExp) {
			whtPix = append([]float32(nil), whtPix...)
		}
	}
	var rowDestripeUserMask []bool
	if needStripe {
		rowDestripeUserMask, err = loadRowDestripeUserMask(p.input, opts.Skysub)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("load row destripe mask for %s: %w", InputKey(p.input), err)
		}
	}
	var miriArtifactMask []bool
	var miriArtifactMaskPath string
	if needMIRIArtifactMask {
		miriArtifactMask, miriArtifactMaskPath, err = loadMIRIArtifactMask(p.input, opts.Skysub)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("load MIRI artifact mask for %s: %w", InputKey(p.input), err)
		}
	}
	if needWisp {
		if err := applyNIRCamWispCorrection(p, sci, opts.Skysub); err != nil {
			return nil, nil, nil, err
		}
	}
	if needSB {
		applyScaleInPlace(sci, sbScale)
		applyScaleInPlace(errPix, sbScale)
		// A pixel scaled by s has variance scaled by s², so its weight
		// (inverse variance) scales by 1/s².
		applyScaleInPlace(whtPix, inverseVarianceScale(sbScale))
	}
	if needExp {
		applyScaleInPlace(sci, expScale)
		applyScaleInPlace(errPix, expScale)
		applyScaleInPlace(whtPix, inverseVarianceScale(expScale))
		debuglog.Log(fmt.Sprintf("prepareFramePixels: %s exposure-normalized scale=%.6g errNormalized=%t",
			InputKey(p.input), expScale, errPix != nil))
	}
	if needAmp {
		applyAmpPedestalCorrection(p, sci)
	}
	if needStripe {
		if err := applyRowDestripe(p, sci, opts.Skysub, rowDestripeUserMask); err != nil {
			return nil, nil, nil, err
		}
	}
	if needMIRIArtifactMask {
		applyMIRIArtifactMask(p, sci, miriArtifactMask, miriArtifactMaskPath)
	}
	if needSky {
		applySkySubInPlace(sci, skyOffset)
	}
	if needPlane {
		applySkyPlaneInPlace(p, sci, plane)
	}
	return sci, errPix, whtPix, nil
}

// inverseVarianceScale converts a value scale s into the factor 1/s² that the
// corresponding inverse-variance weight must be multiplied by. Returns 0 for a
// non-finite or zero scale (matching applyScaleInPlace, which leaves such a
// weight untouched only when the factor is skipped by the caller).
func inverseVarianceScale(s float32) float32 {
	if s == 0 || !isFinite32(s) {
		return 0
	}
	return 1.0 / (s * s)
}

// logMemStats logs a one-line runtime memory snapshot for the given phase so
// before/after peak Alloc/Sys can be compared across a streaming build.
func logMemStats(phase string) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	debuglog.Log(fmt.Sprintf("mem[%s]: Alloc=%dMB HeapInuse=%dMB Sys=%dMB NumGC=%d",
		phase, m.Alloc>>20, m.HeapInuse>>20, m.Sys>>20, m.NumGC))
}
