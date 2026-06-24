package mosaic

import (
	"fmt"
	"math"
	"runtime"
	"sort"
	"sync"

	"gofitsv3/internal/debuglog"
)

// SkyMethod selects the AstroDrizzle-style algorithm used to determine the
// sky value to subtract from each contributing input.
type SkyMethod int

const (
	SkyMethodLocalMin SkyMethod = iota
	SkyMethodGlobalMin
	SkyMethodMatch
	SkyMethodGlobalMinMatch
)

// SkyStat selects the statistic used to estimate sky from eligible pixels.
type SkyStat int

const (
	SkyStatMedian SkyStat = iota
	SkyStatMode
	SkyStatMean
)

// SkysubOptions holds the AstroDrizzle-style sky subtraction controls.
type SkysubOptions struct {
	Enabled  bool
	Method   SkyMethod
	Width    float64
	Stat     SkyStat
	Lower    float64
	Upper    float64
	HasLower bool
	HasUpper bool
	Clip     int
	LSigma   float64
	USigma   float64
}

type skyEdge struct {
	i, j   int
	delta  float64
	weight float64
}

type sampleAccum struct {
	sum   float64
	count int
}

type skyPlane struct {
	A, B, C float64
	Valid   bool
}

func (p skyPlane) value(x, y float64) float64 {
	if !p.Valid {
		return 0
	}
	return p.A*x + p.B*y + p.C
}

func normalizeSkysubOptions(options SkysubOptions) SkysubOptions {
	if options.Width <= 0 {
		options.Width = 0.1
	}
	if options.Clip < 0 {
		options.Clip = 0
	}
	if options.Clip == 0 {
		options.Clip = 5
	}
	if options.LSigma <= 0 {
		options.LSigma = 4.0
	}
	if options.USigma <= 0 {
		options.USigma = 4.0
	}
	switch options.Method {
	case SkyMethodLocalMin, SkyMethodGlobalMin, SkyMethodMatch, SkyMethodGlobalMinMatch:
	default:
		options.Method = SkyMethodLocalMin
	}
	switch options.Stat {
	case SkyStatMedian, SkyStatMode, SkyStatMean:
	default:
		options.Stat = SkyStatMedian
	}
	return options
}

// planSkysub computes the per-frame sky offset to subtract without retaining any
// full-size pixel arrays. It streams each contributing frame from the loader,
// estimates its sky (and, for the matched methods, builds a small downsampled
// overlap sample map), then releases the pixels. The returned skyOffset[i] is
// applied per frame at drizzle time via prepareFramePixels; only scalars (and
// the tiny overlap maps) are held during planning.
func planSkysub(planned []plannedInput, opts Options) (skyOffset []float64, skyPlanes []skyPlane, applied []bool, skyValues []float64, err error) {
	n := len(planned)
	skyOffset = make([]float64, n)
	skyPlanes = make([]skyPlane, n)
	applied = make([]bool, n)
	skyValues = make([]float64, n)
	for i := range skyValues {
		skyValues[i] = math.NaN()
	}
	if !opts.Skysub.Enabled {
		return skyOffset, skyPlanes, applied, skyValues, nil
	}

	options := normalizeSkysubOptions(opts.Skysub)
	rawSky := make([]float64, n)
	needMaps := options.Method == SkyMethodMatch || options.Method == SkyMethodGlobalMinMatch
	var maps []map[int64]float64
	if needMaps {
		maps = make([]map[int64]float64, n)
	}

	// Stream per frame with bounded concurrency: each worker loads one frame,
	// estimates its sky (+ overlap map), then drops the pixels. This keeps at
	// most NumCPU frames resident rather than all of them.
	var skyErr error
	var skyErrMu sync.Mutex
	var wg sync.WaitGroup
	// Each worker holds a full loaded frame plus its sky sample buffer, so cap
	// concurrency to keep peak memory bounded (the loads are I/O-bound anyway).
	sem := make(chan struct{}, skyConcurrency())
	for i := range planned {
		if planned[i].input.ReferenceOnly {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			pixels, _, perr := prepareFramePixels(planned[i], opts, 0, skyPlane{})
			if perr != nil {
				skyErrMu.Lock()
				if skyErr == nil {
					skyErr = fmt.Errorf("load frame for sky estimate %s: %w", InputKey(planned[i].input), perr)
				}
				skyErrMu.Unlock()
				return
			}
			sky, serr := estimateSkyValue(pixels, options)
			if serr != nil {
				skyErrMu.Lock()
				if skyErr == nil {
					skyErr = fmt.Errorf("estimate sky for %s: %w", InputKey(planned[i].input), serr)
				}
				skyErrMu.Unlock()
				return
			}
			rawSky[i] = sky
			if needMaps {
				maps[i] = buildOverlapSampleMap(planned[i], pixels, options)
			}
		}(i)
	}
	wg.Wait()
	if skyErr != nil {
		return nil, nil, nil, nil, skyErr
	}

	subtractSky := make([]float64, n)
	var matched []bool
	switch options.Method {
	case SkyMethodGlobalMin:
		globalMin := math.Inf(1)
		for i := range planned {
			if planned[i].input.ReferenceOnly {
				continue
			}
			if rawSky[i] < globalMin {
				globalMin = rawSky[i]
			}
		}
		for i := range planned {
			if !planned[i].input.ReferenceOnly {
				subtractSky[i] = globalMin
			}
		}
	case SkyMethodMatch, SkyMethodGlobalMinMatch:
		var rel []float64
		rel, matched = computeMatchedSkyOffsets(planned, maps, options)
		if options.Method == SkyMethodMatch {
			for i := range planned {
				if planned[i].input.ReferenceOnly {
					continue
				}
				if matched[i] {
					subtractSky[i] = rel[i]
				} else {
					// If a chip/frame could not be connected to the match graph,
					// fall back to its own measured sky instead of leaving it
					// unsubtracted. This is especially important for ACS/WFC
					// chip-to-chip pedestal differences.
					subtractSky[i] = rawSky[i]
				}
			}
		} else {
			globalMin := math.Inf(1)
			for i := range planned {
				if planned[i].input.ReferenceOnly {
					continue
				}
				if rawSky[i] < globalMin {
					globalMin = rawSky[i]
				}
			}

			minRel := math.Inf(1)
			for i := range planned {
				if planned[i].input.ReferenceOnly || !matched[i] {
					continue
				}
				if rel[i] < minRel {
					minRel = rel[i]
				}
			}

			for i := range planned {
				if planned[i].input.ReferenceOnly {
					continue
				}
				if matched[i] && isFiniteSky64(minRel) && isFiniteSky64(globalMin) {
					subtractSky[i] = globalMin + (rel[i] - minRel)
				} else {
					// A disconnected chip/frame should not inherit the global minimum.
					// Use its own measured sky so per-chip pedestals still get removed.
					subtractSky[i] = rawSky[i]
				}
			}
		}
	default:
		groupMin := map[string]float64{}
		for i := range planned {
			if planned[i].input.ReferenceOnly {
				continue
			}
			key := InputKey(planned[i].input)
			if minVal, ok := groupMin[key]; !ok || rawSky[i] < minVal {
				groupMin[key] = rawSky[i]
			}
		}
		for i := range planned {
			if planned[i].input.ReferenceOnly {
				continue
			}
			subtractSky[i] = groupMin[InputKey(planned[i].input)]
		}
	}

	if needMaps {
		skyPlanes = computeOverlapAnchoredSkyPlanes(planned, maps, subtractSky, matched, options)
	}

	logSkysubPlan(planned, rawSky, subtractSky, maps)

	for i := range planned {
		if planned[i].input.ReferenceOnly {
			continue
		}
		skyOffset[i] = subtractSky[i]
		applied[i] = true
		skyValues[i] = subtractSky[i]
	}
	return skyOffset, skyPlanes, applied, skyValues, nil
}

// skyConcurrency bounds how many frames are sky-estimated at once. Each worker
// holds one loaded frame plus a sky sample buffer, so this is capped well below
// NumCPU on many-core machines to keep peak memory in check.
func skyConcurrency() int {
	workers := runtime.NumCPU()
	if workers > 4 {
		workers = 4
	}
	if workers < 1 {
		workers = 1
	}
	return workers
}

func applySkySubInPlace(pixels []float32, sky float64) {
	if sky == 0 {
		return
	}
	delta := float32(sky)
	for i, v := range pixels {
		if !isFinite32(v) {
			continue
		}
		pixels[i] = v - delta
	}
}

func applySkyPlaneInPlace(p plannedInput, pixels []float32, plane skyPlane) {
	if !plane.Valid {
		return
	}
	width := p.input.HDU.Data.Width
	height := p.input.HDU.Data.Height
	for y := 0; y < height; y++ {
		row := y * width
		for x := 0; x < width; x++ {
			idx := row + x
			if idx >= len(pixels) || !isFinite32(pixels[idx]) {
				continue
			}
			rx, ry := p.mapPixel(float64(x), float64(y))
			correction := plane.value(rx, ry)
			if !isFiniteSky64(correction) {
				continue
			}
			pixels[idx] -= float32(correction)
		}
	}
}

func isFiniteSky64(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

// skyDebugLog can be temporarily enabled while diagnosing per-chip sky matching.
const skyDebugLog = true

func logSkysubPlan(planned []plannedInput, rawSky, subtractSky []float64, maps []map[int64]float64) {
	if !skyDebugLog {
		return
	}
	for i := range planned {
		if planned[i].input.ReferenceOnly {
			continue
		}
		mapCells := 0
		if i < len(maps) {
			mapCells = len(maps[i])
		}
		debuglog.Log(fmt.Sprintf(
			"SKYSUB input=%s rawSky=%.6f subtractSky=%.6f mapCells=%d",
			InputKey(planned[i].input),
			rawSky[i],
			subtractSky[i],
			mapCells,
		))
	}
}

// skyMaxSamples caps how many pixels estimateSkyValue collects from a single
// frame. Beyond this it samples a strided subset: the clipped background
// statistic is unchanged within noise, but the float64 working buffer stays
// bounded instead of growing to 8 bytes per input pixel (≥130 MB for a full ACS
// chip). Frames at or below this size are sampled in full, so small/medium
// inputs and tests are unaffected.
const skyMaxSamples = 2_000_000

func estimateSkyValue(pixels []float32, options SkysubOptions) (float64, error) {
	stride := 1
	if len(pixels) > skyMaxSamples {
		stride = len(pixels) / skyMaxSamples
		if stride < 1 {
			stride = 1
		}
	}
	values := make([]float64, 0, len(pixels)/stride+1)
	for i := 0; i < len(pixels); i += stride {
		px := pixels[i]
		if !isFinite32(px) {
			continue
		}
		v := float64(px)
		if options.HasLower && v < options.Lower {
			continue
		}
		if options.HasUpper && v > options.Upper {
			continue
		}
		values = append(values, v)
	}
	return estimateSkyFromValues(values, options)
}

func estimateSkyFromValues(values []float64, options SkysubOptions) (float64, error) {
	if len(values) == 0 {
		return 0, fmt.Errorf("no usable pixels for sky estimate")
	}
	// Sigma-clip in place. Every caller passes a freshly built, disposable
	// slice, so duplicating it (tens of MB for a full frame) is wasteful.
	work := values
	for iter := 0; iter < options.Clip; iter++ {
		if len(work) == 0 {
			break
		}
		mean, sigma := meanAndSigma(work)
		if sigma <= 0 {
			break
		}
		lo := mean - options.LSigma*sigma
		hi := mean + options.USigma*sigma
		clipped := work[:0]
		for _, v := range work {
			if v >= lo && v <= hi {
				clipped = append(clipped, v)
			}
		}
		if len(clipped) == len(work) {
			work = clipped
			break
		}
		work = clipped
	}
	if len(work) == 0 {
		return 0, fmt.Errorf("all sky pixels rejected by clipping")
	}
	sort.Float64s(work)
	switch options.Stat {
	case SkyStatMean:
		mean, _ := meanAndSigma(work)
		return mean, nil
	case SkyStatMode:
		return histogramMode(work, options.Width), nil
	default:
		return medianFloat64(work), nil
	}
}

func histogramMode(sorted []float64, widthSigma float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	_, sigma := meanAndSigma(sorted)
	if sigma <= 0 {
		return medianFloat64(sorted)
	}
	binWidth := widthSigma * sigma
	if binWidth <= 0 {
		return medianFloat64(sorted)
	}
	minV := sorted[0]
	maxV := sorted[len(sorted)-1]
	if maxV <= minV {
		return minV
	}
	bins := int(math.Ceil((maxV-minV)/binWidth)) + 1
	if bins < 1 {
		bins = 1
	}
	counts := make([]int, bins)
	for _, v := range sorted {
		idx := int(math.Floor((v - minV) / binWidth))
		if idx < 0 {
			idx = 0
		}
		if idx >= bins {
			idx = bins - 1
		}
		counts[idx]++
	}
	best := 0
	for i := 1; i < len(counts); i++ {
		if counts[i] > counts[best] {
			best = i
		}
	}
	return minV + (float64(best)+0.5)*binWidth
}

func meanAndSigma(values []float64) (float64, float64) {
	if len(values) == 0 {
		return 0, 0
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	mean := sum / float64(len(values))
	var varSum float64
	for _, v := range values {
		d := v - mean
		varSum += d * d
	}
	return mean, math.Sqrt(varSum / float64(len(values)))
}

func medianFloat64(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	mid := len(values) / 2
	if len(values)%2 == 1 {
		return values[mid]
	}
	return 0.5 * (values[mid-1] + values[mid])
}

// computeMatchedSkyOffsets solves for the relative sky offset between
// overlapping frames given each frame's precomputed (downsampled) overlap sample
// map. The maps are built once by planSkysub so no full-size pixel arrays are
// retained here. It returns matched[i]=true only for frames/chips that are
// connected to at least one usable overlap edge. Callers should fall back to
// rawSky[i] for unmatched inputs rather than treating their relative offset as
// a valid zero.
func computeMatchedSkyOffsets(planned []plannedInput, maps []map[int64]float64, options SkysubOptions) ([]float64, []bool) {
	offsets := make([]float64, len(planned))
	matched := make([]bool, len(planned))
	edges := make([]skyEdge, 0)
	for i := 0; i < len(planned); i++ {
		if planned[i].input.ReferenceOnly || len(maps[i]) == 0 {
			continue
		}
		for j := i + 1; j < len(planned); j++ {
			if planned[j].input.ReferenceOnly || len(maps[j]) == 0 {
				continue
			}
			diffs := overlapDiffsForEdge(maps[i], maps[j])
			if len(diffs) < skyMinOverlapCells {
				continue
			}
			delta, err := estimateSkyFromValues(diffs, options)
			if err != nil {
				continue
			}
			// Weight each edge by its overlap support so well-measured interior
			// edges dominate the solve and edge chips, which typically connect
			// through one or two small overlaps, inherit a consistent level
			// through the chain instead of being pinned by a single noisy edge.
			// sqrt keeps one huge overlap from completely swamping several
			// medium ones.
			edges = append(edges, skyEdge{i: i, j: j, delta: delta, weight: math.Sqrt(float64(len(diffs)))})
			if skyDebugLog {
				debuglog.Log(fmt.Sprintf(
					"SKYSUB EDGE i=%s j=%s cells=%d delta=%.6f",
					InputKey(planned[i].input),
					InputKey(planned[j].input),
					len(diffs),
					delta,
				))
			}
		}
	}
	if len(edges) == 0 {
		return offsets, matched
	}
	components := connectedSkyComponents(len(planned), edges)
	for _, component := range components {
		if len(component) <= 1 {
			continue
		}
		compSet := make(map[int]struct{}, len(component))
		for _, idx := range component {
			compSet[idx] = struct{}{}
		}
		compEdges := make([]skyEdge, 0, len(edges))
		for _, edge := range edges {
			_, okI := compSet[edge.i]
			_, okJ := compSet[edge.j]
			if okI && okJ {
				compEdges = append(compEdges, edge)
			}
		}
		compOffsets := solveSkyComponent(component, compEdges)
		for _, idx := range component {
			matched[idx] = true
		}
		for idx, val := range compOffsets {
			offsets[idx] = val
		}
	}
	return offsets, matched
}

// overlapDiffsForEdge returns per-cell differences for one edge in the sky
// match graph. The returned value is always mapJ - mapI, regardless of which
// map is smaller and therefore cheaper to iterate.
func overlapDiffsForEdge(mapI, mapJ map[int64]float64) []float64 {
	diffs := make([]float64, 0, minInt(len(mapI), len(mapJ)))
	if len(mapI) <= len(mapJ) {
		for key, vi := range mapI {
			vj, ok := mapJ[key]
			if !ok {
				continue
			}
			diffs = append(diffs, vj-vi)
		}
	} else {
		for key, vj := range mapJ {
			vi, ok := mapI[key]
			if !ok {
				continue
			}
			diffs = append(diffs, vj-vi)
		}
	}
	return diffs
}

type skyPlaneSample struct {
	x, y, z float64
}

func computeOverlapAnchoredSkyPlanes(planned []plannedInput, maps []map[int64]float64, subtractSky []float64, matched []bool, options SkysubOptions) []skyPlane {
	planes := make([]skyPlane, len(planned))
	for i := range planned {
		if planned[i].input.ReferenceOnly || i >= len(maps) || len(maps[i]) == 0 || i >= len(matched) || !matched[i] {
			continue
		}
		overlapKeys := overlapKeysForInput(i, planned, maps)
		if len(overlapKeys) < skyMinPlaneAnchorCells {
			continue
		}
		anchorVals := make([]float64, 0, len(overlapKeys))
		for key := range overlapKeys {
			if v, ok := maps[i][key]; ok {
				anchorVals = append(anchorVals, v-subtractSky[i])
			}
		}
		if len(anchorVals) < skyMinPlaneAnchorCells {
			continue
		}
		sort.Float64s(anchorVals)
		anchor := medianFloat64(anchorVals)

		samples := make([]skyPlaneSample, 0, len(maps[i]))
		for key, value := range maps[i] {
			x, y := overlapCellCenter(key)
			samples = append(samples, skyPlaneSample{x: x, y: y, z: value - subtractSky[i] - anchor})
		}
		plane, ok := fitSkyPlane(samples, options)
		if !ok {
			continue
		}

		anchorCorrections := make([]float64, 0, len(overlapKeys))
		for key := range overlapKeys {
			x, y := overlapCellCenter(key)
			anchorCorrections = append(anchorCorrections, plane.value(x, y))
		}
		sort.Float64s(anchorCorrections)
		plane.C -= medianFloat64(anchorCorrections)
		plane.Valid = true
		planes[i] = plane
	}
	return planes
}

func overlapKeysForInput(i int, planned []plannedInput, maps []map[int64]float64) map[int64]struct{} {
	keys := make(map[int64]struct{})
	for j := range planned {
		if i == j || planned[j].input.ReferenceOnly || j >= len(maps) || len(maps[j]) == 0 {
			continue
		}
		if len(maps[i]) <= len(maps[j]) {
			for key := range maps[i] {
				if _, ok := maps[j][key]; ok {
					keys[key] = struct{}{}
				}
			}
		} else {
			for key := range maps[j] {
				if _, ok := maps[i][key]; ok {
					keys[key] = struct{}{}
				}
			}
		}
	}
	return keys
}

func fitSkyPlane(samples []skyPlaneSample, options SkysubOptions) (skyPlane, bool) {
	if len(samples) < skyMinPlaneFitCells {
		return skyPlane{}, false
	}
	work := append([]skyPlaneSample(nil), samples...)
	clip := options.Clip
	if clip < 1 {
		clip = 1
	}
	var plane skyPlane
	for iter := 0; iter <= clip; iter++ {
		var ok bool
		plane, ok = solveSkyPlane(work)
		if !ok {
			return skyPlane{}, false
		}
		if iter == clip {
			break
		}
		residuals := make([]float64, len(work))
		for i, sample := range work {
			residuals[i] = sample.z - plane.value(sample.x, sample.y)
		}
		mean, sigma := meanAndSigma(residuals)
		if sigma <= 0 {
			break
		}
		lo := mean - options.LSigma*sigma
		hi := mean + options.USigma*sigma
		clipped := work[:0]
		for i, sample := range work {
			if residuals[i] >= lo && residuals[i] <= hi {
				clipped = append(clipped, sample)
			}
		}
		if len(clipped) == len(work) || len(clipped) < skyMinPlaneFitCells {
			break
		}
		work = clipped
	}
	plane.Valid = true
	return plane, true
}

func solveSkyPlane(samples []skyPlaneSample) (skyPlane, bool) {
	if len(samples) < 3 {
		return skyPlane{}, false
	}
	var meanX, meanY float64
	for _, sample := range samples {
		meanX += sample.x
		meanY += sample.y
	}
	meanX /= float64(len(samples))
	meanY /= float64(len(samples))

	ata := make([][]float64, 3)
	for i := range ata {
		ata[i] = make([]float64, 3)
	}
	atb := make([]float64, 3)
	for _, sample := range samples {
		x := sample.x - meanX
		y := sample.y - meanY
		terms := [3]float64{x, y, 1}
		for r := 0; r < 3; r++ {
			atb[r] += terms[r] * sample.z
			for c := 0; c < 3; c++ {
				ata[r][c] += terms[r] * terms[c]
			}
		}
	}
	sol := solveLinearSystem(ata, atb)
	if len(sol) != 3 {
		return skyPlane{}, false
	}
	return skyPlane{
		A: sol[0],
		B: sol[1],
		C: sol[2] - sol[0]*meanX - sol[1]*meanY,
	}, true
}

const (
	// skyOverlapMaxSamples limits how many samples from one chip/frame are used
	// to build the coarse overlap map when the map cells would still be well
	// populated. Very large mosaics may exceed this target so each overlap cell
	// keeps enough samples for stable sky matching.
	skyOverlapMaxSamples = 20_000

	// skyOverlapCellSize is in output mosaic pixels. Coarse cells make overlap
	// matching robust against dithers, distortion, and non-identical sampling.
	skyOverlapCellSize = 32.0

	// skyOverlapSamplesPerCellSide keeps each coarse cell from being represented
	// by only one or two source pixels on large ACS-sized frames.
	skyOverlapSamplesPerCellSide = 8

	// skyOverlapMinCellSamples rejects partial/noisy cells that do not have
	// enough support to represent the local overlap background.
	skyOverlapMinCellSamples = 8

	// skyMinOverlapCells avoids solving a sky edge from one or two accidental
	// matching cells.
	skyMinOverlapCells = 8

	// skyMinPlaneAnchorCells requires enough overlap anchors before extrapolating
	// a per-chip correction plane into non-overlap regions.
	skyMinPlaneAnchorCells = 8

	// skyMinPlaneFitCells requires enough coarse background samples across the
	// chip to fit a stable clipped plane.
	skyMinPlaneFitCells = 12
)

func overlapSampleStride(width, height int) int {
	stride := 1
	if total := width * height; total > skyOverlapMaxSamples {
		stride = int(math.Ceil(math.Sqrt(float64(total) / float64(skyOverlapMaxSamples))))
		if stride < 1 {
			stride = 1
		}
	}
	maxStride := int(math.Floor(skyOverlapCellSize / skyOverlapSamplesPerCellSide))
	if maxStride < 1 {
		maxStride = 1
	}
	if stride > maxStride {
		stride = maxStride
	}
	return stride
}

func buildOverlapSampleMap(p plannedInput, pixels []float32, options SkysubOptions) map[int64]float64 {
	width := p.input.HDU.Data.Width
	height := p.input.HDU.Data.Height
	stride := overlapSampleStride(width, height)
	accum := make(map[int64]sampleAccum)
	for y := 0; y < height; y += stride {
		for x := 0; x < width; x += stride {
			idx := y*width + x
			if idx >= len(pixels) {
				continue
			}
			v := pixels[idx]
			if !isFinite32(v) {
				continue
			}
			fv := float64(v)
			if options.HasLower && fv < options.Lower {
				continue
			}
			if options.HasUpper && fv > options.Upper {
				continue
			}
			rx, ry := p.mapPixel(float64(x), float64(y))
			key := overlapCellKey(rx, ry)
			cur := accum[key]
			cur.sum += fv
			cur.count++
			accum[key] = cur
		}
	}
	out := make(map[int64]float64, len(accum))
	for key, cur := range accum {
		if cur.count < skyOverlapMinCellSamples {
			continue
		}
		out[key] = cur.sum / float64(cur.count)
	}
	return out
}

func overlapCellKey(x, y float64) int64 {
	ix := int32(math.Floor(x / skyOverlapCellSize))
	iy := int32(math.Floor(y / skyOverlapCellSize))
	return (int64(ix) << 32) | int64(uint32(iy))
}

func overlapCellCenter(key int64) (float64, float64) {
	ix := int32(key >> 32)
	iy := int32(uint32(key))
	return (float64(ix) + 0.5) * skyOverlapCellSize, (float64(iy) + 0.5) * skyOverlapCellSize
}

func connectedSkyComponents(n int, edges []skyEdge) [][]int {
	adj := make([][]int, n)
	active := make([]bool, n)
	for _, edge := range edges {
		adj[edge.i] = append(adj[edge.i], edge.j)
		adj[edge.j] = append(adj[edge.j], edge.i)
		active[edge.i] = true
		active[edge.j] = true
	}
	seen := make([]bool, n)
	var components [][]int
	for i := 0; i < n; i++ {
		if seen[i] || !active[i] {
			continue
		}
		queue := []int{i}
		seen[i] = true
		var comp []int
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			comp = append(comp, cur)
			for _, next := range adj[cur] {
				if seen[next] {
					continue
				}
				seen[next] = true
				queue = append(queue, next)
			}
		}
		components = append(components, comp)
	}
	return components
}

func solveSkyComponent(component []int, edges []skyEdge) map[int]float64 {
	result := make(map[int]float64, len(component))
	if len(component) == 0 {
		return result
	}
	root := component[0]
	for _, idx := range component {
		if idx < root {
			root = idx
		}
	}
	if len(component) == 1 {
		result[root] = 0
		return result
	}
	varIndex := make(map[int]int, len(component)-1)
	vars := make([]int, 0, len(component)-1)
	for _, idx := range component {
		if idx == root {
			continue
		}
		varIndex[idx] = len(vars)
		vars = append(vars, idx)
	}
	nVars := len(vars)
	ata := make([][]float64, nVars)
	for i := range ata {
		ata[i] = make([]float64, nVars)
	}
	atb := make([]float64, nVars)
	for _, edge := range edges {
		coeffs := make(map[int]float64, 2)
		if edge.i != root {
			coeffs[varIndex[edge.i]] = -1
		}
		if edge.j != root {
			coeffs[varIndex[edge.j]] = 1
		}
		w := edge.weight
		if w <= 0 {
			w = 1
		}
		for a, ca := range coeffs {
			atb[a] += w * ca * edge.delta
			for b, cb := range coeffs {
				ata[a][b] += w * ca * cb
			}
		}
	}
	sol := solveLinearSystem(ata, atb)
	result[root] = 0
	for idx, varPos := range varIndex {
		result[idx] = sol[varPos]
	}
	return result
}

func solveLinearSystem(a [][]float64, b []float64) []float64 {
	n := len(b)
	if n == 0 {
		return nil
	}
	mat := make([][]float64, n)
	for i := 0; i < n; i++ {
		mat[i] = append([]float64(nil), a[i]...)
		mat[i][i] += 1e-9
	}
	rhs := append([]float64(nil), b...)
	for col := 0; col < n; col++ {
		pivot := col
		for row := col + 1; row < n; row++ {
			if math.Abs(mat[row][col]) > math.Abs(mat[pivot][col]) {
				pivot = row
			}
		}
		if math.Abs(mat[pivot][col]) < 1e-12 {
			continue
		}
		mat[col], mat[pivot] = mat[pivot], mat[col]
		rhs[col], rhs[pivot] = rhs[pivot], rhs[col]
		div := mat[col][col]
		for j := col; j < n; j++ {
			mat[col][j] /= div
		}
		rhs[col] /= div
		for row := 0; row < n; row++ {
			if row == col {
				continue
			}
			factor := mat[row][col]
			if factor == 0 {
				continue
			}
			for j := col; j < n; j++ {
				mat[row][j] -= factor * mat[col][j]
			}
			rhs[row] -= factor * rhs[col]
		}
	}
	return rhs
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
