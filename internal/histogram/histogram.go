package histogram

import "math"

type Stats struct {
	Min   float64
	Max   float64
	Mean  float64
	Std   float64
	Count int
	Hist  [256]int
}

func Compute(pixels []float32) Stats {
	var s Stats
	if len(pixels) == 0 {
		return s
	}

	var sum float64
	var validCount int
	initialized := false

	for _, v := range pixels {
		fv := float64(v)
		if math.IsNaN(fv) || math.IsInf(fv, 0) {
			continue
		}
		if !initialized {
			s.Min = fv
			s.Max = fv
			initialized = true
		} else {
			if fv < s.Min {
				s.Min = fv
			}
			if fv > s.Max {
				s.Max = fv
			}
		}
		sum += fv
		validCount++
	}

	s.Count = validCount
	if validCount == 0 {
		return s
	}

	s.Mean = sum / float64(validCount)

	var variance float64
	rangeVal := s.Max - s.Min
	for _, v := range pixels {
		fv := float64(v)
		if math.IsNaN(fv) || math.IsInf(fv, 0) {
			continue
		}
		diff := fv - s.Mean
		variance += diff * diff
		idx := 0
		if rangeVal > 0 {
			norm := (fv - s.Min) / rangeVal
			idx = int(norm * 255.0)
		}
		if idx < 0 {
			idx = 0
		} else if idx > 255 {
			idx = 255
		}
		s.Hist[idx]++
	}

	s.Std = math.Sqrt(variance / float64(validCount))
	return s
}
