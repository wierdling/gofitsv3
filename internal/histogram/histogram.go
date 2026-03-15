package histogram

// Stats holds basic histogram info.
type Stats struct {
	Min   float64
	Max   float64
	Mean  float64
	Std   float64
	Count int
	Hist  [256]int
}

func Compute(pixels []float64) Stats {
	var s Stats
	s.Count = len(pixels)
	if len(pixels) == 0 {
		return s
	}
	s.Min = pixels[0]
	s.Max = pixels[0]
	var sum float64
	for _, v := range pixels {
		if v < s.Min {
			s.Min = v
		}
		if v > s.Max {
			s.Max = v
		}
		sum += v
		idx := int(v * 255)
		if idx < 0 {
			idx = 0
		}
		if idx > 255 {
			idx = 255
		}
		s.Hist[idx]++
	}
	s.Mean = sum / float64(len(pixels))
	// compute std
	var variance float64
	for _, v := range pixels {
		diff := v - s.Mean
		variance += diff * diff
	}
	s.Std = variance / float64(len(pixels))
	return s
}
