package processing

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sort"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
)

// StarNeutralizer whitens stars in a composed RGB image. The geometry (which
// pixels are star, and how strongly) is seeded by one reference source's
// prepared star model and remeasured on the current RGB planes for every
// apply, using the same coordinate mapping as the compositors. The color
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
	cell     int
	cols     int
	rows     int
	buckets  [][]int
}

type neutralizeStar struct {
	fit                                               *StarTreatmentFit
	cx, cy, extent                                    float64 // composite-grid center and radius
	x0, y0, x1, y1                                    int     // composite-grid footprint box
	ax0, ay0, ax1, ay1                                int     // composite-grid annulus box
	inner, outer                                      float64 // annulus radii in the composite grid
	mappedExtent, maxExtent                           float64 // current RGB footprint and bounded search limit
	sourceExtent, mappedSourceExtent, maxSourceExtent float64
	mappedInner                                       float64
	background                                        [3]float64
	samples                                           [3][]float64
	peak                                              [3]float64 // brightest value per channel inside the footprint
	valid                                             bool
	forced                                            bool    // user override: whiten even if runtime checks would skip it
	peakExcess                                        float64 // largest per-channel excess at the core
	noise                                             float64
	spikes                                            []StarTreatmentSpike // runtime RGB extensions
	index                                             int
	sampleStride, sampleAt                            int
}

const (
	neutralizeAnnulusInner  = 1.1
	neutralizeAnnulusOuter  = 1.5
	neutralizeMaxSamples    = 4096
	neutralizeMinSamples    = 24
	neutralizeRadialSamples = 128
	neutralizeLargeExtent   = 12
	neutralizeLargeSearch   = 4
	neutralizeSmallSearch   = 2
	neutralizeSpikeAngles   = 72
)

// sourceToCompositeTransform builds the affine mapping from a source's own
// pixel grid (sw x sh) onto the composite render grid (w x h), plus the
// largest singular value of that mapping (used to scale extents/radii into
// composite-grid units). Shared by star whitening and the star-treatment
// diagnostics overlay, which both need the same source-to-composite geometry.
func sourceToCompositeTransform(ref DiskChannel, grid models.LoadedImage, w, h, sw, sh int) (*diskCoordinateMapper, AffineTransform, float64, error) {
	mapper := newDiskCoordinateMapper(ref.Image, grid, ref.OffsetX, ref.OffsetY, ref.OffsetRot, w, h, sw, sh)
	// The mapping is affine (resize, WCS affine, offset, rotation), so three
	// samples determine it exactly; invert it for source -> composite.
	x0, y0 := mapper.mapCoordinate(0, 0)
	x1, y1 := mapper.mapCoordinate(1, 0)
	x2, y2 := mapper.mapCoordinate(0, 1)
	backward := AffineTransform{A: x1 - x0, B: x2 - x0, C: x0, D: y1 - y0, E: y2 - y0, F: y0}
	det := backward.A*backward.E - backward.B*backward.D
	if !starFinite(det) || math.Abs(det) < 1e-12 {
		return nil, AffineTransform{}, 0, fmt.Errorf("reference mapping is singular")
	}
	forward := AffineTransform{A: backward.E / det, B: -backward.B / det, D: -backward.D / det, E: backward.A / det}
	forward.C = -(forward.A*backward.C + forward.B*backward.F)
	forward.F = -(forward.D*backward.C + forward.E*backward.F)
	// Use the largest singular value as the transformed-radius bound. The
	// largest column norm underestimates a sheared affine and can drop valid
	// pixels from the runtime footprint bucket.
	norm2 := forward.A*forward.A + forward.B*forward.B + forward.D*forward.D + forward.E*forward.E
	detForward := forward.A*forward.E - forward.B*forward.D
	scale := math.Sqrt(.5 * (norm2 + math.Sqrt(math.Max(0, norm2*norm2-4*detForward*detForward))))
	return mapper, forward, scale, nil
}

// StarTreatmentDiagnostic describes one catalog star's treatment outcome,
// mapped onto the composite render grid, for the "why wasn't this star
// treated" overlay. It carries no pixel data.
type StarTreatmentDiagnostic struct {
	Source        string // caller-supplied label for the source this fit came from
	SourceID      int
	X, Y          float64 // composite-grid coordinates
	Usable        bool
	Reason        string
	Saturated     bool
	HaloValidated bool
}

// MapStarTreatmentDiagnostics maps a source's prepared star-treatment fits
// onto the composite render grid (w x h) using the same coordinate transform
// the compositor and star whitening use, so the diagnostic overlay lines up
// with what was actually rendered. Every fit is returned (Usable true or
// false) except those that land off the composite entirely.
func MapStarTreatmentDiagnostics(model *StarTreatmentModel, label string, ref DiskChannel, grid models.LoadedImage, w, h int) ([]StarTreatmentDiagnostic, error) {
	if model == nil {
		return nil, fmt.Errorf("star diagnostics: no model")
	}
	if !validStarTreatmentDimensions(w, h) {
		return nil, fmt.Errorf("star diagnostics: invalid composite dimensions")
	}
	sw, sh := model.SourceSize()
	_, forward, _, err := sourceToCompositeTransform(ref, grid, w, h, sw, sh)
	if err != nil {
		return nil, fmt.Errorf("star diagnostics: %w", err)
	}
	fits := model.Fits()
	out := make([]StarTreatmentDiagnostic, 0, len(fits))
	for _, f := range fits {
		if !starFinite(f.X) || !starFinite(f.Y) {
			continue
		}
		cx, cy := ApplyAffineTransform(forward, f.X, f.Y)
		if !starFinite(cx) || !starFinite(cy) || cx < -1 || cy < -1 || cx > float64(w) || cy > float64(h) {
			continue // off the composite; not interesting to the overlay
		}
		out = append(out, StarTreatmentDiagnostic{
			Source: label, SourceID: f.SourceID, X: cx, Y: cy,
			Usable: f.Usable, Reason: f.Reason, Saturated: f.Saturated, HaloValidated: f.HaloValidated,
		})
	}
	return out, nil
}

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
	mapper, forward, scale, err := sourceToCompositeTransform(ref, grid, w, h, sw, sh)
	if err != nil {
		return nil, fmt.Errorf("white stars: %w", err)
	}
	detForward := forward.A*forward.E - forward.B*forward.D
	n := &StarNeutralizer{model: model, mapper: mapper, forward: forward, scale: scale, w: w, h: h, settings: settings, starOf: make([]int, len(model.fits)), cell: 64}
	n.cols, n.rows = (w+n.cell-1)/n.cell, (h+n.cell-1)/n.cell
	n.buckets = make([][]int, n.cols*n.rows)
	for i := range model.fits {
		n.starOf[i] = -1
		f := &model.fits[i]
		// Rejected preparation fits remain in the model for diagnostics, but
		// they must never become whitening owners. Forced processing applies to
		// valid fits whose runtime annulus is sparse; it does not resurrect a
		// rejected or malformed model.
		if !f.Usable {
			continue
		}
		cx, cy := ApplyAffineTransform(forward, f.X, f.Y)
		sourceExtent := StarTreatmentExtent(*f)
		extent := sourceExtent * scale
		searchFactor := neutralizeSmallSearch
		if sourceExtent >= neutralizeLargeExtent {
			searchFactor = neutralizeLargeSearch
		}
		maxSourceExtent := math.Min(maxStarTreatmentExtent, math.Max(sourceExtent+8, sourceExtent*float64(searchFactor)))
		for j := range model.fits {
			if j != i {
				other := model.fits[j]
				maxSourceExtent = math.Min(maxSourceExtent, math.Max(sourceExtent, math.Hypot(f.X-other.X, f.Y-other.Y)/2))
			}
		}
		maxExtent := maxSourceExtent * scale
		// Keep the background annulus outside the bounded wing search whenever
		// possible. A changed RGB stretch can put visible stellar light into the
		// original annulus, which would otherwise bias the red background upward.
		outer := math.Max(extent*neutralizeAnnulusOuter, maxExtent*1.3)
		s := neutralizeStar{fit: f, cx: cx, cy: cy, extent: extent, mappedExtent: extent, maxExtent: maxExtent, sourceExtent: sourceExtent, mappedSourceExtent: sourceExtent, maxSourceExtent: maxSourceExtent, inner: extent * neutralizeAnnulusInner, outer: outer, forced: settings.ForcedStars[f.SourceID], index: len(n.stars)}
		s.x0, s.y0 = max(0, int(math.Floor(cx-extent))), max(0, int(math.Floor(cy-extent)))
		s.x1, s.y1 = min(w-1, int(math.Ceil(cx+extent))), min(h-1, int(math.Ceil(cy+extent)))
		s.ax0, s.ay0 = max(0, int(math.Floor(cx-outer))), max(0, int(math.Floor(cy-outer)))
		s.ax1, s.ay1 = min(w-1, int(math.Ceil(cx+outer))), min(h-1, int(math.Ceil(cy+outer)))
		if s.x1 < s.x0 || s.y1 < s.y0 {
			continue // entirely outside the composite
		}
		// Subsample large annuli so memory stays bounded per star.
		area := math.Pi * (math.Pow(outer/scale, 2) - math.Pow(maxSourceExtent*1.05, 2)) * math.Abs(detForward)
		s.sampleStride = max(1, int(math.Ceil(area/neutralizeMaxSamples)))
		n.starOf[i] = len(n.stars)
		bx0, bx1 := max(0, int(math.Floor(cx-maxExtent))/n.cell), min(n.cols-1, int(math.Floor(cx+maxExtent))/n.cell)
		by0, by1 := max(0, int(math.Floor(cy-maxExtent))/n.cell), min(n.rows-1, int(math.Floor(cy+maxExtent))/n.cell)
		for by := by0; by <= by1; by++ {
			for bx := bx0; bx <= bx1; bx++ {
				n.buckets[by*n.cols+bx] = append(n.buckets[by*n.cols+bx], len(n.stars))
			}
		}
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
		rowExtent := math.Max(s.outer, s.mappedExtent)
		if y >= max(0, int(math.Floor(s.cy-rowExtent))) && y <= min(n.h-1, int(math.Ceil(s.cy+rowExtent))) {
			return true
		}
	}
	return false
}

// weightAt is the reference model's footprint weight at a composite pixel,
// with the owning star (largest weight).
func (n *StarNeutralizer) baseWeightAt(x, y int) (float64, int) {
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

// weightAt includes the current-pass extension, if one was detected. The
// extension is a radial feather outside the reviewed footprint; it cannot
// create a new star or widen an unvalidated spike/halo component.
func (n *StarNeutralizer) weightAt(x, y int) (float64, int) {
	best, owner := 0., -1
	if x < 0 || y < 0 || x >= n.w || y >= n.h {
		return 0, -1
	}
	for _, i := range n.buckets[(y/n.cell)*n.cols+x/n.cell] {
		s := &n.stars[i]
		fx, fy := n.mapper.mapCoordinate(x, y)
		w := starTreatmentFitWeight(fx, fy, *s.fit)
		if s.mappedInner > 0 {
			extended := StarTreatmentFit{X: s.fit.X, Y: s.fit.Y, InnerRadius: s.mappedInner, OuterRadius: s.mappedSourceExtent}
			w = math.Max(w, starTreatmentFitWeight(fx, fy, extended))
		}
		for _, spike := range s.spikes {
			w = math.Max(w, starTreatmentSpikeWeight(fx-s.fit.X, fy-s.fit.Y, spike))
		}
		if w > best {
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
			fx, fy := n.mapper.mapCoordinate(x, y)
			d := math.Hypot(fx-s.fit.X, fy-s.fit.Y)
			if d > s.outer/n.scale {
				continue
			}
			if d < s.sourceExtent {
				if w, owner := n.weightAt(x, y); w > 0 && owner == i {
					for c := 0; c < 3; c++ {
						if v := float64(rows[c][x]); starFinite(v) && v > s.peak[c] {
							s.peak[c] = v
						}
					}
				}
				continue
			}
			if d < s.maxSourceExtent*1.05 {
				continue
			}
			s.sampleAt++
			if s.sampleAt%s.sampleStride != 0 {
				continue
			}
			if w, _ := n.baseWeightAt(x, y); w > 0 {
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

func (n *StarNeutralizer) resetPass() {
	for i := range n.stars {
		s := &n.stars[i]
		s.mappedExtent, s.mappedSourceExtent, s.peakExcess, s.sampleAt, s.peak, s.noise = s.extent, s.sourceExtent, 0, 0, [3]float64{}, 0
		s.valid = false
		s.mappedInner = 0
		s.spikes = nil
		s.samples = [3][]float64{}
	}
}

// remapCurrentFootprints measures the displayed RGB planes for this Apply.
// It follows coherent wings introduced by a changed stretch while requiring
// three of four sectors to agree, which avoids dilating across a filament or
// one-sided nebular structure. The scan is bounded by each reviewed fit.
func (n *StarNeutralizer) remapCurrentFootprints(ctx context.Context, rowsAt func(int) ([3][]float32, error)) error {
	for i := range n.stars {
		s := &n.stars[i]
		radialExtent := s.fit.OuterRadius
		if s.fit.HaloValidated {
			radialExtent = math.Max(radialExtent, s.fit.HaloRadius)
		}
		if !s.valid || s.maxSourceExtent <= radialExtent {
			continue
		}
		// Read each row once per star and bin samples by reference-grid radius.
		// This keeps disk I/O independent of the number of radial bins and uses
		// bounded local storage, rather than retaining full composite planes.
		type radialSamples struct {
			values [3][]float64
			seen   int
		}
		bins := make([][4]radialSamples, int(math.Ceil(s.maxSourceExtent)))
		// Deterministic reservoir sampling bounds storage even when alignment
		// magnifies a reference pixel into many output pixels. Both apply paths
		// visit pixels in the same order and therefore measure identical masks.
		rng := rand.New(rand.NewSource(int64(i) + 1))
		start := max(1, int(math.Floor(s.fit.InnerRadius)))
		if s.fit.InnerRadius <= 0 {
			start = max(1, int(math.Floor(s.fit.OuterRadius*.62)))
		}
		for y := max(0, int(math.Floor(s.cy-s.maxExtent))); y <= min(n.h-1, int(math.Ceil(s.cy+s.maxExtent))); y++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			rows, err := rowsAt(y)
			if err != nil {
				return err
			}
			for x := max(0, int(math.Floor(s.cx-s.maxExtent))); x <= min(n.w-1, int(math.Ceil(s.cx+s.maxExtent))); x++ {
				fx, fy := n.mapper.mapCoordinate(x, y)
				dx, dy := fx-s.fit.X, fy-s.fit.Y
				d := math.Hypot(dx, dy)
				r := int(d)
				if r < start || r >= len(bins) || d >= s.maxSourceExtent {
					continue
				}
				if w, owner := n.baseWeightAt(x, y); w > 0 && owner != i {
					continue
				}
				q := 0
				if dx < 0 {
					q++
				}
				if dy < 0 {
					q += 2
				}
				v := [3]float64{float64(rows[0][x]), float64(rows[1][x]), float64(rows[2][x])}
				if !starFinite(v[0]) || !starFinite(v[1]) || !starFinite(v[2]) {
					continue
				}
				bin := &bins[r][q]
				bin.seen++
				slot := len(bin.values[0])
				if slot >= neutralizeRadialSamples {
					slot = rng.Intn(bin.seen)
				}
				if slot >= neutralizeRadialSamples {
					continue
				}
				for c := 0; c < 3; c++ {
					value := v[c] - s.background[c]
					if slot == len(bin.values[c]) {
						bin.values[c] = append(bin.values[c], value)
					} else {
						bin.values[c][slot] = value
					}
				}
			}
		}
		threshold := math.Max(.003, 3*s.noise)
		last, quiet := 0., 0
		for r := start; r < len(bins); r++ {
			strong := 0
			for _, sector := range bins[r] {
				for c := 0; c < 3; c++ {
					if len(sector.values[c]) >= 2 && starMedian(sector.values[c]) > threshold {
						strong++
						break
					}
				}
			}
			if strong >= 3 {
				last, quiet = float64(r+1), 0
			} else {
				quiet++
			}
			if quiet >= 2 && float64(r) >= radialExtent {
				break
			}
		}
		// Only extend on measured signal beyond the original radial footprint.
		// Keep the full measured halo at the requested strength and fade only
		// its outer edge. Taking the maximum with the base mask preserves spikes.
		if last > radialExtent {
			s.mappedInner = last
			s.mappedSourceExtent = math.Min(s.maxSourceExtent, last+math.Max(2, last*.15))
			s.mappedInner = math.Min(s.mappedInner, s.mappedSourceExtent-1)
			s.mappedExtent = math.Max(s.extent, s.mappedSourceExtent*n.scale)
		}
		if err := s.detectCurrentSpikes(ctx, n, rowsAt, radialExtent); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// detectCurrentSpikes looks for narrow, opposing arms that are present in the
// displayed RGB planes even when the reference star fit did not contain them.
// It operates in source coordinates, so affine/resizing changes do not turn a
// rectangular output footprint into a circular mask. Smooth halos illuminate
// most angular bins equally and therefore fail the localized-pair test.
func (s *neutralizeStar) detectCurrentSpikes(ctx context.Context, n *StarNeutralizer, rowsAt func(int) ([3][]float32, error), radialExtent float64) error {
	if !s.valid || s.maxSourceExtent <= radialExtent+3 || s.maxSourceExtent <= s.sourceExtent+3 {
		return nil
	}
	const maxBins = neutralizeSpikeAngles
	maxRadius := min(int(math.Ceil(s.maxSourceExtent)), maxBins*3)
	if maxRadius <= int(math.Floor(radialExtent))+3 {
		return nil
	}
	maxes := make([][]float64, maxBins)
	counts := make([][]int, maxBins)
	for i := range maxes {
		maxes[i] = make([]float64, maxRadius)
		counts[i] = make([]int, maxRadius)
	}
	for y := max(0, int(math.Floor(s.cy-s.maxExtent))); y <= min(n.h-1, int(math.Ceil(s.cy+s.maxExtent))); y++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		rows, err := rowsAt(y)
		if err != nil {
			return err
		}
		for x := max(0, int(math.Floor(s.cx-s.maxExtent))); x <= min(n.w-1, int(math.Ceil(s.cx+s.maxExtent))); x++ {
			fx, fy := n.mapper.mapCoordinate(x, y)
			dx, dy := fx-s.fit.X, fy-s.fit.Y
			r := math.Hypot(dx, dy)
			ri := int(math.Floor(r))
			if r <= radialExtent || ri < 0 || ri >= maxRadius || r >= s.maxSourceExtent {
				continue
			}
			if w, owner := n.baseWeightAt(x, y); w > 0 && owner != s.index {
				continue
			}
			v := [3]float64{float64(rows[0][x]), float64(rows[1][x]), float64(rows[2][x])}
			if !starFinite(v[0]) || !starFinite(v[1]) || !starFinite(v[2]) {
				continue
			}
			residual := math.Max(0, math.Max(v[0]-s.background[0], math.Max(v[1]-s.background[1], v[2]-s.background[2])))
			if residual <= 0 {
				continue
			}
			angle := math.Atan2(dy, dx)
			if angle < 0 {
				angle += 2 * math.Pi
			}
			ai := int(angle/(2*math.Pi)*maxBins) % maxBins
			counts[ai][ri]++
			if residual > maxes[ai][ri] {
				maxes[ai][ri] = residual
			}
		}
	}
	threshold := math.Max(.004, math.Max(3*s.noise, .01*s.peakExcess))
	scores := make([]int, maxBins)
	for a := 0; a < maxBins; a++ {
		for r := int(math.Floor(radialExtent)); r < maxRadius; r++ {
			if counts[a][r] >= 1 && maxes[a][r] > threshold {
				scores[a]++
			}
		}
	}
	type spikePair struct{ angle, score int }
	pairs := make([]spikePair, 0, maxBins/2)
	for a := 0; a < maxBins/2; a++ {
		score := min(scores[a], scores[a+maxBins/2])
		pairs = append(pairs, spikePair{angle: a, score: score})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].score > pairs[j].score })
	backgroundScore := append([]int(nil), scores...)
	sort.Ints(backgroundScore)
	medianScore := backgroundScore[len(backgroundScore)/2]
	selectedAngles := make([]int, 0, 2)
	for _, pair := range pairs {
		if pair.score < 4 || pair.score < medianScore+2 || len(s.spikes) >= 4 {
			break
		}
		// Several adjacent angular bins usually represent one physical arm.
		// Suppress those bins after accepting a pair so a second orthogonal
		// diffraction axis can still be selected.
		nearSelected := false
		for _, selected := range selectedAngles {
			d := pair.angle - selected
			if d < 0 {
				d = -d
			}
			d = min(d, maxBins/2-d)
			if d <= 3 {
				nearSelected = true
				break
			}
		}
		if nearSelected {
			continue
		}
		candidate := make([]StarTreatmentSpike, 0, 2)
		for _, a := range []int{pair.angle, pair.angle + maxBins/2} {
			start, end, gaps := -1, -1, 0
			for r := int(math.Floor(radialExtent)); r < maxRadius; r++ {
				strong := counts[a][r] >= 1 && maxes[a][r] > threshold
				if strong {
					if start < 0 {
						start = r
					}
					end, gaps = r+1, 0
				} else if start >= 0 {
					gaps++
					if gaps > 1 {
						break
					}
				}
			}
			if start < 0 || end-start < 4 || start > int(math.Ceil(radialExtent))+6 {
				candidate = nil
				break
			}
			angle := 2 * math.Pi * (float64(a) + .5) / maxBins
			width := math.Max(1.25, math.Min(6, .06*float64(end)))
			candidate = append(candidate, StarTreatmentSpike{Angle: angle, StartRadius: math.Max(0, float64(start)-width), EndRadius: math.Min(s.maxSourceExtent, float64(end)+width), Width: width})
		}
		if len(candidate) == 2 {
			s.spikes = append(s.spikes, candidate...)
			selectedAngles = append(selectedAngles, pair.angle)
		}
	}
	if len(s.spikes) == 0 || len(s.spikes)%2 != 0 {
		s.spikes = nil
	}
	return nil
}

// finishBackgrounds turns gathered samples into a sigma-clipped median per
// channel. A star without enough clean annulus is left untouched, unless it
// is forced: an override then accepts whatever background the annulus gave
// it (even none, which leaves that channel's background at zero) rather than
// skip the star outright.
func (n *StarNeutralizer) finishBackgrounds() {
	for i := range n.stars {
		s := &n.stars[i]
		s.valid = len(s.samples[0]) >= neutralizeMinSamples || s.forced
		if !s.valid {
			continue
		}
		for c := 0; c < 3; c++ {
			if len(s.samples[c]) > 0 {
				s.background[c] = clippedMedian(s.samples[c])
				// Adjacent annulus differences measure noise without interpreting a
				// smooth nebular gradient across the whole annulus as random noise.
				if len(s.samples[c]) >= 2 {
					diffs := make([]float64, len(s.samples[c])-1)
					for j := range diffs {
						diffs[j] = s.samples[c][j+1] - s.samples[c][j]
					}
					center := starMedian(append([]float64(nil), diffs...))
					for j := range diffs {
						diffs[j] = math.Abs(diffs[j] - center)
					}
					s.noise = math.Max(s.noise, 1.4826*starMedian(diffs)/math.Sqrt2)
				}
			}
			s.samples[c] = nil
			s.peakExcess = math.Max(s.peakExcess, s.peak[c]-s.background[c])
		}
		if s.peakExcess <= 0 && !s.forced {
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
		}
		// The footprint supplies the spatial fade; a second peak-based fade
		// would leave the measured red wings colored even at full strength.
		for c := 0; c < 3; c++ {
			if !selected[c] {
				continue
			}
			v := float64(rows[c][x]) + a*(neutral-excess[c])
			rows[c][x] = float32(math.Max(0, math.Min(1, v)))
		}
	}
}

// Apply measures backgrounds and current footprints, then whitens in-memory
// planes in place. Measurements from previous calls are discarded.
func (n *StarNeutralizer) Apply(ctx context.Context, planes [3][]float32) error {
	for c := range planes {
		if len(planes[c]) != n.w*n.h {
			return fmt.Errorf("white stars: plane %d has %d samples, composite is %dx%d", c, len(planes[c]), n.w, n.h)
		}
	}
	rowsAt := func(y int) [3][]float32 {
		return [3][]float32{planes[0][y*n.w : (y+1)*n.w], planes[1][y*n.w : (y+1)*n.w], planes[2][y*n.w : (y+1)*n.w]}
	}
	n.resetPass()
	for y := 0; y < n.h; y++ {
		if y%64 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		n.gather(y, rowsAt(y))
	}
	n.finishBackgrounds()
	if err := n.remapCurrentFootprints(ctx, func(y int) ([3][]float32, error) { return rowsAt(y), nil }); err != nil {
		return err
	}
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
	n.resetPass()
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
	if err := n.remapCurrentFootprints(ctx, func(y int) ([3][]float32, error) {
		if err := read(y); err != nil {
			return [3][]float32{}, err
		}
		return rows, nil
	}); err != nil {
		return err
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
		n.applyRow(y, rows)
		for c := range arts {
			if err := arts[c].WriteRow(y, rows[c]); err != nil {
				return err
			}
		}
	}
	return ctx.Err()
}
