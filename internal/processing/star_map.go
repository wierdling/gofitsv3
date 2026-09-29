package processing

import (
	"context"
	"fmt"
	"math"
	"sort"
)

// StarMapOptions controls detection on linear, unstretched science pixels.
// Scores are shape diagnostics, not calibrated probabilities.
type StarMapOptions struct {
	MinSNR      float64
	MaxResidual float64
	FWHM        float64 // zero estimates the width from compact, isolated candidates
	Progress    func(string, int, int)
}

type StarMapSource struct {
	ID                                           int
	X, Y, FWHM, Radius, SNR, Residual, Amplitude float64
	Status, Reason                               string
	Saturated                                    bool
	Usable, Confirmed                            int
	Override                                     string // empty, accept or reject; automatic Status is preserved
}

func (s StarMapSource) Accepted() bool {
	return s.Override == "accept" || (s.Override != "reject" && s.Status == "accepted")
}

type StarMap struct {
	Width, Height int
	Sources       []StarMapSource
	FWHM          float64
	Empirical     []float64 // normalized radial profile, half-pixel radial bins
	Options       StarMapOptions
}

func DefaultStarMapOptions() StarMapOptions { return StarMapOptions{MinSNR: 7, MaxResidual: .22} }

func starFinite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func starMedian(a []float64) float64 {
	if len(a) == 0 {
		return 0
	}
	sort.Float64s(a)
	n := len(a)
	if n%2 == 0 {
		return (a[n/2-1] + a[n/2]) / 2
	}
	return a[n/2]
}

// DetectStarMap uses local residual maxima only to propose candidates. Each is
// then fit with a PSF plus a sloping background and compared to extended shapes.
// saturated is detector evidence in THIS pixel grid; nil means unavailable.
func DetectStarMap(ctx context.Context, pixels []float32, w, h int, saturated []bool, opt StarMapOptions) (*StarMap, error) {
	if w < 25 || h < 25 || w > int(^uint(0)>>1)/h || len(pixels) != w*h {
		return nil, fmt.Errorf("star map requires a valid image at least 25 x 25")
	}
	if len(saturated) != 0 && len(saturated) != len(pixels) {
		return nil, fmt.Errorf("saturation dimensions differ from science")
	}
	if opt.MinSNR == 0 {
		opt.MinSNR = 7
	}
	if opt.MaxResidual == 0 {
		opt.MaxResidual = .22
	}
	if !starFinite(opt.MinSNR) || opt.MinSNR < 3 || !starFinite(opt.MaxResidual) || opt.MaxResidual <= 0 || opt.MaxResidual > 1 || !starFinite(opt.FWHM) || opt.FWHM < 0 || opt.FWHM > 8 {
		return nil, fmt.Errorf("invalid star-map options")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Local tile noise of neighbor differences reduces bias from slowly changing
	// nebula. A per-candidate annulus estimate below guards sharp structures.
	const tile = 64
	nx, ny := (w+tile-1)/tile, (h+tile-1)/tile
	noise := make([]float64, nx*ny)
	finiteCount := 0
	for ty := 0; ty < ny; ty++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for tx := 0; tx < nx; tx++ {
			d := make([]float64, 0, tile*tile/4)
			for y := ty * tile; y < min((ty+1)*tile, h); y += 2 {
				for x := tx * tile; x < min((tx+1)*tile, w)-1; x += 2 {
					a, b := float64(pixels[y*w+x]), float64(pixels[y*w+x+1])
					if starFinite(a) && starFinite(b) {
						d = append(d, math.Abs(a-b))
						finiteCount++
					}
				}
			}
			noise[ty*nx+tx] = math.Max(starMedian(d)*1.0484, 1e-12)
		}
	}
	if finiteCount == 0 {
		return nil, fmt.Errorf("image has no valid science samples")
	}
	result := &StarMap{Width: w, Height: h, Options: opt}
	// Smooth only for locating peaks, retaining the original pixels for fitting.
	smooth := make([]float32, len(pixels))
	for y := 1; y < h-1; y++ {
		if y%64 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		for x := 1; x < w-1; x++ {
			sum, weight := 0., 0.
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					v := float64(pixels[(y+dy)*w+x+dx])
					if starFinite(v) {
						wt := 1.
						if dx == 0 {
							wt *= 2
						}
						if dy == 0 {
							wt *= 2
						}
						sum += wt * v
						weight += wt
					}
				}
			}
			if weight > 0 {
				smooth[y*w+x] = float32(sum / weight)
			} else {
				smooth[y*w+x] = float32(math.NaN())
			}
		}
	}
	var candidates []StarMapSource
	for y := 12; y < h-12; y++ {
		if y%64 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if opt.Progress != nil {
				opt.Progress("Finding stellar profiles", y, h)
			}
		}
		for x := 12; x < w-12; x++ {
			v := float64(smooth[y*w+x])
			if !starFinite(v) {
				continue
			}
			maximum := true
			for dy := -2; dy <= 2 && maximum; dy++ {
				for dx := -2; dx <= 2; dx++ {
					if dx == 0 && dy == 0 {
						continue
					}
					k := (y+dy)*w + x + dx
					nv := float64(smooth[k])
					if nv > v || (nv == v && k < y*w+x) {
						maximum = false
						break
					}
				}
			}
			if !maximum {
				continue
			}
			bg := []float64{float64(smooth[(y-8)*w+x]), float64(smooth[(y+8)*w+x]), float64(smooth[y*w+x-8]), float64(smooth[y*w+x+8])}
			if v-starMedian(bg) < opt.MinSNR*noise[(y/tile)*nx+x/tile] {
				continue
			}
			s := MeasureStarMapSource(pixels, w, h, float64(x), float64(y), saturated, opt)
			if s.SNR >= opt.MinSNR {
				candidates = append(candidates, s)
			}
		}
	}
	// Independent saturation seeds rescue missing/flattened cores. Components are
	// NOT accepted as stars: the unsaturated wings must still pass a profile fit.
	if len(saturated) > 0 {
		seen := make([]bool, len(saturated))
		for k, on := range saturated {
			if k%65536 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			if !on || seen[k] {
				continue
			}
			q := []int{k}
			seen[k] = true
			sx, sy := 0., 0.
			for j := 0; j < len(q); j++ {
				if j%8192 == 0 {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
				}
				i := q[j]
				x, y := i%w, i/w
				sx += float64(x)
				sy += float64(y)
				for dy := -1; dy <= 1; dy++ {
					for dx := -1; dx <= 1; dx++ {
						xx, yy := x+dx, y+dy
						if xx < 0 || xx >= w || yy < 0 || yy >= h {
							continue
						}
						ii := yy*w + xx
						if saturated[ii] && !seen[ii] {
							seen[ii] = true
							q = append(q, ii)
						}
					}
				}
			}
			x, y := sx/float64(len(q)), sy/float64(len(q))
			if x < 12 || y < 12 || x >= float64(w-12) || y >= float64(h-12) {
				continue
			}
			s := MeasureStarMapSource(pixels, w, h, x, y, saturated, opt)
			s.Saturated = true
			if s.SNR >= opt.MinSNR {
				candidates = append(candidates, s)
			}
		}
	}
	// Infer damaged bright cores from mosaic wings independently of native DQ.
	rescued, err := mosaicSaturatedStars(ctx, pixels, w, h, opt)
	if err != nil {
		return nil, err
	}
	candidates = append(candidates, rescued...)
	// Keep strongest candidate in a compact neighborhood; no catalog-size cap.
	sort.SliceStable(candidates, func(i, j int) bool {
		// A verified wing rescue must not be discarded in favor of a bright
		// core fit that will subsequently fail the ordinary field-width check.
		rescuedI := candidates[i].Accepted() && candidates[i].Saturated
		rescuedJ := candidates[j].Accepted() && candidates[j].Saturated
		if rescuedI != rescuedJ {
			return rescuedI
		}
		return candidates[i].Amplitude > candidates[j].Amplitude
	})
	occupied := make(map[[2]int][]StarMapSource)
	for _, s := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		bx, by := int(s.X)/8, int(s.Y)/8
		dup := false
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				for _, a := range occupied[[2]int{bx + dx, by + dy}] {
					if math.Hypot(a.X-s.X, a.Y-s.Y) < 4 {
						dup = true
					}
				}
			}
		}
		if dup {
			continue
		}
		occupied[[2]int{bx, by}] = append(occupied[[2]int{bx, by}], s)
		result.Sources = append(result.Sources, s)
	}
	widths := []float64{}
	for _, s := range result.Sources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if s.Status == "accepted" && !s.Saturated && s.SNR > 15 && s.Residual < .2 {
			widths = append(widths, s.FWHM)
		}
	}
	result.FWHM = opt.FWHM
	if result.FWHM == 0 {
		if len(widths) >= 5 {
			result.FWHM = starMedian(widths)
		} else {
			result.FWHM = 2.5
		}
	}
	// Empirical radial profile is built from normalized isolated seed stamps. It
	// is an extra rejection test, never a way to promote an extended source.
	bins := make([][]float64, 20)
	for _, s := range result.Sources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if s.Status != "accepted" || s.Saturated || s.SNR < 20 || s.Residual > .2 || math.Abs(s.FWHM-result.FWHM) > .7 {
			continue
		}
		isolated := true
		for _, t := range result.Sources {
			if t.X == s.X && t.Y == s.Y {
				continue
			}
			if math.Hypot(s.X-t.X, s.Y-t.Y) < 15 {
				isolated = false
				break
			}
		}
		if !isolated {
			continue
		}
		bg, _, _, _ := starLocalBackground(pixels, w, h, s.X, s.Y)
		for dy := -8; dy <= 8; dy++ {
			for dx := -8; dx <= 8; dx++ {
				x, y := int(math.Round(s.X))+dx, int(math.Round(s.Y))+dy
				r := math.Hypot(float64(x)-s.X, float64(y)-s.Y)
				b := int(r * 2)
				if b >= len(bins) {
					continue
				}
				v := float64(pixels[y*w+x])
				if starFinite(v) {
					bins[b] = append(bins[b], (v-bg)/s.Amplitude)
				}
			}
		}
	}
	if len(widths) >= 5 {
		result.Empirical = make([]float64, len(bins))
		for i, b := range bins {
			result.Empirical[i] = math.Max(0, starMedian(b))
		}
	}
	for i := range result.Sources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		s := &result.Sources[i]
		if s.Status == "accepted" && !s.Saturated && (s.FWHM > result.FWHM*1.65 || s.FWHM < result.FWHM*.55) {
			s.Status = "uncertain"
			s.Reason = "width differs from field stars"
		}
		if s.Status == "accepted" && !s.Saturated && len(result.Empirical) > 0 {
			bg, _, _, _ := starLocalBackground(pixels, w, h, s.X, s.Y)
			radial := make([][]float64, len(result.Empirical))
			for dy := -7; dy <= 7; dy++ {
				for dx := -7; dx <= 7; dx++ {
					x, y := int(math.Round(s.X))+dx, int(math.Round(s.Y))+dy
					b := int(2 * math.Hypot(float64(x)-s.X, float64(y)-s.Y))
					if b < 2 || b >= len(radial) {
						continue
					}
					v := float64(pixels[y*w+x])
					if starFinite(v) {
						radial[b] = append(radial[b], (v-bg)/s.Amplitude)
					}
				}
			}
			loss, energy := 0., 0.
			for b, a := range radial {
				if len(a) < 2 {
					continue
				}
				v := starMedian(a)
				d := v - result.Empirical[b]
				loss += d * d
				energy += v * v
			}
			if energy > 0 && math.Sqrt(loss/energy) > .65 {
				s.Status = "uncertain"
				s.Reason = "radial wings differ from empirical field PSF"
			}
		}
		// A bright parent's diffraction structure must not create automatic stars.
		for j := 0; j < i; j++ {
			p := result.Sources[j]
			if p.Amplitude < s.Amplitude*30 {
				continue
			}
			d := math.Hypot(s.X-p.X, s.Y-p.Y)
			if d < math.Max(16, p.Radius*2) {
				s.Status = "uncertain"
				s.Reason = "near a bright star: possible spike or companion"
				break
			}
		}
	}
	sort.Slice(result.Sources, func(i, j int) bool {
		a, b := result.Sources[i], result.Sources[j]
		if a.Y == b.Y {
			return a.X < b.X
		}
		return a.Y < b.Y
	})
	for i := range result.Sources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result.Sources[i].ID = i + 1
	}
	return result, nil
}

// starLocalBackground fits a robust plane to the outer annulus. The returned
// noise includes residual nebular structure, not only instrumental pixel noise.
func starLocalBackground(p []float32, w, h int, cx, cy float64) (bg, gx, gy, noise float64) {
	vals := make([]float64, 0, 200)
	for dy := -11; dy <= 11; dy++ {
		for dx := -11; dx <= 11; dx++ {
			r := dx*dx + dy*dy
			if r < 64 || r > 121 {
				continue
			}
			x, y := int(math.Round(cx))+dx, int(math.Round(cy))+dy
			if x < 0 || y < 0 || x >= w || y >= h {
				continue
			}
			v := float64(p[y*w+x])
			if starFinite(v) {
				vals = append(vals, v)
			}
		}
	}
	bg = starMedian(vals)
	dev := make([]float64, len(vals))
	for i, v := range vals {
		dev[i] = math.Abs(v - bg)
	}
	noise = math.Max(1.4826*starMedian(dev), 1e-12)
	sum, xx, yy := 0., 0., 0.
	n := 0
	for dy := -11; dy <= 11; dy++ {
		for dx := -11; dx <= 11; dx++ {
			r := dx*dx + dy*dy
			if r < 64 || r > 121 {
				continue
			}
			x, y := int(math.Round(cx))+dx, int(math.Round(cy))+dy
			if x < 0 || y < 0 || x >= w || y >= h {
				continue
			}
			v := float64(p[y*w+x])
			if !starFinite(v) || math.Abs(v-bg) > 3*noise {
				continue
			}
			sum += v
			n++
			gx += float64(dx) * (v - bg)
			gy += float64(dy) * (v - bg)
			xx += float64(dx * dx)
			yy += float64(dy * dy)
		}
	}
	if n > 0 {
		bg = sum / float64(n)
	}
	if xx > 0 {
		gx /= xx
	}
	if yy > 0 {
		gy /= yy
	}
	dev = dev[:0]
	for dy := -11; dy <= 11; dy++ {
		for dx := -11; dx <= 11; dx++ {
			r := dx*dx + dy*dy
			if r < 64 || r > 121 {
				continue
			}
			x, y := int(math.Round(cx))+dx, int(math.Round(cy))+dy
			if x < 0 || y < 0 || x >= w || y >= h {
				continue
			}
			v := float64(p[y*w+x])
			if starFinite(v) {
				dev = append(dev, math.Abs(v-bg-gx*float64(dx)-gy*float64(dy)))
			}
		}
	}
	noise = math.Max(1.4826*starMedian(dev), 1e-12)
	return
}

// MeasureStarMapSource also supports forced exposure measurements. It never
// repairs or substitutes saturated/invalid samples. Coordinates are zero-based.
func MeasureStarMapSource(p []float32, w, h int, cx, cy float64, sat []bool, opt StarMapOptions) StarMapSource {
	s := StarMapSource{X: cx, Y: cy, Status: "uncertain", Reason: "insufficient valid profile samples"}
	if w <= 0 || h <= 0 || w > int(^uint(0)>>1)/h || len(p) != w*h || (len(sat) > 0 && len(sat) != len(p)) || !starFinite(cx) || !starFinite(cy) {
		return s
	}
	if cx < 12 || cy < 12 || cx >= float64(w-12) || cy >= float64(h-12) {
		return s
	}
	bg, gx, gy, noise := starLocalBackground(p, w, h, cx, cy)
	// Refine only a compact centroid, so a gradient cannot drag it far away.
	sx, sy, sw := 0., 0., 0.
	for dy := -2; dy <= 2; dy++ {
		for dx := -2; dx <= 2; dx++ {
			x, y := int(math.Round(cx))+dx, int(math.Round(cy))+dy
			i := y*w + x
			if len(sat) > 0 && sat[i] {
				s.Saturated = true
				continue
			}
			v := float64(p[i]) - bg - gx*float64(dx) - gy*float64(dy)
			if starFinite(v) && v > 0 {
				sw += v
				sx += v * float64(x)
				sy += v * float64(y)
			}
		}
	}
	if !s.Saturated && sw > 0 && math.Hypot(sx/sw-cx, sy/sw-cy) < 1.5 {
		cx, cy = sx/sw, sy/sw
		s.X, s.Y = cx, cy
	}
	type sample struct{ x, y, v float64 }
	samples := make([]sample, 0, 160)
	peak := 0.
	sectors := [4]int{}
	for dy := -7; dy <= 7; dy++ {
		for dx := -7; dx <= 7; dx++ {
			x, y := int(math.Round(cx))+dx, int(math.Round(cy))+dy
			rx, ry := float64(x)-cx, float64(y)-cy
			r2 := rx*rx + ry*ry
			if r2 > 49 {
				continue
			}
			i := y*w + x
			if len(sat) > 0 && sat[i] {
				s.Saturated = true
				continue
			}
			v := float64(p[i]) - bg - gx*(float64(x)-s.X) - gy*(float64(y)-s.Y)
			if !starFinite(v) {
				continue
			}
			peak = math.Max(peak, v)
			samples = append(samples, sample{rx, ry, v})
		}
	}
	if s.Saturated {
		filtered := samples[:0]
		for _, a := range samples {
			if a.x*a.x+a.y*a.y >= 6.25 {
				filtered = append(filtered, a)
			}
		}
		samples = filtered
	}
	if len(samples) < 45 {
		return s
	}
	for _, a := range samples {
		if a.v > noise*2 {
			q := 0
			if a.x < 0 {
				q++
			}
			if a.y < 0 {
				q += 2
			}
			sectors[q]++
		}
	}
	best := math.Inf(1)
	bestAmp, bestW := 0., 0.
	// Saturated HST stars have narrow diffraction spikes and bleed residues.
	// Fit the majority of wing samples robustly, with center refinement, rather
	// than requiring the clipped object to have an unsaturated Gaussian core.
	offsets := []float64{0}
	if s.Saturated {
		offsets = []float64{-1, -.75, -.5, -.25, 0, .25, .5, .75, 1}
	}
	bestDX, bestDY := 0., 0.
	for _, ox := range offsets {
		for _, oy := range offsets {
			for width := 1.2; width <= 8.01; width += .2 {
				if s.Saturated && (width < 1.8 || width > 4.6) {
					continue
				}
				alpha := width / (2 * math.Sqrt(math.Pow(2, 1/2.5)-1))
				templates := make([]float64, len(samples))
				dot, norm := 0., 0.
				for i, a := range samples {
					t := math.Pow(1+((a.x-ox)*(a.x-ox)+(a.y-oy)*(a.y-oy))/(alpha*alpha), -2.5)
					templates[i] = t
					dot += t * a.v
					norm += t * t
				}
				amp := math.Max(0, dot/norm)
				keep := make([]bool, len(samples))
				for i := range keep {
					keep[i] = true
				}
				if s.Saturated {
					errors := make([]float64, len(samples))
					for i, a := range samples {
						errors[i] = math.Abs(a.v - amp*templates[i])
					}
					sorted := append([]float64(nil), errors...)
					sort.Float64s(sorted)
					limit := sorted[len(sorted)*3/4]
					dot, norm = 0, 0
					for i, a := range samples {
						keep[i] = errors[i] <= limit
						if keep[i] {
							dot += templates[i] * a.v
							norm += templates[i] * templates[i]
						}
					}
					amp = math.Max(0, dot/norm)
				}
				energy, loss := 0., 0.
				coverage := [4]int{}
				for i, a := range samples {
					if !keep[i] {
						continue
					}
					energy += a.v * a.v
					d := a.v - amp*templates[i]
					loss += d * d
					q := 0
					if a.x < 0 {
						q++
					}
					if a.y < 0 {
						q += 2
					}
					coverage[q]++
				}
				valid := true
				for _, n := range coverage {
					if n < 5 {
						valid = false
					}
				}
				res := math.Sqrt(loss / math.Max(energy, 1e-30))
				if valid && res < best {
					best, bestAmp, bestW = res, amp, width
					bestDX, bestDY = ox, oy
				}
			}
		}
	}
	if !starFinite(best) {
		return s
	}
	s.X += bestDX
	s.Y += bestDY
	s.Residual, s.Amplitude, s.FWHM = best, bestAmp, bestW
	s.SNR = peak / noise
	s.Radius = math.Min(16, math.Max(3, 1.6*bestW))
	if s.Saturated {
		s.Radius = math.Min(20, math.Max(5, 2*bestW))
	}
	maxResidual := opt.MaxResidual
	if maxResidual == 0 {
		maxResidual = .22
	}
	minSNR := opt.MinSNR
	if minSNR == 0 {
		minSNR = 7
	}
	s.Reason = "profile differs from a point source"
	if s.SNR < minSNR {
		s.Reason = "low local contrast"
		return s
	}
	if bestW > 5.2 {
		s.Reason = "extended emission or unresolved blend"
		return s
	}
	if bestW < 1.5 {
		s.Reason = "too sharp: possible detector artifact"
		return s
	}
	if best > maxResidual {
		return s
	}
	for _, n := range sectors {
		if n < 2 {
			s.Reason = "insufficient stellar wings in all directions"
			return s
		}
	}
	s.Status = "accepted"
	s.Reason = "compact stellar profile"
	if s.Saturated {
		s.Reason = "saturation with stellar wings"
	}
	return s
}

// Rasterize constructs bounded, feathered selections. No flood fill can spill
// across a nebular ridge. Uncovered pixels remain zero with FLAGS bit 1 set.
func (m *StarMap) Rasterize(ctx context.Context, p []float32) (mask []float32, labels, flags []int32, err error) {
	if m.Width <= 0 || m.Height <= 0 || m.Width > int(^uint(0)>>1)/m.Height || len(p) != m.Width*m.Height {
		return nil, nil, nil, fmt.Errorf("invalid star map dimensions")
	}
	mask = make([]float32, len(p))
	labels = make([]int32, len(p))
	flags = make([]int32, len(p))
	for i, v := range p {
		if i%65536 == 0 {
			if err = ctx.Err(); err != nil {
				return nil, nil, nil, err
			}
		}
		if !starFinite(float64(v)) {
			flags[i] = 1
		}
	}
	for _, s := range m.Sources {
		if err = ctx.Err(); err != nil {
			return nil, nil, nil, err
		}
		if !starFinite(s.X) || !starFinite(s.Y) || !starFinite(s.Radius) || s.Radius <= 0 || s.Radius > 100 {
			return nil, nil, nil, fmt.Errorf("invalid footprint for source %d", s.ID)
		}
		r := s.Radius
		for y := max(0, int(math.Floor(s.Y-r))); y <= min(m.Height-1, int(math.Ceil(s.Y+r))); y++ {
			for x := max(0, int(math.Floor(s.X-r))); x <= min(m.Width-1, int(math.Ceil(s.X+r))); x++ {
				i := y*m.Width + x
				d := math.Hypot(float64(x)-s.X, float64(y)-s.Y)
				if d >= r || flags[i]&1 != 0 {
					continue
				}
				if !s.Accepted() {
					flags[i] |= 2
					continue
				}
				if s.Saturated {
					flags[i] |= 4
				}
				if s.Override != "" {
					flags[i] |= 16
				}
				v := float32(1)
				if d > r*.65 {
					v = float32(.5 + .5*math.Cos(math.Pi*(d/r-.65)/.35))
				}
				if v > mask[i] {
					mask[i] = v
					labels[i] = int32(s.ID)
				}
			}
		}
	}
	return
}
