package processing

import (
	"fmt"
	"math"
)

// Legacy connected-component detector kept for comparison and possible rollback
// while the seed-based starless pipeline is under active development.
func DetectStarComponents(pixels []float32, width, height int, model BackgroundModel, thresholdSigma float64, minComponentArea, maxComponentArea int) ([]DetectedStarComponent, []bool, error) {
	components, _, accepted, _, err := DetectStarComponentsWithValidity(pixels, nil, width, height, model, thresholdSigma, minComponentArea, maxComponentArea, 0)
	return components, accepted, err
}

func DetectStarComponentsWithValidity(pixels []float32, valid []bool, width, height int, model BackgroundModel, thresholdSigma float64, minComponentArea, maxComponentArea int, minValidFraction float64) ([]DetectedStarComponent, []bool, []bool, []bool, error) {
	if width <= 0 || height <= 0 {
		return nil, nil, nil, nil, fmt.Errorf("width and height must be > 0")
	}
	if thresholdSigma <= 0 {
		return nil, nil, nil, nil, fmt.Errorf("threshold sigma must be > 0")
	}
	if minComponentArea < 1 {
		return nil, nil, nil, nil, fmt.Errorf("min component area must be >= 1")
	}
	total := width * height
	if total <= 0 {
		return nil, nil, nil, nil, fmt.Errorf("invalid image dimensions")
	}
	if len(pixels) != total {
		return nil, nil, nil, nil, fmt.Errorf("pixels length = %d, want %d", len(pixels), total)
	}
	if valid != nil && len(valid) != total {
		return nil, nil, nil, nil, fmt.Errorf("valid mask length = %d, want %d", len(valid), total)
	}
	if model.Width != width || model.Height != height {
		return nil, nil, nil, nil, fmt.Errorf("background model dimensions = %dx%d, want %dx%d", model.Width, model.Height, width, height)
	}
	if len(model.Background) != total || len(model.Sigma) != total {
		return nil, nil, nil, nil, fmt.Errorf("background model array lengths must equal image size")
	}
	candidate := make([]bool, total)
	for i, pix := range pixels {
		if valid != nil && !valid[i] {
			continue
		}
		if !isFiniteStar32(pix) {
			continue
		}
		bg := float64(model.Background[i])
		sigma := float64(model.Sigma[i])
		if sigma < localSigmaFloor || !isFiniteStar64(sigma) {
			sigma = localSigmaFloor
		}
		if float64(pix) >= bg+thresholdSigma*sigma {
			candidate[i] = true
		}
	}
	acceptedMask := make([]bool, total)
	rejectedMask := make([]bool, total)
	visited := make([]bool, total)
	dirs := [8][2]int{{-1, -1}, {0, -1}, {1, -1}, {-1, 0}, {1, 0}, {-1, 1}, {0, 1}, {1, 1}}
	components := make([]DetectedStarComponent, 0)
	for start := 0; start < total; start++ {
		if visited[start] || !candidate[start] {
			continue
		}
		queue := []int{start}
		visited[start] = true
		members := make([]int, 0, 16)
		minX, maxX := start%width, start%width
		minY, maxY := start/width, start/width
		peak := pixels[start]
		var sum, sumX, sumY, sumW float64
		validArea := 0
		for len(queue) > 0 {
			idx := queue[0]
			queue = queue[1:]
			members = append(members, idx)
			x := idx % width
			y := idx / width
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
			v := pixels[idx]
			if valid == nil || valid[idx] {
				validArea++
			}
			if v > peak {
				peak = v
			}
			sum += float64(v)
			wgt := float64(v)
			if bg := float64(model.Background[idx]); isFiniteStar64(bg) {
				wgt -= bg
			}
			if wgt < 0 || !isFiniteStar64(wgt) {
				wgt = 0
			}
			sumX += float64(x) * wgt
			sumY += float64(y) * wgt
			sumW += wgt
			for _, d := range dirs {
				nx, ny := x+d[0], y+d[1]
				if nx < 0 || nx >= width || ny < 0 || ny >= height {
					continue
				}
				nIdx := ny*width + nx
				if visited[nIdx] || !candidate[nIdx] {
					continue
				}
				visited[nIdx] = true
				queue = append(queue, nIdx)
			}
		}
		area := len(members)
		if area < minComponentArea {
			for _, idx := range members {
				rejectedMask[idx] = true
			}
			continue
		}
		if maxComponentArea > 0 && area > maxComponentArea {
			for _, idx := range members {
				rejectedMask[idx] = true
			}
			continue
		}
		validFraction := 1.0
		if area > 0 {
			validFraction = float64(validArea) / float64(area)
		}
		if validFraction < minValidFraction {
			for _, idx := range members {
				rejectedMask[idx] = true
			}
			continue
		}
		bboxWidth := maxX - minX + 1
		bboxHeight := maxY - minY + 1
		bboxArea := (maxX - minX + 1) * (maxY - minY + 1)
		fillRatio := float64(area) / float64(maxStarInt(bboxArea, 1))
		aspectRatio := 1.0
		if bboxWidth > bboxHeight && bboxHeight > 0 {
			aspectRatio = float64(bboxWidth) / float64(bboxHeight)
		} else if bboxWidth > 0 {
			aspectRatio = float64(bboxHeight) / float64(bboxWidth)
		}
		if valid != nil && bboxArea > 0 {
			bboxValid := 0
			for y := minY; y <= maxY; y++ {
				row := y * width
				for x := minX; x <= maxX; x++ {
					if valid[row+x] {
						bboxValid++
					}
				}
			}
			if float64(bboxValid)/float64(bboxArea) < minValidFraction {
				for _, idx := range members {
					rejectedMask[idx] = true
				}
				continue
			}
		}
		peakContrast := float64(peak) - float64(model.Background[start])
		if peakContrast < 0 {
			peakContrast = 0
		}
		coreThreshold := float64(model.Background[start]) + 0.65*peakContrast
		coreArea := 0
		for _, idx := range members {
			if float64(pixels[idx]) >= coreThreshold {
				coreArea++
			}
		}
		coreFraction := float64(coreArea) / float64(maxStarInt(area, 1))
		compactCore := coreArea > 0 && (coreArea <= maxStarInt(9, area/4) || peakContrast >= 0.55)
		diffuseLarge := area > 64 && peakContrast < 0.35
		elongatedWeak := aspectRatio > 6 && peakContrast < 0.45
		boxyDiffuse := area > 25 && fillRatio > 0.85 && peakContrast < 0.25
		broadCore := area > 32 && coreFraction > 0.2
		weakCore := (!compactCore || coreFraction > 0.45) && area > 16 && peakContrast < 0.55
		if diffuseLarge || elongatedWeak || boxyDiffuse || broadCore || weakCore {
			for _, idx := range members {
				rejectedMask[idx] = true
			}
			continue
		}
		centroidX := float64(minX+maxX) / 2
		centroidY := float64(minY+maxY) / 2
		if sumW > 0 {
			centroidX = sumX / sumW
			centroidY = sumY / sumW
		}
		for _, idx := range members {
			acceptedMask[idx] = true
		}
		components = append(components, DetectedStarComponent{MinX: minX, MinY: minY, MaxX: maxX, MaxY: maxY, Area: area, ValidArea: validArea, BBoxWidth: bboxWidth, BBoxHeight: bboxHeight, FillRatio: fillRatio, AspectRatio: aspectRatio, CentroidX: centroidX, CentroidY: centroidY, PeakValue: peak, MeanValue: float32(sum / float64(area)), PeakContrast: peakContrast, CoreArea: coreArea, CompactCore: compactCore})
	}
	return components, candidate, acceptedMask, rejectedMask, nil
}

func BuildDilatedStarMask(width, height int, components []DetectedStarComponent, componentMask []bool, baseRadius, brightnessRadius float64, maxRadius int) ([]bool, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("width and height must be > 0")
	}
	if baseRadius < 0 {
		return nil, fmt.Errorf("base radius must be >= 0")
	}
	if brightnessRadius < 0 {
		return nil, fmt.Errorf("brightness radius must be >= 0")
	}
	if maxRadius < 0 {
		return nil, fmt.Errorf("max radius must be >= 0")
	}
	total := width * height
	if componentMask != nil && len(componentMask) != total {
		return nil, fmt.Errorf("component mask length = %d, want %d", len(componentMask), total)
	}
	mask := make([]bool, total)
	for _, c := range components {
		areaTerm := math.Sqrt(float64(maxStarInt(c.Area, 1)))
		peakTerm := math.Sqrt(math.Max(float64(c.PeakValue), 0)) * brightnessRadius
		radius := baseRadius + areaTerm + peakTerm
		if maxRadius > 0 && radius > float64(maxRadius) {
			radius = float64(maxRadius)
		}
		if radius < 0 {
			radius = 0
		}
		cr := int(math.Ceil(radius))
		r2 := radius * radius
		usedComponentPixels := false
		if componentMask != nil {
			for sy := c.MinY; sy <= c.MaxY; sy++ {
				row := sy * width
				for sx := c.MinX; sx <= c.MaxX; sx++ {
					srcIdx := row + sx
					if !componentMask[srcIdx] {
						continue
					}
					usedComponentPixels = true
					x0 := maxStarInt(0, sx-cr)
					x1 := minInt(width-1, sx+cr)
					y0 := maxStarInt(0, sy-cr)
					y1 := minInt(height-1, sy+cr)
					for y := y0; y <= y1; y++ {
						for x := x0; x <= x1; x++ {
							dx, dy := float64(x-sx), float64(y-sy)
							if dx*dx+dy*dy <= r2 {
								mask[y*width+x] = true
							}
						}
					}
				}
			}
		}
		if usedComponentPixels {
			continue
		}
		cx, cy := c.CentroidX, c.CentroidY
		x0 := maxStarInt(0, int(math.Floor(cx))-cr)
		x1 := minInt(width-1, int(math.Ceil(cx))+cr)
		y0 := maxStarInt(0, int(math.Floor(cy))-cr)
		y1 := minInt(height-1, int(math.Ceil(cy))+cr)
		for y := y0; y <= y1; y++ {
			for x := x0; x <= x1; x++ {
				dx, dy := float64(x)-cx, float64(y)-cy
				if dx*dx+dy*dy <= r2 {
					mask[y*width+x] = true
				}
			}
		}
	}
	return mask, nil
}

func appendBrightCoreComponents(components []DetectedStarComponent, acceptedMask []bool, pixels []float32, valid []bool, width, height int, model BackgroundModel, thresholdSigma float64) ([]DetectedStarComponent, []bool) {
	if width < 3 || height < 3 {
		return components, acceptedMask
	}
	existsNearby := func(x, y int) bool {
		for _, c := range components {
			dx := c.CentroidX - float64(x)
			dy := c.CentroidY - float64(y)
			if dx*dx+dy*dy <= 9 {
				return true
			}
		}
		return false
	}
	bestIdx := -1
	bestVal := math.Inf(-1)
	for idx, v := range pixels {
		if valid != nil && !valid[idx] {
			continue
		}
		if !isFiniteStar32(v) {
			continue
		}
		if float64(v) > bestVal {
			bestVal = float64(v)
			bestIdx = idx
		}
	}
	if bestIdx >= 0 {
		x := bestIdx % width
		y := bestIdx / width
		bg := float64(model.Background[bestIdx])
		sigma := math.Max(float64(model.Sigma[bestIdx]), localSigmaFloor)
		contrast := bestVal - bg
		ringMean := localRingMean(pixels, valid, width, height, x, y, 2)
		localProminence := bestVal - ringMean
		if !acceptedMask[bestIdx] && !existsNearby(x, y) && (contrast >= math.Max(0.25, thresholdSigma*sigma*1.5) || (bestVal >= 0.8 && localProminence >= 0.03)) {
			acceptedMask[bestIdx] = true
			components = append(components, DetectedStarComponent{
				MinX: x, MinY: y, MaxX: x, MaxY: y,
				Area: 1, ValidArea: 1, BBoxWidth: 1, BBoxHeight: 1,
				FillRatio: 1, AspectRatio: 1, CentroidX: float64(x), CentroidY: float64(y),
				PeakValue: pixels[bestIdx], MeanValue: pixels[bestIdx], PeakContrast: contrast, CoreArea: 1, CompactCore: true,
			})
		}
	}
	for y := 1; y < height-1; y++ {
		for x := 1; x < width-1; x++ {
			idx := y*width + x
			if valid != nil && !valid[idx] {
				continue
			}
			if acceptedMask[idx] || !isFiniteStar32(pixels[idx]) || existsNearby(x, y) {
				continue
			}
			v := float64(pixels[idx])
			bg := float64(model.Background[idx])
			sigma := math.Max(float64(model.Sigma[idx]), localSigmaFloor)
			contrast := v - bg
			isLocalMax := true
			var ringSum float64
			ringCount := 0
			for ny := y - 1; ny <= y+1; ny++ {
				for nx := x - 1; nx <= x+1; nx++ {
					if nx == x && ny == y {
						continue
					}
					nIdx := ny*width + nx
					if valid != nil && !valid[nIdx] {
						continue
					}
					nv := float64(pixels[nIdx])
					if nv > v {
						isLocalMax = false
						break
					}
					ringSum += nv
					ringCount++
				}
				if !isLocalMax {
					break
				}
			}
			if !isLocalMax || ringCount == 0 {
				continue
			}
			ringMean := ringSum / float64(ringCount)
			localProminence := v - ringMean
			if contrast < math.Max(0.25, thresholdSigma*sigma*1.5) && !(v >= 0.8 && localProminence >= 0.03) {
				continue
			}
			acceptedMask[idx] = true
			components = append(components, DetectedStarComponent{
				MinX: x, MinY: y, MaxX: x, MaxY: y,
				Area: 1, ValidArea: 1, BBoxWidth: 1, BBoxHeight: 1,
				FillRatio: 1, AspectRatio: 1, CentroidX: float64(x), CentroidY: float64(y),
				PeakValue: pixels[idx], MeanValue: pixels[idx], PeakContrast: contrast, CoreArea: 1, CompactCore: true,
			})
		}
	}
	return components, acceptedMask
}

func localRingMean(pixels []float32, valid []bool, width, height, cx, cy, radius int) float64 {
	var sum float64
	count := 0
	x0 := maxStarInt(0, cx-radius)
	x1 := minInt(width-1, cx+radius)
	y0 := maxStarInt(0, cy-radius)
	y1 := minInt(height-1, cy+radius)
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			if x == cx && y == cy {
				continue
			}
			idx := y*width + x
			if valid != nil && !valid[idx] {
				continue
			}
			v := pixels[idx]
			if !isFiniteStar32(v) {
				continue
			}
			sum += float64(v)
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return sum / float64(count)
}

func isLocalMaximum(pixels []float32, valid []bool, width, height, cx, cy, radius int) bool {
	center := float64(pixels[cy*width+cx])
	for y := maxStarInt(0, cy-radius); y <= minInt(height-1, cy+radius); y++ {
		for x := maxStarInt(0, cx-radius); x <= minInt(width-1, cx+radius); x++ {
			if x == cx && y == cy {
				continue
			}
			idx := y*width + x
			if valid != nil && !valid[idx] {
				continue
			}
			if float64(pixels[idx]) > center {
				return false
			}
		}
	}
	return true
}
