package processing

import (
	"fmt"
	"math"

	"gofitsv3/internal/models"
	"gofitsv3/internal/stretch"
)

// StarTreatmentModel evaluates the gentler star stretch for one source at
// arbitrary source-grid positions. It holds the prepared usable fits, the
// scalar stretch settings they were prepared against, the strength, and a
// coarse grid index so a sample touches only the stars whose footprint can
// reach it. Both compositors share it, so the treated result is defined once:
// a linear sample at a source position maps to one treated stretched value.
type StarTreatmentModel struct {
	fits     []StarTreatmentFit
	meta     models.LoadedImage
	strength float64
	w, h     int
	cell     int
	cols     int
	rows     int
	buckets  [][]int
}

const starTreatmentIndexCell = 64

// NewStarTreatmentModel validates prepared fits and stretch settings the same
// way ApplyGentlerStarStretch does and builds the spatial index. Fits that are
// not usable are dropped. Strength zero yields the ordinary stretch.
func NewStarTreatmentModel(fits []StarTreatmentFit, w, h int, meta models.LoadedImage, strength float64) (*StarTreatmentModel, error) {
	if !validStarTreatmentDimensions(w, h) {
		return nil, fmt.Errorf("invalid star treatment dimensions")
	}
	if err := validateStarStretchMeta(meta); err != nil {
		return nil, err
	}
	if !starFinite(strength) || strength < 0 || strength > 1 {
		return nil, fmt.Errorf("star stretch strength must be between 0 and 1")
	}
	m := &StarTreatmentModel{meta: scalarStretchMeta(meta), strength: strength, w: w, h: h, cell: starTreatmentIndexCell}
	m.cols = (w + m.cell - 1) / m.cell
	m.rows = (h + m.cell - 1) / m.cell
	m.buckets = make([][]int, m.cols*m.rows)
	for _, f := range fits {
		if !f.Usable {
			continue
		}
		if !validStarTreatmentFit(f, w, h) {
			return nil, fmt.Errorf("star %d has an invalid treatment model", f.SourceID)
		}
		idx := len(m.fits)
		m.fits = append(m.fits, f)
		extent := StarTreatmentExtent(f)
		c0, c1 := m.clampCol(int(math.Floor((f.X-extent)/float64(m.cell)))), m.clampCol(int(math.Floor((f.X+extent)/float64(m.cell))))
		r0, r1 := m.clampRow(int(math.Floor((f.Y-extent)/float64(m.cell)))), m.clampRow(int(math.Floor((f.Y+extent)/float64(m.cell))))
		for r := r0; r <= r1; r++ {
			for c := c0; c <= c1; c++ {
				m.buckets[r*m.cols+c] = append(m.buckets[r*m.cols+c], idx)
			}
		}
	}
	return m, nil
}

func (m *StarTreatmentModel) clampCol(c int) int { return max(0, min(m.cols-1, c)) }
func (m *StarTreatmentModel) clampRow(r int) int { return max(0, min(m.rows-1, r)) }

// Fits returns the usable fits the model renders, in index order.
func (m *StarTreatmentModel) Fits() []StarTreatmentFit { return m.fits }

// Strength returns the treatment strength the model was built with.
func (m *StarTreatmentModel) Strength() float64 { return m.strength }

// SourceSize is the source grid the fits were measured on. A treatment is
// only valid on that grid.
func (m *StarTreatmentModel) SourceSize() (int, int) { return m.w, m.h }

// MatchesStretch reports whether the model was prepared for the given scalar
// stretch settings. The prepared footprint depends on those settings, so a
// compositor must rebuild the model rather than apply a stale one.
func (m *StarTreatmentModel) MatchesStretch(img models.LoadedImage) bool {
	a, b := m.meta, scalarStretchMeta(img)
	return a.Mode == b.Mode && a.Background == b.Background && a.Peak == b.Peak && a.ScaledPeak == b.ScaledPeak &&
		a.AsinhScale == b.AsinhScale && a.MTFMidtone == b.MTFMidtone
}

// bucketAt returns the indices of fits whose footprint may reach a source
// position; empty outside the grid.
func (m *StarTreatmentModel) bucketAt(x, y float64) []int {
	if x < 0 || y < 0 || x >= float64(m.w) || y >= float64(m.h) {
		return nil
	}
	return m.buckets[m.clampRow(int(y)/m.cell)*m.cols+m.clampCol(int(x)/m.cell)]
}

// TreatedStretch renders one linear sample at a source-grid position. Outside
// every footprint, or at strength zero, it equals the ordinary scalar stretch.
func (m *StarTreatmentModel) TreatedStretch(v float32, x, y float64) float32 {
	return m.stretchAt(v, x, y, -1)
}

// stretchAt is the shared per-sample rule. maskWeight below zero means no
// external mask; otherwise it caps every star's weight, as the preview's
// editable mask does. Corrections from overlapping stars max-combine so a
// shared pixel is never treated twice.
func (m *StarTreatmentModel) stretchAt(v float32, x, y float64, maskWeight float64) float32 {
	base := stretchDiskValue(v, m.meta)
	if m.strength == 0 || len(m.fits) == 0 || maskWeight == 0 {
		return base
	}
	if x < 0 || y < 0 || x >= float64(m.w) || y >= float64(m.h) {
		return base
	}
	bucket := m.buckets[m.clampRow(int(y)/m.cell)*m.cols+m.clampCol(int(x)/m.cell)]
	if len(bucket) == 0 {
		return base
	}
	correction := 0.
	for _, idx := range bucket {
		f := &m.fits[idx]
		weight := starTreatmentFitWeight(x, y, *f)
		if weight == 0 {
			continue
		}
		if maskWeight >= 0 {
			weight = math.Min(weight, maskWeight)
		}
		if weight == 0 {
			continue
		}
		// An invalid sample has no stellar light to compress; it keeps its
		// ordinary rendering. Nothing is reconstructed in its place.
		if !starFinite(float64(v)) {
			return base
		}
		b := starTreatmentBackground(x, y, *f)
		q := unclippedStarStretch(float64(v), m.meta)
		background := unclippedStarStretch(b, m.meta)
		if !starFinite(q) || !starFinite(background) {
			return base
		}
		// Treat the observed positive stellar excess above the fitted local
		// background, before display clipping. No missing flux is reconstructed.
		// E/(1+k*E) is monotonic, nonnegative and never exceeds E.
		excess := math.Max(0, q-background)
		gentle := excess / (1 + 4*m.strength*excess)
		candidate := q - weight*(excess-gentle)
		candidate = math.Max(0, math.Min(1, candidate))
		correction = math.Max(correction, math.Max(0, float64(base)-candidate))
	}
	return float32(math.Max(0, float64(base)-correction)) // roundoff only
}

// scalarStretchMeta keeps only the settings the scalar stretch depends on, so
// image buffers and headers never ride along with a model.
func scalarStretchMeta(img models.LoadedImage) models.LoadedImage {
	return models.LoadedImage{Mode: img.Mode, Background: img.Background, Peak: img.Peak, ScaledPeak: img.ScaledPeak,
		AsinhScale: img.AsinhScale, MTFMidtone: img.MTFMidtone}
}

func validateStarStretchMeta(meta models.LoadedImage) error {
	switch meta.Mode {
	case stretch.Linear, stretch.Log, stretch.Asinh, stretch.Sqrt, stretch.MTF:
	default:
		return fmt.Errorf("star treatment requires Linear, Log, Asinh, Sqrt or MTF")
	}
	for _, v := range []float64{meta.Background, meta.Peak, meta.ScaledPeak, meta.AsinhScale, meta.MTFMidtone} {
		if !starFinite(v) {
			return fmt.Errorf("nonfinite stretch metadata")
		}
	}
	if meta.Peak <= meta.Background || meta.ScaledPeak < 0 || meta.AsinhScale < 0 || meta.MTFMidtone < 0 || meta.MTFMidtone >= 1 {
		return fmt.Errorf("invalid stretch levels or parameters")
	}
	return nil
}
