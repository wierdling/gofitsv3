package starbench

import (
	"fmt"
	"math"
	"net/http"
	"sort"

	"gofitsv3/internal/fitsio"
)

type centerPreview struct {
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Shift   float64 `json:"shift"`
	Message string  `json:"message"`
}

// suggestCenter is a pointing aid, not a source classifier. It operates only
// on linear science pixels; it has no access to a detector catalog or PSF.
// A smoothed local maximum seeds a background-subtracted aperture centroid.
func suggestCenter(im fitsio.ImageData, region Region, x, y, radius float64) (centerPreview, error) {
	if im.Width <= 0 || im.Height <= 0 || im.Width > int(^uint(0)>>1)/im.Height || len(im.Pixels) != im.Width*im.Height || !validRect(region.X, region.Y, region.Width, region.Height, im.Width, im.Height) {
		return centerPreview{}, fmt.Errorf("invalid science bounds")
	}
	if !finite(x) || !finite(y) || !inside(region, x, y, 0) || !finite(radius) || radius < 2 || radius > 12 {
		return centerPreview{}, fmt.Errorf("choose a point inside the region and a search radius of 2..12 pixels")
	}
	pixel := func(px, py int) (float64, bool) {
		if px < region.X || py < region.Y || px >= region.X+region.Width || py >= region.Y+region.Height {
			return 0, false
		}
		v := float64(im.Pixels[py*im.Width+px])
		return v, finite(v)
	}
	smooth := func(px, py int) (float64, bool) {
		sum := 0.
		weights := [3]float64{1, 2, 1}
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				v, ok := pixel(px+dx, py+dy)
				if !ok {
					return 0, false
				}
				sum += v * weights[dx+1] * weights[dy+1]
			}
		}
		return sum / 16, true
	}
	peak := math.Inf(-1)
	px, py := 0, 0
	for iy := int(math.Ceil(y - radius)); iy <= int(math.Floor(y+radius)); iy++ {
		for ix := int(math.Ceil(x - radius)); ix <= int(math.Floor(x+radius)); ix++ {
			if math.Hypot(float64(ix)-x, float64(iy)-y) > radius {
				continue
			}
			v, ok := smooth(ix, iy)
			if !ok || v <= peak {
				continue
			}
			maximum, strict := true, false
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					if dx == 0 && dy == 0 {
						continue
					}
					n, ok := smooth(ix+dx, iy+dy)
					if !ok || n > v {
						maximum = false
					}
					if ok && n < v {
						strict = true
					}
				}
			}
			if maximum && strict {
				peak = v
				px = ix
				py = iy
			}
		}
	}
	if !finite(peak) {
		return centerPreview{}, fmt.Errorf("no supported local peak nearby; reposition manually or change the search radius")
	}
	// Estimate local background and its scatter outside the centroid aperture.
	ring := []float64{}
	for dy := -8; dy <= 8; dy++ {
		for dx := -8; dx <= 8; dx++ {
			d := math.Hypot(float64(dx), float64(dy))
			if d < 6 || d > 8 {
				continue
			}
			if v, ok := pixel(px+dx, py+dy); ok {
				ring = append(ring, v)
			}
		}
	}
	if len(ring) < 60 {
		return centerPreview{}, fmt.Errorf("too little valid background near this point; reposition manually")
	}
	median := func(v []float64) float64 {
		sort.Float64s(v)
		n := len(v)
		if n%2 == 1 {
			return v[n/2]
		}
		return (v[n/2-1] + v[n/2]) / 2
	}
	background := median(ring)
	deviation := make([]float64, len(ring))
	for i, v := range ring {
		deviation[i] = math.Abs(v - background)
	}
	noise := 1.4826 * median(deviation)
	if peak-background <= math.Max(3*noise, 1e-12) {
		return centerPreview{}, fmt.Errorf("nearby peak has insufficient contrast for a reliable suggestion; reposition manually")
	}
	cx, cy := float64(px), float64(py)
	for iteration := 0; iteration < 8; iteration++ {
		sum, sx, sy := 0., 0., 0.
		valid, total := 0, 0
		for iy := int(math.Floor(cy - 4)); iy <= int(math.Ceil(cy+4)); iy++ {
			for ix := int(math.Floor(cx - 4)); ix <= int(math.Ceil(cx+4)); ix++ {
				d := math.Hypot(float64(ix)-cx, float64(iy)-cy)
				if d > 4 {
					continue
				}
				total++
				v, ok := pixel(ix, iy)
				if !ok {
					continue
				}
				valid++
				weight := math.Max(0, v-background)
				sum += weight
				sx += weight * float64(ix)
				sy += weight * float64(iy)
			}
		}
		if valid < total*9/10 || sum <= 0 {
			return centerPreview{}, fmt.Errorf("centroid aperture is incomplete; reposition manually")
		}
		nx, ny := sx/sum, sy/sum
		delta := math.Hypot(nx-cx, ny-cy)
		cx, cy = nx, ny
		if delta < .01 {
			break
		}
	}
	shift := math.Hypot(cx-x, cy-y)
	if !inside(region, cx, cy, 0) || shift > radius {
		return centerPreview{}, fmt.Errorf("suggested center left the search area; reposition closer first")
	}
	return centerPreview{X: cx, Y: cy, Shift: shift, Message: "Science-pixel centroid of the strongest nearby peak. Check the preview: a neighbor, nebular knot, blend, or saturated core can pull this position."}, nil
}

func (s *server) previewCenter(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RegionID string  `json:"regionId"`
		X        float64 `json:"x"`
		Y        float64 `json:"y"`
		Radius   float64 `json:"radius"`
	}
	if err := decode(w, r, &req); err != nil {
		problem(w, 400, err)
		return
	}
	s.mu.Lock()
	var region Region
	for _, v := range s.project.Regions {
		if v.ID == req.RegionID {
			region = v
			break
		}
	}
	s.mu.Unlock()
	if region.ID == "" {
		problem(w, 404, fmt.Errorf("unknown region"))
		return
	}
	preview, err := suggestCenter(s.image, region, req.X, req.Y, req.Radius)
	if err != nil {
		problem(w, 400, err)
		return
	}
	respond(w, preview)
}
