package starbench

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"net/http"
	"sort"
	"strconv"
)

func (s *server) render(w http.ResponseWriter, r *http.Request) {
	if dataset := r.URL.Query().Get("dataset"); dataset != "" && dataset != s.dataset.SHA256 {
		problem(w, 409, fmt.Errorf("the active science image changed; reload"))
		return
	}
	id := r.URL.Query().Get("region")
	region := Region{Width: s.image.Width, Height: s.image.Height}
	if id != "overview" {
		s.mu.Lock()
		found := false
		for _, v := range s.project.Regions {
			if v.ID == id {
				region = v
				found = true
				break
			}
		}
		s.mu.Unlock()
		if !found {
			problem(w, 404, fmt.Errorf("unknown region"))
			return
		}
	}
	gain := 20.
	if value := r.URL.Query().Get("gain"); value != "" {
		var err error
		gain, err = strconv.ParseFloat(value, 64)
		if err != nil || !finite(gain) || gain < 1 || gain > 200 {
			problem(w, 400, fmt.Errorf("stretch must be 1..200"))
			return
		}
	}
	size := 2048
	if id == "overview" {
		size = 900
	}
	img := renderCutout(s.image.Pixels, s.image.Width, region, gain, size)
	w.Header().Set("Content-Type", "image/png")
	png.Encode(w, img)
}

// renderCutout flips FITS rows for a Cartesian display (y increases upward).
// The transform is for display only; detection and scoring use linear science.
func renderCutout(pixels []float32, width int, r Region, gain float64, maxSize int) *image.NRGBA {
	scale := max(1, int(math.Ceil(float64(max(r.Width, r.Height))/float64(maxSize))))
	outW, outH := (r.Width+scale-1)/scale, (r.Height+scale-1)/scale
	values := make([]float64, outW*outH)
	sample := make([]float64, 0, min(len(values), 100000))
	sampleStride := max(1, len(values)/100000)
	for y := 0; y < outH; y++ {
		for x := 0; x < outW; x++ {
			sum := 0.
			n := 0
			for dy := 0; dy < scale && y*scale+dy < r.Height; dy++ {
				for dx := 0; dx < scale && x*scale+dx < r.Width; dx++ {
					v := float64(pixels[(r.Y+y*scale+dy)*width+r.X+x*scale+dx])
					if finite(v) {
						sum += v
						n++
					}
				}
			}
			i := y*outW + x
			values[i] = math.NaN()
			if n > 0 {
				values[i] = sum / float64(n)
				if i%sampleStride == 0 {
					sample = append(sample, values[i])
				}
			}
		}
	}
	sort.Float64s(sample)
	lo, hi := 0., 1.
	if len(sample) > 0 {
		lo = sample[len(sample)/10]
		hi = sample[min(len(sample)-1, int(float64(len(sample))*.998))]
		if hi <= lo {
			hi = lo + 1
		}
	}
	img := image.NewNRGBA(image.Rect(0, 0, outW, outH))
	den := math.Asinh(gain)
	for y := 0; y < outH; y++ {
		for x := 0; x < outW; x++ {
			v := values[y*outW+x]
			c := color.NRGBA{R: 45, G: 20, B: 45, A: 255}
			if finite(v) {
				n := math.Max(0, math.Min(1, (v-lo)/(hi-lo)))
				b := uint8(math.Round(255 * math.Asinh(gain*n) / den))
				c = color.NRGBA{R: b, G: b, B: b, A: 255}
			}
			img.SetNRGBA(x, outH-1-y, c)
		}
	}
	return img
}
