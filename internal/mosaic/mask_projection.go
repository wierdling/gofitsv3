package mosaic

import (
	"context"
	"fmt"
	"math"

	"gofitsv3/internal/processing"
)

type MaskOutputGeometry struct {
	Width   int
	Height  int
	OriginX float64
	OriginY float64
	Scale   float64
}

type MaskProjectionOptions struct {
	Ctx context.Context
	// Progress reports completed detector rows and total detector rows.
	Progress func(done, total int)
	// ConservativeRadius expands selected authoring pixels in authoring-grid
	// pixels. Zero uses the version-1 default.
	ConservativeRadius float64
}

type DetectorOutputMapper struct {
	planned plannedInput
	geom    MaskOutputGeometry
}

func NewDetectorOutputMapper(input, reference Input, geom MaskOutputGeometry) (*DetectorOutputMapper, error) {
	if input.HDU.Data.Width <= 0 || input.HDU.Data.Height <= 0 {
		return nil, fmt.Errorf("input %s has invalid dimensions %dx%d", InputKey(input), input.HDU.Data.Width, input.HDU.Data.Height)
	}
	if reference.HDU.Data.Width <= 0 || reference.HDU.Data.Height <= 0 {
		return nil, fmt.Errorf("reference %s has invalid dimensions %dx%d", InputKey(reference), reference.HDU.Data.Width, reference.HDU.Data.Height)
	}
	if geom.Scale <= 0 || !isFinite64(geom.Scale) {
		return nil, fmt.Errorf("mask output geometry has invalid scale %v", geom.Scale)
	}
	if geom.Width <= 0 || geom.Height <= 0 {
		return nil, fmt.Errorf("mask output geometry has invalid dimensions %dx%d", geom.Width, geom.Height)
	}
	mapper, err := processing.NewWCSMapperToLinearRef(input.HDU.Header, input.D2IX, input.D2IY, reference.HDU.Header)
	if err != nil {
		return nil, err
	}
	p := plannedInput{
		input:       input,
		sourceToRef: composePlacementTransform(processing.IdentityTransform(), input),
		mapper:      mapper,
	}
	return &DetectorOutputMapper{planned: p, geom: geom}, nil
}

func (m *DetectorOutputMapper) MapDetectorToOutput(x, y float64) (float64, float64) {
	return m.planned.mapOutputPixel(x, y, m.geom.OriginX, m.geom.OriginY, m.geom.Scale)
}

func ProjectAuthoringMaskToDetector(input, reference Input, geom MaskOutputGeometry, authoringMask []bool, options MaskProjectionOptions) ([]bool, error) {
	if err := validateAuthoringMask(authoringMask, geom.Width, geom.Height); err != nil {
		return nil, err
	}
	mapper, err := NewDetectorOutputMapper(input, reference, geom)
	if err != nil {
		return nil, err
	}
	width := input.HDU.Data.Width
	height := input.HDU.Data.Height
	out := make([]bool, width*height)
	radius := options.ConservativeRadius
	if radius == 0 {
		radius = 0.5
	}
	if radius < 0 {
		radius = 0
	}
	for y := 0; y < height; y++ {
		if options.Ctx != nil && options.Ctx.Err() != nil {
			return nil, ErrCancelled
		}
		row := y * width
		for x := 0; x < width; x++ {
			outX, outY := mapper.MapDetectorToOutput(float64(x), float64(y))
			if authoringMaskContains(authoringMask, geom.Width, geom.Height, outX, outY, radius) {
				out[row+x] = true
			}
		}
		if options.Progress != nil {
			options.Progress(y+1, height)
		}
	}
	return out, nil
}

type MaskPoint struct {
	X float64
	Y float64
}

func RasterizeRectangleMask(width, height int, x0, y0, x1, y1 float64) ([]bool, error) {
	mask, err := NewZeroArtifactMask(width, height)
	if err != nil {
		return nil, err
	}
	minX := int(math.Floor(math.Min(x0, x1)))
	maxX := int(math.Ceil(math.Max(x0, x1)))
	minY := int(math.Floor(math.Min(y0, y1)))
	maxY := int(math.Ceil(math.Max(y0, y1)))
	minX, maxX, minY, maxY, ok := clippedRect(minX, maxX, minY, maxY, width, height)
	if !ok {
		return mask, nil
	}
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			mask[y*width+x] = true
		}
	}
	return mask, nil
}

func RasterizeBrushStrokeMask(width, height int, points []MaskPoint, radius float64) ([]bool, error) {
	mask, err := NewZeroArtifactMask(width, height)
	if err != nil {
		return nil, err
	}
	if radius < 0 {
		radius = 0
	}
	for i, pt := range points {
		rasterizeBrushCircle(mask, width, height, pt.X, pt.Y, radius)
		if i > 0 {
			rasterizeBrushSegment(mask, width, height, points[i-1], pt, radius)
		}
	}
	return mask, nil
}

func RasterizePolygonMask(width, height int, points []MaskPoint) ([]bool, error) {
	mask, err := NewZeroArtifactMask(width, height)
	if err != nil {
		return nil, err
	}
	if len(points) < 3 {
		return mask, nil
	}
	for y := 0; y < height; y++ {
		py := float64(y)
		for x := 0; x < width; x++ {
			if pointInPolygon(float64(x), py, points) {
				mask[y*width+x] = true
			}
		}
	}
	return mask, nil
}

func RasterizeThresholdMask(pixels []float32, width, height int, x0, y0, x1, y1 int, minValue, maxValue float32) ([]bool, error) {
	if len(pixels) != width*height {
		return nil, fmt.Errorf("threshold pixels len=%d vs dimensions %dx%d", len(pixels), width, height)
	}
	mask, err := NewZeroArtifactMask(width, height)
	if err != nil {
		return nil, err
	}
	if minValue > maxValue {
		minValue, maxValue = maxValue, minValue
	}
	minX := clampIntLocal(min(x0, x1), 0, width-1)
	maxX := clampIntLocal(max(x0, x1), 0, width-1)
	minY := clampIntLocal(min(y0, y1), 0, height-1)
	maxY := clampIntLocal(max(y0, y1), 0, height-1)
	if min(x0, x1) >= width || max(x0, x1) < 0 || min(y0, y1) >= height || max(y0, y1) < 0 {
		return mask, nil
	}
	for y := minY; y <= maxY; y++ {
		row := y * width
		for x := minX; x <= maxX; x++ {
			v := pixels[row+x]
			if isFinite32(v) && v >= minValue && v <= maxValue {
				mask[row+x] = true
			}
		}
	}
	return mask, nil
}

func SelectConnectedThresholdComponent(mask []bool, width, height, seedX, seedY int) ([]bool, error) {
	if err := validateArtifactMaskDimensions(mask, width, height); err != nil {
		return nil, err
	}
	out := make([]bool, len(mask))
	if width == 0 || height == 0 {
		return out, nil
	}
	if seedX >= 0 && seedX < width && seedY >= 0 && seedY < height && mask[seedY*width+seedX] {
		fillConnectedComponent(mask, out, width, height, seedX, seedY)
		return out, nil
	}
	bestStart := -1
	bestCount := 0
	seen := make([]bool, len(mask))
	for i, v := range mask {
		if !v || seen[i] {
			continue
		}
		count := measureConnectedComponent(mask, seen, width, height, i%width, i/width)
		if count > bestCount {
			bestCount = count
			bestStart = i
		}
	}
	if bestStart >= 0 {
		fillConnectedComponent(mask, out, width, height, bestStart%width, bestStart/width)
	}
	return out, nil
}

func CountMaskPixels(mask []bool) int {
	count := 0
	for _, v := range mask {
		if v {
			count++
		}
	}
	return count
}

func validateAuthoringMask(mask []bool, width, height int) error {
	if width <= 0 || height <= 0 {
		return fmt.Errorf("authoring mask dimensions must be positive: %dx%d", width, height)
	}
	if len(mask) != width*height {
		return fmt.Errorf("authoring mask dimension mismatch: mask len=%d vs grid %dx%d", len(mask), width, height)
	}
	return nil
}

func measureConnectedComponent(mask, seen []bool, width, height, startX, startY int) int {
	queue := []int{startY*width + startX}
	seen[startY*width+startX] = true
	count := 0
	for len(queue) > 0 {
		idx := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		count++
		x := idx % width
		y := idx / width
		for _, n := range componentNeighbors(x, y, width, height) {
			if seen[n] || !mask[n] {
				continue
			}
			seen[n] = true
			queue = append(queue, n)
		}
	}
	return count
}

func fillConnectedComponent(mask, out []bool, width, height, startX, startY int) {
	queue := []int{startY*width + startX}
	out[startY*width+startX] = true
	for len(queue) > 0 {
		idx := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		x := idx % width
		y := idx / width
		for _, n := range componentNeighbors(x, y, width, height) {
			if out[n] || !mask[n] {
				continue
			}
			out[n] = true
			queue = append(queue, n)
		}
	}
}

func componentNeighbors(x, y, width, height int) []int {
	neighbors := make([]int, 0, 4)
	if x > 0 {
		neighbors = append(neighbors, y*width+x-1)
	}
	if x+1 < width {
		neighbors = append(neighbors, y*width+x+1)
	}
	if y > 0 {
		neighbors = append(neighbors, (y-1)*width+x)
	}
	if y+1 < height {
		neighbors = append(neighbors, (y+1)*width+x)
	}
	return neighbors
}

func authoringMaskContains(mask []bool, width, height int, x, y, radius float64) bool {
	if !isFinite64(x) || !isFinite64(y) {
		return false
	}
	minX := int(math.Floor(x - radius))
	maxX := int(math.Ceil(x + radius))
	minY := int(math.Floor(y - radius))
	maxY := int(math.Ceil(y + radius))
	for yy := minY; yy <= maxY; yy++ {
		if yy < 0 || yy >= height {
			continue
		}
		for xx := minX; xx <= maxX; xx++ {
			if xx < 0 || xx >= width {
				continue
			}
			if math.Abs(float64(xx)-x) <= radius && math.Abs(float64(yy)-y) <= radius && mask[yy*width+xx] {
				return true
			}
		}
	}
	return false
}

func rasterizeBrushCircle(mask []bool, width, height int, cx, cy, radius float64) {
	r2 := radius * radius
	minX := clampIntLocal(int(math.Floor(cx-radius)), 0, width-1)
	maxX := clampIntLocal(int(math.Ceil(cx+radius)), 0, width-1)
	minY := clampIntLocal(int(math.Floor(cy-radius)), 0, height-1)
	maxY := clampIntLocal(int(math.Ceil(cy+radius)), 0, height-1)
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			dx := float64(x) - cx
			dy := float64(y) - cy
			if dx*dx+dy*dy <= r2 {
				mask[y*width+x] = true
			}
		}
	}
}

func rasterizeBrushSegment(mask []bool, width, height int, a, b MaskPoint, radius float64) {
	dx := b.X - a.X
	dy := b.Y - a.Y
	steps := int(math.Ceil(math.Hypot(dx, dy)))
	if steps < 1 {
		steps = 1
	}
	for i := 0; i <= steps; i++ {
		t := float64(i) / float64(steps)
		rasterizeBrushCircle(mask, width, height, a.X+dx*t, a.Y+dy*t, radius)
	}
}

func pointInPolygon(x, y float64, points []MaskPoint) bool {
	inside := false
	j := len(points) - 1
	for i := range points {
		yi := points[i].Y
		yj := points[j].Y
		if (yi > y) != (yj > y) {
			xi := points[i].X
			xj := points[j].X
			crossX := (xj-xi)*(y-yi)/(yj-yi) + xi
			if x < crossX {
				inside = !inside
			}
		}
		j = i
	}
	return inside
}

func clampIntLocal(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clippedRect(minX, maxX, minY, maxY, width, height int) (int, int, int, int, bool) {
	if minX >= width || maxX < 0 || minY >= height || maxY < 0 {
		return 0, 0, 0, 0, false
	}
	return clampIntLocal(minX, 0, width-1),
		clampIntLocal(maxX, 0, width-1),
		clampIntLocal(minY, 0, height-1),
		clampIntLocal(maxY, 0, height-1),
		true
}
