package mosaic

import (
	"fmt"
	"math"
	"runtime"
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
	// SkyMethodMatchPlane solves a per-frame correction PLANE (not just a
	// scalar) from overlap DIFFERENCES between frames, so smooth real
	// background sampled by both frames in an overlap cancels out instead of
	// being fit from a single frame's own pixels. See
	// computeDifferenceSkyPlanes and docs/sky-background-matching-plan.md,
	// Stage 1.
	SkyMethodMatchPlane
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
	// EqualizeDisconnectedBackgrounds shifts independently matched overlap
	// components to the darkest component's background. It is deliberately
	// opt-in because disconnected footprints provide no photometric constraint
	// on their relative zero points.
	EqualizeDisconnectedBackgrounds bool
	// AmpPedestal enables NIRCam per-amplifier pedestal removal (see
	// amp_pedestal.go). Independent of Enabled/Method: it corrects an
	// intra-chip readout artifact, not inter-chip sky level, so it applies
	// even when overlap-based sky matching is off.
	AmpPedestal bool
	// RowDestripe enables NIRCam per-amplifier 1/f row-banding removal (see
	// row_destripe.go). Independent of Enabled/Method for the same reason as
	// AmpPedestal, and applied after it so row medians see no DC amp steps.
	RowDestripe bool
	// RowDestripeMaskPath is an optional binary FITS mask. Non-zero finite pixels
	// are excluded from row statistics. Dimensions must match each input exactly.
	RowDestripeMaskPath string
	// RowDestripeMaskDir is an optional directory containing per-input masks
	// named <input-stem>_rowmask.fits. Each mask applies only to the matching
	// calibrated NIRCam input.
	RowDestripeMaskDir string
	// RowDestripeMaskSigma is the positive residual threshold for automatic
	// source masking. Zero uses the default.
	RowDestripeMaskSigma float64
	// RowDestripeTrendWindow is the row smoothing window that preserves the
	// low-frequency trend. Zero uses the default.
	RowDestripeTrendWindow int
	// RowDestripeDirection is reserved for future column support. The current
	// implementation accepts only "" or "rows".
	RowDestripeDirection string
	// NIRCamWisp enables detector-fixed additive wisp template subtraction before
	// sky estimation. Templates are read from NIRCamWispTemplateDir; no runtime
	// downloads are attempted.
	NIRCamWisp bool
	// NIRCamWispTemplateDir is a local directory containing templates named
	// nircam_wisp_<detector>_<filter>.fits, for example
	// nircam_wisp_nrcb4_f200w.fits.
	NIRCamWispTemplateDir string
	// NIRCamWispAutoScale fits a non-negative scalar template amplitude. When
	// false, NIRCamWispScale is used directly.
	NIRCamWispAutoScale bool
	// NIRCamWispScale is the fixed non-negative template scale used when
	// NIRCamWispAutoScale is false.
	NIRCamWispScale float64
	// MIRIArtifactMask enables user-provided MIRI artifact masks. This masks
	// calibrated pixels only; it does not try to reproduce ramp-level shower
	// detection.
	MIRIArtifactMask bool
	// MIRIArtifactMaskPath is an optional binary FITS mask applied to MIRI inputs.
	MIRIArtifactMaskPath string
	// MIRIArtifactMaskDir is an optional directory containing per-input masks
	// named <input-stem>_miri_mask.fits.
	MIRIArtifactMaskDir string
}

type skyEdge struct {
	i, j   int
	delta  float64
	weight float64
	// cells is the number of overlap cells the edge's delta was measured
	// from, kept for the post-solve debug log.
	cells int
}

// sampleAccum holds a bounded set of raw samples for one coarse overlap
// cell. A plain mean is star-contaminated (a single bright pixel sampled
// into a cell skews it); keeping the raw values lets buildOverlapSampleMap
// report a median instead. Capped per cell (skyOverlapCellSampleCap) so a
// cell's memory stays bounded regardless of how many source pixels stride
// into it.
type sampleAccum struct {
	values []float64
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
	case SkyMethodLocalMin, SkyMethodGlobalMin, SkyMethodMatch, SkyMethodGlobalMinMatch, SkyMethodMatchPlane:
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
	needMaps := options.Method == SkyMethodMatch || options.Method == SkyMethodGlobalMinMatch || options.Method == SkyMethodMatchPlane
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
			pixels, _, _, perr := prepareFramePixels(planned[i], opts, 0, skyPlane{})
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
	var matchComponents [][]int
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
	case SkyMethodMatchPlane:
		planes, planeMatched, components := computeDifferenceSkyPlanesWithComponents(planned, maps, options)
		matched = planeMatched
		matchComponents = components
		for i := range planned {
			if planned[i].input.ReferenceOnly {
				continue
			}
			if matched[i] {
				// The plane's C term already carries the full correction
				// (including the equivalent of a scalar offset), so no
				// separate subtractSky is applied on top of it.
				skyPlanes[i] = planes[i]
			} else {
				// Same fallback as SkyMethodMatch: a chip/frame with no
				// overlap edges falls back to its own measured sky.
				subtractSky[i] = rawSky[i]
			}
		}
	case SkyMethodMatch, SkyMethodGlobalMinMatch:
		var rel []float64
		rel, matched, matchComponents = computeMatchedSkyOffsetsWithComponents(planned, maps, options)
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

	equalizeDisconnectedSkyComponents(planned, maps, rawSky, subtractSky, skyPlanes, matchComponents, options)

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
	// Only the median needs the values sorted. The mean is order-independent and
	// the mode bins by value, so sorting (an O(n log n) pass over up to ~2M
	// samples per frame) is pure waste for those two statistics.
	switch options.Stat {
	case SkyStatMean:
		mean, _ := meanAndSigma(work)
		return mean, nil
	case SkyStatMode:
		return histogramMode(work, options.Width), nil
	default:
		return quickSelectMedian(work), nil
	}
}

// quickSelectMedian returns the median of values, reordering the slice in place.
// It runs in O(n) average time using a three-way partition that stays linear
// even when the data contains many equal values (common in integer-quantised
// sky backgrounds), avoiding the O(n log n) cost of a full sort just to read out
// the middle element(s). The result is the exact median, identical to sorting.
func quickSelectMedian(values []float64) float64 {
	n := len(values)
	if n == 0 {
		return 0
	}
	mid := n / 2
	kth := selectKth(values, mid)
	if n%2 == 1 {
		return kth
	}
	// Even count: also need the (mid-1)th order statistic. selectKth left every
	// element in values[:mid] <= kth, so the (mid-1)th smallest is the maximum of
	// that left partition.
	lower := values[0]
	for _, v := range values[1:mid] {
		if v > lower {
			lower = v
		}
	}
	return 0.5 * (lower + kth)
}

// selectKth reorders a in place so that a[k] holds the k-th smallest element,
// with every element left of k <= a[k] and every element right of k >= a[k].
// It uses a three-way (Dutch-flag) partition with a median-of-three pivot, which
// keeps duplicate-heavy inputs linear and avoids the sorted-input worst case.
func selectKth(a []float64, k int) float64 {
	lo, hi := 0, len(a)-1
	for lo < hi {
		mid := lo + (hi-lo)/2
		pivot := medianOfThree(a[lo], a[mid], a[hi])
		lt, i, gt := lo, lo, hi
		for i <= gt {
			switch {
			case a[i] < pivot:
				a[lt], a[i] = a[i], a[lt]
				lt++
				i++
			case a[i] > pivot:
				a[i], a[gt] = a[gt], a[i]
				gt--
			default:
				i++
			}
		}
		// a[lo:lt] < pivot, a[lt:gt+1] == pivot, a[gt+1:hi+1] > pivot.
		switch {
		case k < lt:
			hi = lt - 1
		case k > gt:
			lo = gt + 1
		default:
			return a[k]
		}
	}
	return a[k]
}

func medianOfThree(a, b, c float64) float64 {
	if a < b {
		switch {
		case b < c:
			return b
		case a < c:
			return c
		default:
			return a
		}
	}
	switch {
	case a < c:
		return a
	case b < c:
		return c
	default:
		return b
	}
}

// histogramMode finds the modal background from an unsorted slice of values. It
// derives its own min/max in a single linear pass rather than requiring the
// caller to pre-sort.
func histogramMode(values []float64, widthSigma float64) float64 {
	if len(values) == 0 {
		return 0
	}
	_, sigma := meanAndSigma(values)
	minV, maxV := values[0], values[0]
	for _, v := range values {
		if v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
	}
	if sigma <= 0 {
		// Degenerate (effectively constant) data: every value equals minV, which
		// is also the median, so this matches the previous behaviour.
		return minV
	}
	binWidth := widthSigma * sigma
	if binWidth <= 0 {
		return minV
	}
	if maxV <= minV {
		return minV
	}
	bins := int(math.Ceil((maxV-minV)/binWidth)) + 1
	if bins < 1 {
		bins = 1
	}
	counts := make([]int, bins)
	for _, v := range values {
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
	offsets, matched, _ := computeMatchedSkyOffsetsWithComponents(planned, maps, options)
	return offsets, matched
}

func computeMatchedSkyOffsetsWithComponents(planned []plannedInput, maps []map[int64]float64, options SkysubOptions) ([]float64, []bool, [][]int) {
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
			edges = append(edges, skyEdge{i: i, j: j, delta: delta, weight: math.Sqrt(float64(len(diffs))), cells: len(diffs)})
		}
	}
	allComponents := activeSkyComponents(planned, edges)
	if len(edges) == 0 {
		return offsets, matched, allComponents
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
		compEdges, compOffsets = dropOutlierSkyEdgesAndResolve(component, compEdges, compOffsets)
		for _, idx := range component {
			matched[idx] = true
		}
		for idx, val := range compOffsets {
			offsets[idx] = val
		}
		logSkyMatchEdges(planned, compEdges, compOffsets)
	}
	return offsets, matched, allComponents
}

// dropOutlierSkyEdgesAndResolve computes each edge's residual against the
// already-solved offsets, and if any edge's residual sits more than
// skyEdgeResidualSigma sigma from the component's residual distribution,
// drops it and re-solves once. The drop is only applied if the surviving
// edges still connect every frame in the component — an outlier edge that is
// the sole connection for some frame is kept rather than isolating that
// frame, since a noisy relative offset is still better than none.
func dropOutlierSkyEdgesAndResolve(component []int, edges []skyEdge, offsets map[int]float64) ([]skyEdge, map[int]float64) {
	if len(edges) < 2 {
		return edges, offsets
	}
	residuals := make([]float64, len(edges))
	for k, e := range edges {
		residuals[k] = e.delta - (offsets[e.j] - offsets[e.i])
	}
	// Median/MAD rather than mean/sigma: a mean-based sigma over a handful of
	// edges is itself dragged around by the one gross outlier it's supposed
	// to detect (a "masking" effect), whereas the median stays put.
	center := quickSelectMedian(append([]float64(nil), residuals...))
	absDev := make([]float64, len(residuals))
	for k, r := range residuals {
		absDev[k] = math.Abs(r - center)
	}
	sigma := 1.4826 * quickSelectMedian(absDev)
	if sigma <= 0 {
		return edges, offsets
	}
	lo := center - skyEdgeResidualSigma*sigma
	hi := center + skyEdgeResidualSigma*sigma
	kept := make([]skyEdge, 0, len(edges))
	dropped := false
	for k, e := range edges {
		if residuals[k] < lo || residuals[k] > hi {
			dropped = true
			continue
		}
		kept = append(kept, e)
	}
	if !dropped || !sameSkyComponent(component, kept) {
		return edges, offsets
	}
	return kept, solveSkyComponent(component, kept)
}

// sameSkyComponent reports whether edges alone connect every member of
// component into one piece, without needing the full frame count that
// connectedSkyComponents requires.
func sameSkyComponent(component []int, edges []skyEdge) bool {
	if len(component) == 0 {
		return true
	}
	adj := make(map[int][]int, len(component))
	for _, e := range edges {
		adj[e.i] = append(adj[e.i], e.j)
		adj[e.j] = append(adj[e.j], e.i)
	}
	seen := make(map[int]bool, len(component))
	queue := []int{component[0]}
	seen[component[0]] = true
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, next := range adj[cur] {
			if !seen[next] {
				seen[next] = true
				queue = append(queue, next)
			}
		}
	}
	return len(seen) == len(component)
}

// logSkyMatchEdges reports each edge's post-solve residual (delta minus what
// the solved offsets predict), so a bad or inconsistent edge is visible in
// the same SKYSUB EDGE debuglog lines used for Stage 0-style diagnosis.
// Logged after solving (and any outlier re-solve) rather than at edge-build
// time so the residual reflects the final result.
func logSkyMatchEdges(planned []plannedInput, edges []skyEdge, offsets map[int]float64) {
	if !skyDebugLog {
		return
	}
	for _, e := range edges {
		residual := e.delta - (offsets[e.j] - offsets[e.i])
		debuglog.Log(fmt.Sprintf(
			"SKYSUB EDGE i=%s j=%s cells=%d delta=%.6f residual=%.6f",
			InputKey(planned[e.i].input),
			InputKey(planned[e.j].input),
			e.cells,
			e.delta,
			residual,
		))
	}
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

// positionedOverlapDiffs is overlapDiffsForEdge but keeps each cell's output
// mosaic position, needed to fit a plane (not just a scalar) from overlap
// differences. Returned samples are always mapJ - mapI at that cell's center.
func positionedOverlapDiffs(mapI, mapJ map[int64]float64) []skyPlaneSample {
	samples := make([]skyPlaneSample, 0, minInt(len(mapI), len(mapJ)))
	if len(mapI) <= len(mapJ) {
		for key, vi := range mapI {
			vj, ok := mapJ[key]
			if !ok {
				continue
			}
			x, y := overlapCellCenter(key)
			samples = append(samples, skyPlaneSample{x: x, y: y, z: vj - vi})
		}
	} else {
		for key, vj := range mapJ {
			vi, ok := mapI[key]
			if !ok {
				continue
			}
			x, y := overlapCellCenter(key)
			samples = append(samples, skyPlaneSample{x: x, y: y, z: vj - vi})
		}
	}
	return samples
}

type skyPlaneEdge struct {
	i, j    int
	samples []skyPlaneSample
	weight  float64
}

// computeDifferenceSkyPlanes solves per-frame sky correction PLANES directly
// from overlap DIFFERENCES between frames (Montage mBgModel-style), rather
// than from a frame's own pixels. This is the nebula-safe replacement for the
// former own-frame plane fit (see docs/sky-background-matching-plan.md,
// Stage 1): smooth real background sampled identically by both frames in an
// overlap cancels out of the difference, so it never gets fit and subtracted.
//
// Model: frame i's correction is b_i(x,y) = A_i*x + B_i*y + C_i in output
// mosaic coordinates. For every shared overlap cell k between frames i and j,
// the residual to minimize is
//
//	r_ijk = (v_jk - v_ik) - (b_j(x_k,y_k) - b_i(x_k,y_k))
//
// solved per connected component by weighted least squares. Each component's
// root frame (lowest index, matching computeMatchedSkyOffsets) is pinned to
// the zero plane for gauge fixing. matched[i] is true only for frames
// connected to at least one usable overlap edge; callers should fall back to
// a plain sky estimate for unmatched inputs, same as the scalar solve.
func computeDifferenceSkyPlanes(planned []plannedInput, maps []map[int64]float64, options SkysubOptions) ([]skyPlane, []bool) {
	planes, matched, _ := computeDifferenceSkyPlanesWithComponents(planned, maps, options)
	return planes, matched
}

func computeDifferenceSkyPlanesWithComponents(planned []plannedInput, maps []map[int64]float64, options SkysubOptions) ([]skyPlane, []bool, [][]int) {
	planes := make([]skyPlane, len(planned))
	matched := make([]bool, len(planned))

	var edges []skyPlaneEdge
	var connectivity []skyEdge
	for i := 0; i < len(planned); i++ {
		if planned[i].input.ReferenceOnly || len(maps[i]) == 0 {
			continue
		}
		for j := i + 1; j < len(planned); j++ {
			if planned[j].input.ReferenceOnly || len(maps[j]) == 0 {
				continue
			}
			samples := positionedOverlapDiffs(maps[i], maps[j])
			if len(samples) < skyMinOverlapCells {
				continue
			}
			edges = append(edges, skyPlaneEdge{i: i, j: j, samples: samples, weight: math.Sqrt(float64(len(samples)))})
			connectivity = append(connectivity, skyEdge{i: i, j: j})
		}
	}
	allComponents := activeSkyComponents(planned, connectivity)
	if len(edges) == 0 {
		return planes, matched, allComponents
	}

	components := connectedSkyComponents(len(planned), connectivity)
	for _, component := range components {
		if len(component) <= 1 {
			continue
		}
		compSet := make(map[int]struct{}, len(component))
		for _, idx := range component {
			compSet[idx] = struct{}{}
		}
		var compEdges []skyPlaneEdge
		for _, e := range edges {
			_, okI := compSet[e.i]
			_, okJ := compSet[e.j]
			if okI && okJ {
				compEdges = append(compEdges, e)
			}
		}
		regScale := componentOutputExtent(planned, component)
		compPlanes := solveSkyPlaneComponent(component, compEdges, regScale, options)
		logSkyPlaneEdges(planned, compEdges, compPlanes)
		for _, idx := range component {
			matched[idx] = true
			planes[idx] = compPlanes[idx]
		}
	}
	return planes, matched, allComponents
}

// logSkyPlaneEdges reports each edge's post-fit residual RMS (over all its
// cells, including any the solver's internal sigma-clip iterations dropped)
// so a bad or weakly-supported edge is visible the same way SKYSUB EDGE lines
// are for the scalar solve.
func logSkyPlaneEdges(planned []plannedInput, edges []skyPlaneEdge, planes map[int]skyPlane) {
	if !skyDebugLog {
		return
	}
	for _, e := range edges {
		var sumSq float64
		for _, sample := range e.samples {
			pred := planes[e.j].value(sample.x, sample.y) - planes[e.i].value(sample.x, sample.y)
			r := sample.z - pred
			sumSq += r * r
		}
		rms := 0.0
		if len(e.samples) > 0 {
			rms = math.Sqrt(sumSq / float64(len(e.samples)))
		}
		debuglog.Log(fmt.Sprintf(
			"SKYSUB PLANE EDGE i=%s j=%s cells=%d rms=%.6f",
			InputKey(planned[e.i].input),
			InputKey(planned[e.j].input),
			len(e.samples),
			rms,
		))
	}
}

// componentOutputExtent returns the largest output-mosaic-pixel extent (width
// or height of a frame's own footprint, mapped through its WCS) among a
// component's members. This bounds how far a fitted plane will be
// extrapolated beyond the overlap region it was measured in, which is what
// the slope regularization in solveSkyPlaneComponent needs to guard against —
// using the overlap sample extent instead would under-regularize exactly the
// small, poorly-constrained overlaps that need it most.
func componentOutputExtent(planned []plannedInput, component []int) float64 {
	extent := 0.0
	for _, idx := range component {
		if e := chipOutputExtent(planned[idx]); e > extent {
			extent = e
		}
	}
	if extent <= 0 {
		extent = 1
	}
	return extent
}

func chipOutputExtent(p plannedInput) float64 {
	w := float64(p.input.HDU.Data.Width)
	h := float64(p.input.HDU.Data.Height)
	minX, maxX := math.Inf(1), math.Inf(-1)
	minY, maxY := math.Inf(1), math.Inf(-1)
	corners := [4][2]float64{{0, 0}, {w, 0}, {0, h}, {w, h}}
	for _, c := range corners {
		x, y := p.mapPixel(c[0], c[1])
		if !isFiniteSky64(x) || !isFiniteSky64(y) {
			return 0
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
	dx, dy := maxX-minX, maxY-minY
	if dx > dy {
		return dx
	}
	return dy
}

const (
	// skyPlaneRegLambda scales the slope-regularization term added to the
	// joint plane solve: λ * regScale² * (A_i² + B_i²) per non-root frame.
	// This keeps weakly-connected frames (a small or single overlap) from
	// acquiring large, poorly-constrained gradients that would be
	// extrapolated across their whole footprint.
	skyPlaneRegLambda = 0.01

	// skyPlaneClipIters bounds the residual sigma-clip re-solve passes.
	skyPlaneClipIters = 2
)

// solveSkyPlaneComponent solves the joint per-frame plane system for one
// connected component, iterating with sigma-clipping on the residuals (same
// pattern as estimateSkyFromValues/the retired fitSkyPlane) so a handful of
// contaminated cells cannot dominate the fit.
func solveSkyPlaneComponent(component []int, edges []skyPlaneEdge, regScale float64, options SkysubOptions) map[int]skyPlane {
	result := make(map[int]skyPlane, len(component))
	root := component[0]
	for _, idx := range component {
		if idx < root {
			root = idx
		}
		result[idx] = skyPlane{}
	}
	result[root] = skyPlane{Valid: true}
	if len(component) == 1 || len(edges) == 0 {
		return result
	}

	varIndex := make(map[int]int, len(component)-1)
	order := make([]int, 0, len(component)-1)
	for _, idx := range component {
		if idx == root {
			continue
		}
		varIndex[idx] = len(order) * 3
		order = append(order, idx)
	}
	nVars := len(order) * 3
	if nVars == 0 {
		return result
	}
	regTerm := skyPlaneRegLambda * regScale * regScale

	active := make([][]bool, len(edges))
	for k, e := range edges {
		active[k] = make([]bool, len(e.samples))
		for si := range active[k] {
			active[k][si] = true
		}
	}

	clip := options.Clip
	if clip <= 0 {
		clip = skyPlaneClipIters
	}
	if clip > skyPlaneClipIters {
		clip = skyPlaneClipIters
	}
	planes := map[int]skyPlane{root: {Valid: true}}
	for iter := 0; iter <= clip; iter++ {
		ata := make([][]float64, nVars)
		for i := range ata {
			ata[i] = make([]float64, nVars)
		}
		atb := make([]float64, nVars)

		for k, e := range edges {
			w := e.weight
			if w <= 0 {
				w = 1
			}
			jBase, jActive := -1, e.j != root
			if jActive {
				jBase = varIndex[e.j]
			}
			iBase, iActive := -1, e.i != root
			if iActive {
				iBase = varIndex[e.i]
			}
			for si, sample := range e.samples {
				if !active[k][si] {
					continue
				}
				var pos [6]int
				var coeff [6]float64
				n := 0
				if jActive {
					pos[n], coeff[n] = jBase, sample.x
					n++
					pos[n], coeff[n] = jBase+1, sample.y
					n++
					pos[n], coeff[n] = jBase+2, 1
					n++
				}
				if iActive {
					pos[n], coeff[n] = iBase, -sample.x
					n++
					pos[n], coeff[n] = iBase+1, -sample.y
					n++
					pos[n], coeff[n] = iBase+2, -1
					n++
				}
				for a := 0; a < n; a++ {
					atb[pos[a]] += w * coeff[a] * sample.z
					for b := 0; b < n; b++ {
						ata[pos[a]][pos[b]] += w * coeff[a] * coeff[b]
					}
				}
			}
		}
		// Slope regularization: penalize A_i, B_i (not C_i) for every
		// non-root frame so under-constrained frames stay near flat rather
		// than extrapolating a wild gradient.
		for _, idx := range order {
			base := varIndex[idx]
			ata[base][base] += regTerm
			ata[base+1][base+1] += regTerm
		}

		sol := solveLinearSystem(ata, atb)
		planes = map[int]skyPlane{root: {Valid: true}}
		for idx, base := range varIndex {
			planes[idx] = skyPlane{A: sol[base], B: sol[base+1], C: sol[base+2], Valid: true}
		}

		if iter == clip {
			break
		}

		// Sigma-clip residuals for the next iteration.
		var residuals []float64
		type sampleRef struct{ edge, sample int }
		var refs []sampleRef
		for k, e := range edges {
			for si, sample := range e.samples {
				if !active[k][si] {
					continue
				}
				pred := planes[e.j].value(sample.x, sample.y) - planes[e.i].value(sample.x, sample.y)
				residuals = append(residuals, sample.z-pred)
				refs = append(refs, sampleRef{k, si})
			}
		}
		if len(residuals) == 0 {
			break
		}
		mean, sigma := meanAndSigma(residuals)
		if sigma <= 0 {
			break
		}
		lo := mean - options.LSigma*sigma
		hi := mean + options.USigma*sigma
		changed := false
		for idx, r := range residuals {
			if r < lo || r > hi {
				active[refs[idx].edge][refs[idx].sample] = false
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	for idx, plane := range planes {
		result[idx] = plane
	}
	return result
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

	// skyOverlapCellSampleCap bounds how many raw samples buildOverlapSampleMap
	// keeps per cell to compute a median. skyOverlapSamplesPerCellSide² is the
	// design target per cell, so this caps a bit above that rather than at it.
	skyOverlapCellSampleCap = 96

	// skyEdgeResidualSigma is the threshold (in residual sigma across a
	// component's edges) beyond which a sky-match edge is treated as an
	// outlier: dropped and the component re-solved once, so one bad overlap
	// cannot skew every frame's offset in its component.
	skyEdgeResidualSigma = 3.0
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
			if len(cur.values) >= skyOverlapCellSampleCap {
				continue
			}
			cur.values = append(cur.values, fv)
			accum[key] = cur
		}
	}
	out := make(map[int64]float64, len(accum))
	for key, cur := range accum {
		if len(cur.values) < skyOverlapMinCellSamples {
			continue
		}
		// Median rather than mean: a single bright star or nebula knot
		// sampled into a coarse cell should not bias the whole cell's
		// background estimate.
		out[key] = quickSelectMedian(cur.values)
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

// activeSkyComponents returns the complete graph partition for contributing
// data inputs. Unlike connectedSkyComponents, it includes singleton frames
// that have no usable overlap edge; those are independent background gauges
// too when disconnected-background equalization is enabled.
func activeSkyComponents(planned []plannedInput, edges []skyEdge) [][]int {
	adj := make([][]int, len(planned))
	for _, edge := range edges {
		adj[edge.i] = append(adj[edge.i], edge.j)
		adj[edge.j] = append(adj[edge.j], edge.i)
	}
	seen := make([]bool, len(planned))
	var components [][]int
	for i := range planned {
		if seen[i] || planned[i].input.ReferenceOnly {
			continue
		}
		queue := []int{i}
		seen[i] = true
		var component []int
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			component = append(component, cur)
			for _, next := range adj[cur] {
				if seen[next] || planned[next].input.ReferenceOnly {
					continue
				}
				seen[next] = true
				queue = append(queue, next)
			}
		}
		components = append(components, component)
	}
	return components
}

// equalizeDisconnectedSkyComponents removes the otherwise arbitrary scalar
// gauge between independent overlap graphs. Each frame contributes the median
// of its coarse map after its already-planned correction, then each component
// uses the median frame baseline. Brighter components receive a uniform extra
// subtraction to meet the darkest finite component. This is intentionally
// opt-in and non-photometric.
func equalizeDisconnectedSkyComponents(planned []plannedInput, maps []map[int64]float64, rawSky, subtractSky []float64, planes []skyPlane, components [][]int, options SkysubOptions) {
	if !options.EqualizeDisconnectedBackgrounds || len(components) < 2 {
		return
	}
	switch options.Method {
	case SkyMethodMatch, SkyMethodGlobalMinMatch, SkyMethodMatchPlane:
	default:
		return
	}

	baselines := make([]float64, len(components))
	for i := range baselines {
		baselines[i] = math.NaN()
	}
	target := math.Inf(1)
	validComponents := 0
	for componentIndex, component := range components {
		frameBaselines := make([]float64, 0, len(component))
		for _, frameIndex := range component {
			baseline, ok := correctedFrameBackground(frameIndex, maps, rawSky, subtractSky, planes)
			if ok {
				frameBaselines = append(frameBaselines, baseline)
			}
		}
		if len(frameBaselines) == 0 {
			continue
		}
		baseline := quickSelectMedian(frameBaselines)
		if !isFiniteSky64(baseline) {
			continue
		}
		baselines[componentIndex] = baseline
		validComponents++
		if baseline < target {
			target = baseline
		}
	}
	if validComponents < 2 || !isFiniteSky64(target) {
		return
	}

	for componentIndex, component := range components {
		baseline := baselines[componentIndex]
		if !isFiniteSky64(baseline) {
			continue
		}
		shift := baseline - target
		if shift < 0 {
			shift = 0
		}
		for _, frameIndex := range component {
			if options.Method == SkyMethodMatchPlane {
				if shift != 0 {
					planes[frameIndex].C += shift
					planes[frameIndex].Valid = true
				}
			} else {
				subtractSky[frameIndex] += shift
			}
		}
		if skyDebugLog {
			debuglog.Log(fmt.Sprintf(
				"SKYSUB COMPONENT members=%v baseline=%.6f target=%.6f shift=%.6f",
				component,
				baseline,
				target,
				shift,
			))
		}
	}
}

func correctedFrameBackground(frameIndex int, maps []map[int64]float64, rawSky, subtractSky []float64, planes []skyPlane) (float64, bool) {
	if frameIndex < 0 || frameIndex >= len(rawSky) || frameIndex >= len(subtractSky) || frameIndex >= len(planes) {
		return 0, false
	}
	values := make([]float64, 0)
	if frameIndex < len(maps) {
		values = make([]float64, 0, len(maps[frameIndex]))
		for key, value := range maps[frameIndex] {
			if !isFiniteSky64(value) {
				continue
			}
			x, y := overlapCellCenter(key)
			corrected := value - subtractSky[frameIndex] - planes[frameIndex].value(x, y)
			if isFiniteSky64(corrected) {
				values = append(values, corrected)
			}
		}
	}
	if len(values) > 0 {
		return quickSelectMedian(values), true
	}

	// A frame with no usable coarse cells is still an independent component.
	// Its robust full-frame sky is the best available estimate; plane C is a
	// sensible scalar fallback because no map coordinates exist to evaluate a
	// slope against.
	fallback := rawSky[frameIndex] - subtractSky[frameIndex]
	if planes[frameIndex].Valid {
		fallback -= planes[frameIndex].C
	}
	return fallback, isFiniteSky64(fallback)
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
