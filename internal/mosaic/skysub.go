package mosaic

import (
	"fmt"
	"math"
	"sort"
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
	i, j  int
	delta float64
}

type sampleAccum struct {
	sum   float64
	count int
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

func prepareSkysubWorkingPixels(planned []plannedInput, options SkysubOptions) ([][]float32, []bool, []float64, error) {
	working := make([][]float32, len(planned))
	applied := make([]bool, len(planned))
	skyValues := make([]float64, len(planned))
	for i := range skyValues {
		skyValues[i] = math.NaN()
	}

	for i := range planned {
		pixels := planned[i].input.HDU.Data.Pixels
		if planned[i].input.ReferenceOnly || !options.Enabled {
			working[i] = pixels
			continue
		}
		dup := make([]float32, len(pixels))
		copy(dup, pixels)
		working[i] = dup
	}
	if !options.Enabled {
		return working, applied, skyValues, nil
	}

	options = normalizeSkysubOptions(options)
	rawSky := make([]float64, len(planned))
	for i := range planned {
		if planned[i].input.ReferenceOnly {
			continue
		}
		sky, err := estimateSkyValue(working[i], options)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("estimate sky for %s: %w", InputKey(planned[i].input), err)
		}
		rawSky[i] = sky
	}

	subtractSky := make([]float64, len(planned))
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
		rel := computeMatchedSkyOffsets(planned, working, options)
		if options.Method == SkyMethodMatch {
			copy(subtractSky, rel)
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
				if planned[i].input.ReferenceOnly {
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
				subtractSky[i] = globalMin + (rel[i] - minRel)
			}
		}
	default:
		groupMin := map[string]float64{}
		for i := range planned {
			if planned[i].input.ReferenceOnly {
				continue
			}
			key := planned[i].input.Path
			if minVal, ok := groupMin[key]; !ok || rawSky[i] < minVal {
				groupMin[key] = rawSky[i]
			}
		}
		for i := range planned {
			if planned[i].input.ReferenceOnly {
				continue
			}
			subtractSky[i] = groupMin[planned[i].input.Path]
		}
	}

	for i := range planned {
		if planned[i].input.ReferenceOnly {
			continue
		}
		applySkySubInPlace(working[i], subtractSky[i])
		applied[i] = true
		skyValues[i] = subtractSky[i]
	}
	return working, applied, skyValues, nil
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

func estimateSkyValue(pixels []float32, options SkysubOptions) (float64, error) {
	values := make([]float64, 0, len(pixels))
	for _, px := range pixels {
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
	work := append([]float64(nil), values...)
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

func computeMatchedSkyOffsets(planned []plannedInput, working [][]float32, options SkysubOptions) []float64 {
	offsets := make([]float64, len(planned))
	edges := make([]skyEdge, 0)
	maps := make([]map[int64]float64, len(planned))
	for i := range planned {
		if planned[i].input.ReferenceOnly {
			continue
		}
		maps[i] = buildOverlapSampleMap(planned[i], working[i], options)
	}
	for i := 0; i < len(planned); i++ {
		if planned[i].input.ReferenceOnly || len(maps[i]) == 0 {
			continue
		}
		for j := i + 1; j < len(planned); j++ {
			if planned[j].input.ReferenceOnly || len(maps[j]) == 0 {
				continue
			}
			var diffs []float64
			if len(maps[i]) > len(maps[j]) {
				diffs = overlapDiffs(maps[j], maps[i], true)
			} else {
				diffs = overlapDiffs(maps[i], maps[j], false)
			}
			if len(diffs) == 0 {
				continue
			}
			delta, err := estimateSkyFromValues(diffs, options)
			if err != nil {
				continue
			}
			edges = append(edges, skyEdge{i: i, j: j, delta: delta})
		}
	}
	if len(edges) == 0 {
		return offsets
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
		for idx, val := range compOffsets {
			offsets[idx] = val
		}
	}
	return offsets
}

func overlapDiffs(primary, secondary map[int64]float64, invert bool) []float64 {
	diffs := make([]float64, 0, minInt(len(primary), len(secondary)))
	for key, a := range primary {
		b, ok := secondary[key]
		if !ok {
			continue
		}
		if invert {
			diffs = append(diffs, b-a)
		} else {
			diffs = append(diffs, b-a)
		}
	}
	return diffs
}

func buildOverlapSampleMap(p plannedInput, pixels []float32, options SkysubOptions) map[int64]float64 {
	width := p.input.HDU.Data.Width
	height := p.input.HDU.Data.Height
	stride := 1
	if total := width * height; total > 20000 {
		stride = int(math.Ceil(math.Sqrt(float64(total) / 20000.0)))
		if stride < 1 {
			stride = 1
		}
	}
	accum := make(map[int64]sampleAccum)
	for y := 0; y < height; y += stride {
		for x := 0; x < width; x += stride {
			idx := y*width + x
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
		if cur.count == 0 {
			continue
		}
		out[key] = cur.sum / float64(cur.count)
	}
	return out
}

func overlapCellKey(x, y float64) int64 {
	ix := int32(math.Round(x))
	iy := int32(math.Round(y))
	return (int64(ix) << 32) | int64(uint32(iy))
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
		for a, ca := range coeffs {
			atb[a] += ca * edge.delta
			for b, cb := range coeffs {
				ata[a][b] += ca * cb
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
