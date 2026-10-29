package stretch

import (
	"fmt"
	"math"
	"strings"
)

// modeNames are the persisted spellings of each Mode. Project files store
// these strings, so they must never change.
var modeNames = [...]string{
	Linear: "Linear",
	Log:    "Log",
	Asinh:  "Asinh",
	Sqrt:   "Sqrt",
	HistEq: "HistEq",
	MTF:    "MTF",
	GHS:    "GHS",
}

// Valid reports whether m is a known stretch mode.
func (m Mode) Valid() bool { return m >= 0 && int(m) < len(modeNames) }

// String returns the persisted name of m.
func (m Mode) String() string {
	if !m.Valid() {
		return fmt.Sprintf("Mode(%d)", int(m))
	}
	return modeNames[m]
}

// ParseMode resolves a persisted or user-typed mode name, ignoring case.
func ParseMode(s string) (Mode, bool) {
	for i, name := range modeNames {
		if strings.EqualFold(s, name) {
			return Mode(i), true
		}
	}
	return Linear, false
}

// Params are the settings the scalar stretch depends on. Zero tuning values
// mean "unset" and resolve to the package defaults.
type Params struct {
	Mode        Mode
	Background  float64
	Peak        float64
	ScaledPeak  float64
	AsinhScale  float64
	MTFMidtone  float64
	GHSStretch  float64
	GHSLocal    float64
	GHSSymmetry float64
}

// Scalar is a per-pixel stretch with its levels, defaults and GHS
// coefficients resolved once. Build it per image with NewScalar, then call
// Apply or Eval per sample; neither allocates.
type Scalar struct {
	mode       Mode
	bg, rng    float64 // background and peak-background
	scaled     float64
	denom      float64 // per-mode output normaliser
	asinhScale float64
	mtf        float64
	ghs        GHSParams
}

// NewScalar sanitises the levels and resolves the mode parameters.
// HistEq is not a scalar stretch: Scalar yields the normalised linear value
// and callers apply the CDF separately.
func NewScalar(p Params) Scalar {
	bg, peak, scaled := p.Background, p.Peak, p.ScaledPeak
	if math.IsNaN(bg) || math.IsInf(bg, 0) {
		bg = 0
	}
	if math.IsNaN(peak) || math.IsInf(peak, 0) || peak <= bg {
		peak = bg + 1
	}
	if scaled <= 0 || math.IsNaN(scaled) || math.IsInf(scaled, 0) {
		scaled = 100
	}
	s := Scalar{mode: p.Mode, bg: bg, rng: peak - bg, scaled: scaled}
	switch p.Mode {
	case Log:
		s.denom = math.Log1p(scaled)
	case Asinh:
		s.asinhScale = p.AsinhScale
		if s.asinhScale <= 0 {
			s.asinhScale = DefaultAsinhScale
		}
		s.denom = math.Asinh(scaled / s.asinhScale)
	case Sqrt:
		s.denom = math.Sqrt(scaled)
	case MTF:
		s.mtf = p.MTFMidtone
		if s.mtf <= 0 || s.mtf >= 1 {
			s.mtf = DefaultMTFMidtone
		}
	case GHS:
		d := p.GHSStretch
		if d <= 0 {
			d = DefaultGHSStretch
		}
		sp := p.GHSSymmetry
		if sp <= 0 || sp >= 1 {
			sp = DefaultGHSSymmetry
		}
		s.ghs = NewGHS(d, p.GHSLocal, sp, 0, 1)
	}
	return s
}

// Mode returns the stretch mode s was built for.
func (s *Scalar) Mode() Mode { return s.mode }

// Eval stretches a finite sample without the final clip, so callers can
// detect highlight clipping. The result may exceed 1 or be NaN.
func (s *Scalar) Eval(v float64) float64 {
	x := (v - s.bg) * s.scaled / s.rng
	if x < 0 {
		x = 0
	}
	switch s.mode {
	case Log:
		return math.Log1p(x) / s.denom
	case Asinh:
		return math.Asinh(x/s.asinhScale) / s.denom
	case Sqrt:
		return math.Sqrt(x) / s.denom
	case MTF:
		return Mtf(s.mtf, x/s.scaled)
	case GHS:
		return s.ghs.Eval(x / s.scaled)
	default:
		return x / s.scaled
	}
}

// Apply stretches one sample into [0,1]; NaN and Inf map to 0.
func (s *Scalar) Apply(v float32) float32 {
	if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
		return 0
	}
	out := s.Eval(float64(v))
	if out < 0 || math.IsNaN(out) || math.IsInf(out, 0) {
		out = 0
	}
	if out > 1 {
		out = 1
	}
	return float32(out)
}
