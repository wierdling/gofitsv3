package processing

import (
	"context"
	"fmt"
	"math"
	"sort"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
)

// StarNeutralizer whitens stars in a composed RGB image. The geometry (which
// pixels are star, and how strongly) comes from one reference source's
// prepared star model, mapped from the composite grid back to that source's
// grid with the same coordinate mapping the compositors use. The color
// operation is local: for each star a robust background per channel is
// measured in an annulus outside its footprint, the stellar excess above
// that background is moved toward a neutral level, and the background is left
// alone, so a halo over colored nebulosity keeps the nebula's color. This is
// an artistic adjustment; stars are not all white.
type StarNeutralizer struct {
	model    *StarTreatmentModel
	mapper   *diskCoordinateMapper
	forward  AffineTransform // reference source grid -> composite grid
	scale    float64         // largest stretch of forward, for extents
	w, h     int
	settings models.StarWhiteningState
	stars    []neutralizeStar
	starOf   []int // model fit index -> stars index, or -1
}

type neutralizeStar struct {
	fit                    *StarTreatmentFit
	cx, cy, extent         float64 // composite-grid center and radius
	x0, y0, x1, y1         int     // composite-grid footprint box
	ax0, ay0, ax1, ay1     int     // composite-grid annulus box
	inner, outer           float64 // annulus radii in the composite grid
	background             [3]float64
	samples                [3][]float64
	peak                   [3]float64 // brightest value per channel inside the footprint
	valid                  bool
	peakExcess             float64 // largest per-channel excess at the core
	sampleStride, sampleAt int
}

const (
	neutralizeAnnulusInner = 1.1
	neutralizeAnnulusOuter = 1.5
	neutralizeMaxSamples   = 4096
	neutralizeMinSamples   = 24
)

// NewStarNeutralizer prepares whitening for a composite of size w x h. ref is
// the reference source as the compositor sees it (metadata plus manual
// offsets); grid is the composite's reference image (Channel 2), which
// defines the output grid. The model must have been prepared on ref's
// original grid.
func NewStarNeutralizer(model *StarTreatmentModel, ref DiskChannel, grid models.LoadedImage, w, h int, settings models.StarWhiteningState) (*StarNeutralizer, error) {
	if model == nil {
		return nil, fmt.Errorf("white stars: no reference star model")
	}
	if !validStarTreatmentDimensions(w, h) {
		return nil, fmt.Errorf("white stars: invalid composite dimensions")
	}
	if !starFinite(settings.Strength) || settings.Strength < 0 || settings.Strength > 1 {
		return nil, fmt.Errorf("white stars: strength must be between 0 and 1")
	}
	if !settings.Red && !settings.Green && !settings.Blue {
		return nil, fmt.Errorf("white stars: select at least one output channel")
	}
	sw, sh := model.SourceSize()
	mapper := newDiskCoordinateMapper(ref.Image, grid, ref.OffsetX, ref.OffsetY, ref.OffsetRot, w, h, sw, sh)
	// The mapping is affine (resize, WCS affine, offset, rotation), so three
	// samples determine it exactly; invert it for source -> composite.
	x0, y0 := mapper.mapCoordinate(0, 0)
	x1, y1 := mapper.mapCoordinate(1, 0)
	x2, y2 := mapper.mapCoordinate(0, 1)
	backward := AffineTransform{A: x1 - x0, B: x2 - x0, C: x0, D: y1 - y0, E: y2 - y0, F: y0}
	det := backward.A*backward.E - backward.B*backward.D
	if !starFinite(det) || math.Abs(det) < 1e-12 {
		return nil, fmt.Errorf("white stars: reference mapping is singular")
	}
	forward := AffineTransform{A: backward.E / det, B: -backward.B / det, D: -backward.D / det, E: backward.A / det}
	forward.C = -(forward.A*backward.C + forward.B*backward.F)
	forward.F = -(forward.D*backward.C + forward.E*backward.F)
	scale := math.Max(math.Hypot(forward.A, forward.D), math.Hypot(forward.B, forward.E))
	n := &StarNeutralizer{model: model, mapper: mapper, forward: forward, scale: scale, w: w, h: h, settings: settings, starOf: make([]int, len(model.fits))}
	for i := range model.fits {
		n.starOf[i] = -1
		f := &model.fits[i]
		cx, cy := ApplyAffineTransform(forward, f.X, f.Y)
		extent := StarTreatmentExtent(*f) * scale
		outer := extent * neutralizeAnnulusOuter
		s := neutralizeStar{fit: f, cx: cx, cy: cy, extent: extent, inner: extent * neutralizeAnnulusInner, outer: outer}
		s.x0, s.y0 = max(0, int(math.Floor(cx-extent))), max(0, int(math.Floor(cy-extent)))
		s.x1, s.y1 = min(w-1, int(math.Ceil(cx+extent))), min(h-1, int(math.Ceil(cy+extent)))
		s.ax0, s.ay0 = max(0, int(math.Floor(cx-outer))), max(0, int(math.Floor(cy-outer)))
		s.ax1, s.ay1 = min(w-1, int(math.Ceil(cx+outer))), min(h-1, int(math.Ceil(cy+outer)))
		if s.x1 < s.x0 || s.y1 < s.y0 {
			continue // entirely outside the composite
		}
		// Subsample large annuli so memory stays bounded per star.
		area := math.Pi * (outer*outer - s.inner*s.inner)
		s.sampleStride = max(1, int(area/neutralizeMaxSamples))
		n.starOf[i] = len(n.stars)
		n.stars = append(n.stars, s)
	}
	return n, nil
}

// Stars reports how many reference stars fall inside the composite.
func (n *StarNeutralizer) Stars() int { return len(n.stars) }

// touchesRow reports whether any star's annulus box covers a composite row;
// passes skip the rest.
func (n *StarNeutralizer) touchesRow(y int) bool {
	for i := range n.stars {
		s := &n.stars[i]
		if y >= s.ay0 && y <= s.ay1 {
			return true
		}
	}
	return false
}

// weightAt is the reference model's footprint weight at a composite pixel,
// with the owning star (largest weight).
func (n *StarNeutralizer) weightAt(x, y int) (float64, int) {
	fx, fy := n.mapper.mapCoordinate(x, y)
	best, owner := 0., -1
	for _, idx := range n.model.bucketAt(fx, fy) {
		i := n.starOf[idx]
		if i < 0 {
			continue
		}
		if w := starTreatmentFitWeight(fx, fy, *n.stars[i].fit); w > best {
			best, owner = w, i
		}
	}
	return best, owner
}

// gather collects annulus samples from one composite row, skipping pixels
// inside any star's footprint and invalid values, and tracks each star's
// per-channel peak inside its footprint.
func (n *StarNeutralizer) gather(y int, rows [3][]float32) {
	for i := range n.stars {
		s := &n.stars[i]
		if y < s.ay0 || y > s.ay1 {
			continue
		}
		for x := s.ax0; x <= s.ax1; x++ {
			d := math.Hypot(float64(x)-s.cx, float64(y)-s.cy)
			if d > s.outer {
				continue
			}
			if d < s.inner {
				if w, owner := n.weightAt(x, y); w > 0 && owner == i {
					for c := 0; c < 3; c++ {
						if v := float64(rows[c][x]); starFinite(v) && v > s.peak[c] {
							s.peak[c] = v
						}
					}
				}
				continue
			}
			s.sampleAt++
			if s.sampleAt%s.sampleStride != 0 {
				continue
			}
			if w, _ := n.weightAt(x, y); w > 0 {
				continue
			}
			r, g, b := float64(rows[0][x]), float64(rows[1][x]), float64(rows[2][x])
			if !starFinite(r) || !starFinite(g) || !starFinite(b) {
				continue
			}
			s.samples[0] = append(s.samples[0], r)
			s.samples[1] = append(s.samples[1], g)
			s.samples[2] = append(s.samples[2], b)
		}
	}
}

// finishBackgrounds turns gathered samples into a sigma-clipped median per
// channel. A star without enough clean annulus is left untouched.
func (n *StarNeutralizer) finishBackgrounds() {
	for i := range n.stars {
		s := &n.stars[i]
		s.valid = len(s.samples[0]) >= neutralizeMinSamples
		if !s.valid {
			continue
		}
		for c := 0; c < 3; c++ {
			s.background[c] = clippedMedian(s.samples[c])
			s.samples[c] = nil
			s.peakExcess = math.Max(s.peakExcess, s.peak[c]-s.background[c])
		}
		if s.peakExcess <= 0 {
			s.valid = false
		}
	}
}

func clippedMedian(v []float64) float64 {
	m := starMedian(append([]float64(nil), v...))
	dev := make([]float64, len(v))
	for i, x := range v {
		dev[i] = math.Abs(x - m)
	}
	limit := 3 * 1.4826 * starMedian(dev)
	if limit <= 0 {
		return m
	}
	kept := make([]float64, 0, len(v))
	for _, x := range v {
		if math.Abs(x-m) <= limit {
			kept = append(kept, x)
		}
	}
	sort.Float64s(kept)
	if len(kept) == 0 {
		return m
	}
	return kept[len(kept)/2]
}

// applyRow whitens one composite row in place.
func (n *StarNeutralizer) applyRow(y int, rows [3][]float32) {
	if !n.touchesRow(y) {
		return
	}
	selected := [3]bool{n.settings.Red, n.settings.Green, n.settings.Blue}
	for x := 0; x < n.w; x++ {
		weight, owner := n.weightAt(x, y)
		if weight <= 0 || owner < 0 || !n.stars[owner].valid {
			continue
		}
		a := n.settings.Strength * weight
		if a <= 0 {
			continue
		}
		s := &n.stars[owner]
		var excess [3]float64
		neutral, luma := 0., 0.
		for c := 0; c < 3; c++ {
			v := float64(rows[c][x])
			if !starFinite(v) {
				excess[c] = math.NaN()
				continue
			}
			excess[c] = math.Max(0, v-s.background[c])
			neutral = math.Max(neutral, excess[c])
		}
		if excess[0] != excess[0] || excess[1] != excess[1] || excess[2] != excess[2] {
			continue
		}
		luma = .2126*excess[0] + .7152*excess[1] + .0722*excess[2]
		if n.settings.Level == models.StarWhiteningLuminance {
			// Full neutralization everywhere in the footprint, brightness kept.
			neutral = luma
		} else {
			// White core whose whiteness fades with the star's own profile: the
			// neutral level is the brightest channel (white), and the amount
			// applied scales with the excess relative to the star's peak, so the
			// core turns white while a faint halo keeps most of its color and
			// is never brightened into a plate.
			a *= math.Sqrt(math.Max(0, math.Min(1, neutral/s.peakExcess)))
		}
		for c := 0; c < 3; c++ {
			if !selected[c] {
				continue
			}
			v := float64(rows[c][x]) + a*(neutral-excess[c])
			rows[c][x] = float32(math.Max(0, math.Min(1, v)))
		}
	}
}

// Apply whitens in-memory planes in place. Two passes: backgrounds, then
// correction.
func (n *StarNeutralizer) Apply(ctx context.Context, planes [3][]float32) error {
	for c := range planes {
		if len(planes[c]) != n.w*n.h {
			return fmt.Errorf("white stars: plane %d has %d samples, composite is %dx%d", c, len(planes[c]), n.w, n.h)
		}
	}
	rowsAt := func(y int) [3][]float32 {
		return [3][]float32{planes[0][y*n.w : (y+1)*n.w], planes[1][y*n.w : (y+1)*n.w], planes[2][y*n.w : (y+1)*n.w]}
	}
	for y := 0; y < n.h; y++ {
		if y%64 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		n.gather(y, rowsAt(y))
	}
	n.finishBackgrounds()
	for y := 0; y < n.h; y++ {
		if y%64 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		n.applyRow(y, rowsAt(y))
	}
	return ctx.Err()
}

// ApplyRGBA whitens a packed RGBA byte buffer in place, through float planes.
func (n *StarNeutralizer) ApplyRGBA(ctx context.Context, buf []byte) error {
	if len(buf) != n.w*n.h*4 {
		return fmt.Errorf("white stars: RGBA buffer does not match the composite")
	}
	var planes [3][]float32
	for c := range planes {
		planes[c] = make([]float32, n.w*n.h)
		for i := range planes[c] {
			planes[c][i] = float32(buf[i*4+c]) / 255
		}
	}
	if err := n.Apply(ctx, planes); err != nil {
		return err
	}
	for i := 0; i < n.w*n.h; i++ {
		for c := 0; c < 3; c++ {
			buf[i*4+c] = byte(clamp01(float64(planes[c][i]))*255 + .5)
		}
	}
	return nil
}

// ApplyDisk whitens R,G,B artifacts in place with bounded row buffers.
func (n *StarNeutralizer) ApplyDisk(ctx context.Context, paths [3]string) error {
	var arts [3]*fitsio.Float32Artifact
	for c, p := range paths {
		a, err := fitsio.OpenFloat32Artifact(p)
		if err != nil {
			return err
		}
		defer a.Close()
		if a.Width != n.w || a.Height != n.h {
			return fmt.Errorf("white stars: artifact %s is %dx%d, composite is %dx%d", p, a.Width, a.Height, n.w, n.h)
		}
		arts[c] = a
	}
	rows := [3][]float32{make([]float32, n.w), make([]float32, n.w), make([]float32, n.w)}
	read := func(y int) error {
		for c := range arts {
			if err := arts[c].ReadRow(y, rows[c]); err != nil {
				return err
			}
		}
		return nil
	}
	for y := 0; y < n.h; y++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !n.touchesRow(y) {
			continue
		}
		if err := read(y); err != nil {
			return err
		}
		n.gather(y, rows)
	}
	n.finishBackgrounds()
	for y := 0; y < n.h; y++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !n.touchesRow(y) {
			continue
		}
		if err := read(y); err != nil {
			return err
		}
		n.applyRow(y, rows)
		for c := range arts {
			if err := arts[c].WriteRow(y, rows[c]); err != nil {
				return err
			}
		}
	}
	return ctx.Err()
}
