package mosaic

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"

	"gofitsv3/internal/debuglog"
	"gofitsv3/internal/processing"
)

// crModelMemBudget bounds the resident memory used while streaming the per-frame
// separate-drizzle products back in to build the median model. The row-band
// height is chosen so (numFrames * band * width * 4 bytes) stays under this. It
// is a var (not const) so tests can shrink it to force multi-band processing.
var crModelMemBudget int64 = 256 << 20 // 256 MB

// buildCRMasksDrizzle implements the AstroDrizzle-style separate-drizzle cosmic
// ray pipeline without holding every per-frame drizzled image in memory at once.
//
// It runs in three streamed phases:
//  1. Separate pass: each data frame is drizzled into one output-size buffer and
//     the normalized result is written to a temp file (NaN marks uncovered
//     pixels). Buffers are released immediately, so peak is a small number of
//     output-size arrays rather than one per frame.
//  2. Median model: the temp files are streamed back in horizontal row bands and
//     minmed/median-combined into a single output-size model image.
//  3. CR masks: each frame is reloaded and flagged against the blotted model
//     independently (BuildCRMasksFromModel touches only that frame plus the
//     shared model), and the result is stored as a compact BitMask.
//
// masks[slot] corresponds to dataPlanned[slot]. Temp files are always cleaned up.
func buildCRMasksDrizzle(
	planned []plannedInput,
	dataPlanned []int,
	skyOffsets []float64,
	skyPlanes []skyPlane,
	opts Options,
	width, height int,
	minX, minY, scale float64,
) ([]BitMask, error) {
	n := len(dataPlanned)
	if n < 2 {
		return nil, nil
	}

	// Root the scratch files next to the source images (same drive) rather than
	// the system temp directory, which may live on a small system volume (e.g.
	// C:) that lacks room for large separate-drizzle products.
	tmpBase := crTempBaseDir(planned, dataPlanned)
	if err := os.MkdirAll(tmpBase, 0o755); err != nil {
		return nil, fmt.Errorf("create CR temp base %s: %w", tmpBase, err)
	}
	tmpDir, err := os.MkdirTemp(tmpBase, "gofits-crsep-")
	if err != nil {
		return nil, fmt.Errorf("create CR temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	paths := make([]string, n)

	// ---- Phase 1: separate drizzle to temp files ----
	logMemStats("CR sep-drizzle start")
	sepWorkers := sepConcurrency(width, height)
	debuglog.Log(fmt.Sprintf("buildCRMasksDrizzle: sep pass, %d frames, %d workers", n, sepWorkers))
	var sepWG sync.WaitGroup
	sepSem := make(chan struct{}, sepWorkers)
	var sepErr atomic.Value // error
	var sepDone int32
	for slot, pi := range dataPlanned {
		if err := opts.cancelled(); err != nil {
			sepErr.Store(err)
			break
		}
		sepWG.Add(1)
		sepSem <- struct{}{}
		go func(slot, pi int) {
			defer sepWG.Done()
			defer func() { <-sepSem }()
			pixels, _, _, perr := prepareFramePixels(planned[pi], opts, skyOffsets[pi], skyPlanes[pi])
			if perr != nil {
				sepErr.Store(fmt.Errorf("load frame %s: %w", InputKey(planned[pi].input), perr))
				return
			}
			sep := drizzleSepFrame(planned[pi], pixels, width, height, minX, minY, scale,
				inputDropSize(planned[pi], scale, opts.PixFrac), opts.SepKernel, opts.WeightingMode)
			pixels = nil
			path := fmt.Sprintf("%s/sep_%03d.f32", tmpDir, slot)
			if werr := writeFloat32File(path, sep.Image); werr != nil {
				sepErr.Store(fmt.Errorf("write sep temp %s: %w", InputKey(planned[pi].input), werr))
				return
			}
			paths[slot] = path
			done := atomic.AddInt32(&sepDone, 1)
			opts.reportProgress("Cleaning cosmic rays", int(done), n)
		}(slot, pi)
	}
	sepWG.Wait()
	if e := sepErr.Load(); e != nil {
		return nil, e.(error)
	}
	if err := opts.cancelled(); err != nil {
		return nil, err
	}

	// ---- Phase 2: streamed median model ----
	logMemStats("CR median-model start")
	debuglog.Log("buildCRMasksDrizzle: building median model from temp files")
	model, err := buildMedianModelFromFiles(paths, width, height, n, func() bool { return opts.cancelled() != nil })
	if err != nil {
		return nil, err
	}

	// ---- Phase 3: per-frame CR masks against the model ----
	logMemStats("CR mask start")
	crSeedSNR := opts.CRSeedSNR
	if crSeedSNR <= 0 {
		crSeedSNR = 4.0
	}
	crDerivScale := opts.CRDerivScale
	if crDerivScale <= 0 {
		crDerivScale = 1.2
	}
	crOpts := processing.DrizzleStyleCROptions{
		SeedSNR:    crSeedSNR,
		DerivScale: crDerivScale,
	}

	masks := make([]BitMask, n)
	var maskWG sync.WaitGroup
	maskSem := make(chan struct{}, maskConcurrency())
	var maskErr atomic.Value
	for slot, pi := range dataPlanned {
		if err := opts.cancelled(); err != nil {
			maskErr.Store(err)
			break
		}
		maskWG.Add(1)
		maskSem <- struct{}{}
		go func(slot, pi int) {
			defer maskWG.Done()
			defer func() { <-maskSem }()
			pixels, errPix, _, perr := prepareFramePixels(planned[pi], opts, skyOffsets[pi], skyPlanes[pi])
			if perr != nil {
				maskErr.Store(fmt.Errorf("reload frame %s: %w", InputKey(planned[pi].input), perr))
				return
			}
			crPixels := normalizedPixelsForWeighting(planned[pi].input, pixels, opts.WeightingMode)
			// The ERR plane is the per-pixel 1-sigma in the same units as SCI and
			// is scaled identically by prepareFramePixels, so the same weighting
			// normalization keeps it matched to crPixels. Used as the CR noise
			// model when present.
			var crNoise []float32
			if errPix != nil && len(errPix) == len(crPixels) {
				crNoise = normalizedPixelsForWeighting(planned[pi].input, errPix, opts.WeightingMode)
			}
			pixels = nil
			errPix = nil
			_, sigma := processing.EstimateBackground(crPixels)
			refToSource, ierr := processing.InvertAffineTransform(planned[pi].sourceToRef)
			if ierr != nil {
				refToSource = processing.IdentityTransform()
			}
			pcopy := planned[pi]
			fi := processing.FrameInfo{
				Pixels:      crPixels,
				Noise:       crNoise,
				Width:       planned[pi].input.HDU.Data.Width,
				Height:      planned[pi].input.HDU.Data.Height,
				SourceToRef: planned[pi].sourceToRef,
				RefToSource: refToSource,
				Sigma:       sigma,
				MapFunc:     func(x, y float64) (float64, float64) { return pcopy.mapPixel(x, y) },
			}
			boolMasks := processing.BuildCRMasksFromModel(
				[]processing.FrameInfo{fi}, model, width, height, minX, minY, scale, crOpts)
			if len(boolMasks) == 1 && len(boolMasks[0]) > 0 {
				m := NewBitMask(len(boolMasks[0]))
				for px, flagged := range boolMasks[0] {
					if flagged {
						m.Set(px)
					}
				}
				masks[slot] = m
			}
		}(slot, pi)
	}
	maskWG.Wait()
	if e := maskErr.Load(); e != nil {
		return nil, e.(error)
	}
	if err := opts.cancelled(); err != nil {
		return nil, err
	}
	logMemStats("CR mask done")
	return masks, nil
}

// crTempBaseDir picks the directory that holds the CR scratch files. It uses the
// working/ subdirectory next to the first source image so the scratch lives on
// the same drive the images were loaded from, never the system temp volume. When
// no usable source path is available it falls back to the system temp dir.
func crTempBaseDir(planned []plannedInput, dataPlanned []int) string {
	for _, pi := range dataPlanned {
		if pi < 0 || pi >= len(planned) {
			continue
		}
		src := planned[pi].input.SourcePath
		if src == "" {
			src = planned[pi].input.Path
		}
		if src == "" {
			continue
		}
		if dir := filepath.Dir(src); dir != "" && dir != "." {
			return filepath.Join(dir, WorkingDirName)
		}
	}
	return os.TempDir()
}

// sepConcurrency picks how many separate-drizzle frames to process at once. Each
// holds two output-size float32 buffers, so for large output canvases this drops
// to one worker to keep peak memory bounded.
func sepConcurrency(width, height int) int {
	workers := 2
	if cpu := runtime.NumCPU(); cpu < workers {
		workers = cpu
	}
	if width > 0 && height > 0 {
		imgBytes := int64(width) * int64(height) * 4
		if imgBytes > 0 {
			if max := int((512 << 20) / (2 * imgBytes)); max < workers {
				workers = max
			}
		}
	}
	if workers < 1 {
		workers = 1
	}
	return workers
}

// maskConcurrency bounds the per-frame CR mask pass. Each worker holds a few
// input-frame-size arrays (pixels, normalized copy, blotted model), which are far
// smaller than output-size buffers.
func maskConcurrency() int {
	workers := runtime.NumCPU()
	if workers > 4 {
		workers = 4
	}
	if workers < 1 {
		workers = 1
	}
	return workers
}

// buildMedianModelFromFiles streams the per-frame separate-drizzle temp files in
// horizontal row bands and minmed/median-combines them into a single model image.
// Uncovered pixels are written as NaN and ignored. n is the original frame count
// (used for the minmed-vs-median threshold), matching the in-memory path.
func buildMedianModelFromFiles(paths []string, width, height, n int, cancel func() bool) ([]float32, error) {
	model := make([]float32, width*height)

	files := make([]*os.File, len(paths))
	active := 0
	closeAll := func() {
		for _, f := range files {
			if f != nil {
				f.Close()
			}
		}
	}
	for i, p := range paths {
		if p == "" {
			continue
		}
		f, err := os.Open(p)
		if err != nil {
			closeAll()
			return nil, fmt.Errorf("open sep temp %s: %w", p, err)
		}
		files[i] = f
		active++
	}
	defer closeAll()
	if active == 0 {
		// No frames contributed; leave model as NaN so nothing is flagged.
		for i := range model {
			model[i] = float32(math.NaN())
		}
		return model, nil
	}

	bandRows := height
	if active > 0 && width > 0 {
		bandBytes := int64(active) * int64(width) * 4
		if bandBytes > 0 {
			bandRows = int(crModelMemBudget / bandBytes)
		}
	}
	if bandRows < 1 {
		bandRows = 1
	}
	if bandRows > height {
		bandRows = height
	}
	debuglog.Log(fmt.Sprintf("buildMedianModelFromFiles: %d active frames, bandRows=%d/%d", active, bandRows, height))

	bufs := make([][]float32, len(files))
	for i, f := range files {
		if f != nil {
			bufs[i] = make([]float32, bandRows*width)
		}
	}
	vals := make([]float32, 0, active)

	for startRow := 0; startRow < height; startRow += bandRows {
		if cancel != nil && cancel() {
			return nil, ErrCancelled
		}
		rows := bandRows
		if startRow+rows > height {
			rows = height - startRow
		}
		count := rows * width
		for i, f := range files {
			if f == nil {
				continue
			}
			if err := readFloat32At(f, int64(startRow)*int64(width)*4, bufs[i][:count]); err != nil {
				return nil, fmt.Errorf("read sep band: %w", err)
			}
		}
		base := startRow * width
		for r := 0; r < count; r++ {
			vals = vals[:0]
			for i, f := range files {
				if f == nil {
					continue
				}
				v := bufs[i][r]
				if isFinite32(v) {
					vals = append(vals, v)
				}
			}
			model[base+r] = combineMinMed(vals, n)
		}
	}
	return model, nil
}

// combineMinMed sorts vals in place (small n, insertion sort) and returns the
// minmed (min of mean and median) for small stacks (n <= 3) or the plain median
// for larger stacks. Empty input yields NaN. This mirrors buildMedianModel.
func combineMinMed(vals []float32, n int) float32 {
	if len(vals) == 0 {
		return float32(math.NaN())
	}
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
			return mean
		}
		return median
	}
	return median
}

// writeFloat32File writes data as little-endian float32 in row-major order.
func writeFloat32File(path string, data []float32) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	bw := bufio.NewWriterSize(f, 1<<20)
	const chunk = 8192
	buf := make([]byte, chunk*4)
	for i := 0; i < len(data); i += chunk {
		end := i + chunk
		if end > len(data) {
			end = len(data)
		}
		b := buf[:(end-i)*4]
		for j, v := range data[i:end] {
			binary.LittleEndian.PutUint32(b[j*4:], math.Float32bits(v))
		}
		if _, err := bw.Write(b); err != nil {
			f.Close()
			return err
		}
	}
	if err := bw.Flush(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// readFloat32At reads len(dst) little-endian float32 values starting at byteOff.
func readFloat32At(f *os.File, byteOff int64, dst []float32) error {
	if _, err := f.Seek(byteOff, io.SeekStart); err != nil {
		return err
	}
	br := bufio.NewReaderSize(f, 1<<20)
	const chunk = 8192
	buf := make([]byte, chunk*4)
	for i := 0; i < len(dst); i += chunk {
		end := i + chunk
		if end > len(dst) {
			end = len(dst)
		}
		b := buf[:(end-i)*4]
		if _, err := io.ReadFull(br, b); err != nil {
			return err
		}
		for j := range dst[i:end] {
			dst[i+j] = math.Float32frombits(binary.LittleEndian.Uint32(b[j*4:]))
		}
	}
	return nil
}
