package mosaic

import (
	"fmt"
	"math"
	"runtime"

	"gofitsv3/internal/debuglog"
)

// resolveFramePixels returns the SCI pixels (and optional ERR pixels) for an
// input. When the input already carries in-memory pixels (the small-mosaic and
// test path) they are returned directly and owned is false, signalling the
// caller must copy before mutating. Otherwise the pixels are loaded fresh via
// Options.FrameLoader (when set) or from disk, and owned is true (safe to mutate
// in place).
func resolveFramePixels(in Input, opts Options) (sci, errPix []float32, owned bool, err error) {
	if in.HDU.Data.Pixels != nil {
		return in.HDU.Data.Pixels, in.ERRPixels, false, nil
	}
	if opts.FrameLoader != nil {
		sci, errPix, err = opts.FrameLoader(in)
		return sci, errPix, true, err
	}
	sci, errPix, err = loadFrameFromDisk(in)
	return sci, errPix, true, err
}

// loadFrameFromDisk reloads a single frame's SCI and ERR pixels from its FITS
// file, selecting the SCI extension matching in.SCIExt. It reuses
// LoadInputsFromPath so the DQ cleaning/repair is identical to a normal load.
func loadFrameFromDisk(in Input) (sci, errPix []float32, err error) {
	loaded, err := LoadInputsFromPath(in.Path)
	if err != nil {
		return nil, nil, err
	}
	var chosen *Input
	for i := range loaded {
		if loaded[i].SCIExt == in.SCIExt {
			chosen = &loaded[i]
			break
		}
	}
	if chosen == nil {
		if len(loaded) == 1 {
			chosen = &loaded[0]
		} else {
			return nil, nil, fmt.Errorf("frame loader: no SCI ext %d in %s", in.SCIExt, in.Path)
		}
	}
	// Guard against silently drizzling a differently-shaped reload (e.g. a
	// synthesized combined-chip input whose Path points at the raw file).
	if in.HDU.Data.Width > 0 && in.HDU.Data.Height > 0 &&
		(chosen.HDU.Data.Width != in.HDU.Data.Width || chosen.HDU.Data.Height != in.HDU.Data.Height) {
		return nil, nil, fmt.Errorf("frame loader: dimension mismatch for %s (reloaded %dx%d, expected %dx%d)",
			InputKey(in), chosen.HDU.Data.Width, chosen.HDU.Data.Height, in.HDU.Data.Width, in.HDU.Data.Height)
	}
	return chosen.HDU.Data.Pixels, chosen.ERRPixels, nil
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
// skyOffset == 0 (or NaN) to skip sky subtraction (e.g. during sky planning,
// where only the surface-brightness step is wanted).
func prepareFramePixels(p plannedInput, opts Options, skyOffset float64) (sci, errPix []float32, err error) {
	sci, errPix, owned, err := resolveFramePixels(p.input, opts)
	if err != nil {
		return nil, nil, err
	}

	var sbScale float32
	needSB := false
	if opts.SurfaceBrightnessNorm && !p.input.ReferenceOnly {
		sbScale, needSB = surfaceBrightnessScale(p)
	}
	needSky := skyOffset != 0 && isFinite64(skyOffset)

	if (needSB || needSky) && !owned {
		sci = append([]float32(nil), sci...)
		if errPix != nil {
			errPix = append([]float32(nil), errPix...)
		}
	}
	if needSB {
		applyScaleInPlace(sci, sbScale)
		applyScaleInPlace(errPix, sbScale)
	}
	if needSky {
		applySkySubInPlace(sci, skyOffset)
	}
	return sci, errPix, nil
}

// logMemStats logs a one-line runtime memory snapshot for the given phase so
// before/after peak Alloc/Sys can be compared across a streaming build.
func logMemStats(phase string) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	debuglog.Log(fmt.Sprintf("mem[%s]: Alloc=%dMB HeapInuse=%dMB Sys=%dMB NumGC=%d",
		phase, m.Alloc>>20, m.HeapInuse>>20, m.Sys>>20, m.NumGC))
}
