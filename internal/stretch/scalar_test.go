package stretch

import (
	"math"
	"strings"
	"testing"
)

// legacyDiskScalar is a verbatim copy of the per-pixel stretch that
// processing.stretchDiskValue and export.stretchScalar used before Scalar.
// It rebuilds every coefficient per call and is kept as the golden oracle.
func legacyDiskScalar(v float32, img Params) float32 {
	if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
		return 0
	}
	bg, peak, scaled := img.Background, img.Peak, img.ScaledPeak
	if math.IsNaN(bg) || math.IsInf(bg, 0) {
		bg = 0
	}
	if math.IsNaN(peak) || math.IsInf(peak, 0) || peak <= bg {
		peak = bg + 1
	}
	if scaled <= 0 || math.IsNaN(scaled) || math.IsInf(scaled, 0) {
		scaled = 100
	}
	x := (float64(v) - bg) * scaled / (peak - bg)
	if x < 0 {
		x = 0
	}
	var out float64
	switch img.Mode {
	case HistEq:
		out = x / scaled
	case Log:
		out = math.Log1p(x) / math.Log1p(scaled)
	case Asinh:
		b := img.AsinhScale
		if b <= 0 {
			b = DefaultAsinhScale
		}
		out = math.Asinh(x/b) / math.Asinh(scaled/b)
	case Sqrt:
		out = math.Sqrt(x) / math.Sqrt(scaled)
	case MTF:
		m := img.MTFMidtone
		if m <= 0 || m >= 1 {
			m = DefaultMTFMidtone
		}
		out = Mtf(m, x/scaled)
	case GHS:
		d := img.GHSStretch
		if d <= 0 {
			d = DefaultGHSStretch
		}
		sp := img.GHSSymmetry
		if sp <= 0 || sp >= 1 {
			sp = DefaultGHSSymmetry
		}
		out = NewGHS(d, img.GHSLocal, sp, 0, 1).Eval(x / scaled)
	default:
		out = x / scaled
	}
	if out < 0 || math.IsNaN(out) || math.IsInf(out, 0) {
		out = 0
	}
	if out > 1 {
		out = 1
	}
	return float32(out)
}

// legacyInMemoryScalar is a verbatim copy of the hoisted per-pixel body of
// processing.ApplyStretchParallel before Scalar (multiply-by-inverse form),
// returning the unclipped result for a finite sample.
func legacyInMemoryScalar(v float64, img Params) float64 {
	background, peak, scaledPeak := img.Background, img.Peak, img.ScaledPeak
	if math.IsNaN(background) || math.IsInf(background, 0) {
		background = 0
	}
	if math.IsNaN(peak) || math.IsInf(peak, 0) || peak <= background {
		peak = background + 1
	}
	if math.IsNaN(scaledPeak) || math.IsInf(scaledPeak, 0) || scaledPeak <= 0 {
		scaledPeak = 100
	}
	stretchMul := scaledPeak / (peak - background)
	invLogPeak := 1.0 / math.Log1p(scaledPeak)
	invSqrtPeak := 1.0 / math.Sqrt(scaledPeak)
	invLinearPeak := 1.0 / scaledPeak
	asinhScale := img.AsinhScale
	if asinhScale <= 0 {
		asinhScale = DefaultAsinhScale
	}
	invAsinhPeak := 1.0 / math.Asinh(scaledPeak/asinhScale)
	mtfMidtone := img.MTFMidtone
	if mtfMidtone <= 0 || mtfMidtone >= 1 {
		mtfMidtone = DefaultMTFMidtone
	}
	ghsD := img.GHSStretch
	if ghsD <= 0 {
		ghsD = DefaultGHSStretch
	}
	ghsSP := img.GHSSymmetry
	if ghsSP <= 0 || ghsSP >= 1 {
		ghsSP = DefaultGHSSymmetry
	}
	ghs := NewGHS(ghsD, img.GHSLocal, ghsSP, 0, 1)
	val := (v - background) * stretchMul
	if val < 0 {
		val = 0
	}
	switch img.Mode {
	case Log:
		return math.Log1p(val) * invLogPeak
	case Asinh:
		return math.Asinh(val/asinhScale) * invAsinhPeak
	case Sqrt:
		return math.Sqrt(val) * invSqrtPeak
	case MTF:
		return Mtf(mtfMidtone, val*invLinearPeak)
	case GHS:
		return ghs.Eval(val * invLinearPeak)
	default:
		return val * invLinearPeak
	}
}

// goldenCases cover every mode, the defaulting rules and the GHS branches:
// exponential (b=0), hyperbolic (b>0), logarithmic (b<0), its b=-1
// singularity nudge and the negative-base powGuard (strong D with b<0).
var goldenCases = []struct {
	name string
	p    Params
}{
	{"linear", Params{Mode: Linear, Background: 10, Peak: 1000, ScaledPeak: 50}},
	{"linear-default-levels", Params{Mode: Linear, Background: math.NaN(), Peak: math.Inf(1), ScaledPeak: 0}},
	{"linear-peak-below-bg", Params{Mode: Linear, Background: 5, Peak: 2, ScaledPeak: -3}},
	{"log", Params{Mode: Log, Background: 10, Peak: 1000, ScaledPeak: 100}},
	{"asinh-default", Params{Mode: Asinh, Background: 0, Peak: 500, ScaledPeak: 20}},
	{"asinh-beta", Params{Mode: Asinh, Background: 3, Peak: 500, ScaledPeak: 20, AsinhScale: 0.05}},
	{"sqrt", Params{Mode: Sqrt, Background: 0, Peak: 1, ScaledPeak: 1}},
	{"histeq-normalised", Params{Mode: HistEq, Background: 10, Peak: 1000, ScaledPeak: 100}},
	{"mtf-default", Params{Mode: MTF, Background: 0, Peak: 1000, ScaledPeak: 10, MTFMidtone: 1.5}},
	{"mtf", Params{Mode: MTF, Background: 0, Peak: 1000, ScaledPeak: 10, MTFMidtone: 0.05}},
	{"ghs-default", Params{Mode: GHS, Background: 0, Peak: 1000, ScaledPeak: 10}},
	{"ghs-exp", Params{Mode: GHS, Background: 0, Peak: 1000, ScaledPeak: 10, GHSStretch: 5, GHSLocal: 0, GHSSymmetry: 0.2}},
	{"ghs-b-positive", Params{Mode: GHS, Background: 0, Peak: 1000, ScaledPeak: 10, GHSStretch: 3, GHSLocal: 2, GHSSymmetry: 0.3}},
	{"ghs-b-negative-log", Params{Mode: GHS, Background: 0, Peak: 1000, ScaledPeak: 10, GHSStretch: 3, GHSLocal: -0.5, GHSSymmetry: 0.3}},
	{"ghs-b-minus-one-singularity", Params{Mode: GHS, Background: 0, Peak: 1000, ScaledPeak: 10, GHSStretch: 4, GHSLocal: -1, GHSSymmetry: 0.25}},
	{"ghs-b-negative-guard", Params{Mode: GHS, Background: 0, Peak: 1000, ScaledPeak: 10, GHSStretch: 50, GHSLocal: -5, GHSSymmetry: 0.5}},
	{"ghs-bad-symmetry", Params{Mode: GHS, Background: 0, Peak: 1000, ScaledPeak: 10, GHSStretch: -1, GHSLocal: 1, GHSSymmetry: 1}},
}

func goldenSamples(p Params) []float32 {
	vals := []float32{float32(math.NaN()), float32(math.Inf(1)), float32(math.Inf(-1)), -1e6, -1, 0}
	lo, hi := p.Background, p.Peak
	if !(hi > lo) || math.IsNaN(lo) || math.IsInf(hi, 0) {
		lo, hi = 0, 1
	}
	for i := 0; i <= 2000; i++ {
		// Sweep below, across and above [bg, peak].
		vals = append(vals, float32(lo+(hi-lo)*(float64(i)/1000-0.5)))
	}
	return append(vals, float32(hi*10), 1e30)
}

func TestScalarMatchesLegacyDiskStretch(t *testing.T) {
	for _, tc := range goldenCases {
		s := NewScalar(tc.p)
		for _, v := range goldenSamples(tc.p) {
			want := legacyDiskScalar(v, tc.p)
			// The disk path keeps the legacy operation order, so it is bit-identical.
			if got := s.Apply(v); got != want {
				t.Fatalf("%s: Apply(%g) = %g, legacy %g", tc.name, v, got, want)
			}
		}
	}
}

func TestScalarEvalMatchesLegacyInMemoryStretch(t *testing.T) {
	for _, tc := range goldenCases {
		if tc.p.Mode == HistEq {
			continue // the in-memory path equalises after the linear pass
		}
		s := NewScalar(tc.p)
		for _, v := range goldenSamples(tc.p) {
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				continue // ApplyStretchParallel filters these before stretching
			}
			want := legacyInMemoryScalar(float64(v), tc.p)
			got := s.Eval(float64(v))
			if math.IsNaN(want) != math.IsNaN(got) || math.Abs(got-want) > 1e-9*math.Max(1, math.Abs(want)) {
				t.Fatalf("%s: Eval(%g) = %g, legacy %g", tc.name, v, got, want)
			}
		}
	}
}

func TestScalarHistEqCDFMatchesLegacy(t *testing.T) {
	p := Params{Mode: HistEq, Background: 10, Peak: 1000, ScaledPeak: 100}
	samples := goldenSamples(p)
	s := NewScalar(p)
	legacy, got := make([]float32, len(samples)), make([]float32, len(samples))
	for i, v := range samples {
		legacy[i], got[i] = legacyDiskScalar(v, p), s.Apply(v)
	}
	want, have := Apply(legacy, HistEq), Apply(got, HistEq)
	for i := range want {
		if want[i] != have[i] {
			t.Fatalf("HistEq CDF[%d] = %g, legacy %g", i, have[i], want[i])
		}
	}
}

func TestModeStringRoundTrip(t *testing.T) {
	persisted := map[Mode]string{Linear: "Linear", Log: "Log", Asinh: "Asinh", Sqrt: "Sqrt", HistEq: "HistEq", MTF: "MTF", GHS: "GHS"}
	for m, name := range persisted {
		if m.String() != name {
			t.Fatalf("%d.String() = %q, want %q", m, m.String(), name)
		}
		for _, in := range []string{name, strings.ToLower(name)} {
			if got, ok := ParseMode(in); !ok || got != m {
				t.Fatalf("ParseMode(%q) = %v, %v", in, got, ok)
			}
		}
	}
	if _, ok := ParseMode("bogus"); ok {
		t.Fatal("ParseMode accepted an unknown name")
	}
	if Mode(99).Valid() || Mode(-1).Valid() {
		t.Fatal("out-of-range mode reported valid")
	}
}
