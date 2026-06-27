package stretch

import (
	"math"
	"runtime"
	"sync"
)

type Mode int

const (
	Linear Mode = iota
	Log
	Asinh
	Sqrt
	HistEq
	// MTF is the midtones transfer function (PixInsight STF-style), driven by a
	// single midtone parameter.
	MTF
	// GHS is the Generalised Hyperbolic Stretch (Payne "normal" form), driven by
	// stretch strength D, local intensity b, and symmetry point SP.
	GHS
)

// Default parameters used when a channel has no explicit value set yet.
const (
	DefaultAsinhScale  = 1.0  // softening beta; smaller stretches faint detail harder
	DefaultMTFMidtone  = 0.25 // maps the midtone to ~0.25 like a typical auto-STF
	DefaultGHSStretch  = 1.0  // D
	DefaultGHSLocal    = 0.0  // b (0 => exponential branch)
	DefaultGHSSymmetry = 0.1  // SP
)

// Mtf applies the midtones transfer function with midtone m in (0,1) to x.
// Inputs and outputs are in [0,1].
func Mtf(m, x float64) float64 {
	if x <= 0 {
		return 0
	}
	if x >= 1 {
		return 1
	}
	if m <= 0 {
		return 1
	}
	if m >= 1 {
		return 0
	}
	if x == m {
		return 0.5
	}
	return ((m - 1) * x) / ((2*m-1)*x - m)
}

// GHSParams holds precomputed coefficients for the Generalised Hyperbolic
// Stretch (Payne "normal" form, ported from Siril's STRETCH_PAYNE_NORMAL).
// Construct once per image with NewGHS, then evaluate per pixel with Eval.
type GHSParams struct {
	identity           bool
	expMode            bool // b == 0 => exponential branch
	b, lp, sp, hp      float64
	b1                 float64
	a2, b2, c2, d2, e2 float64
	a3, b3, c3, d3, e3 float64
	a4, b4             float64
}

// NewGHS precomputes the GHS coefficients. D is stretch strength (>0), b is the
// local intensity, SP the symmetry point, and LP/HP the linear shadow/highlight
// protection bounds (typically 0 and 1). D <= 0 yields an identity transform.
func NewGHS(D, b, SP, LP, HP float64) GHSParams {
	if D <= 0 || HP <= LP {
		return GHSParams{identity: true}
	}
	p := GHSParams{b: b, lp: LP, sp: SP, hp: HP}
	if b == 0 {
		p.expMode = true
		qlp := math.Exp(-D * (SP - LP))
		q0 := qlp - D*LP*math.Exp(-D*(SP-LP))
		qwp := 2.0 - math.Exp(-D*(HP-SP))
		q1 := qwp + D*(1.0-HP)*math.Exp(-D*(HP-SP))
		q := 1.0 / (q1 - q0)
		p.b1 = D * math.Exp(-D*(SP-LP)) * q
		p.a2 = -q0 * q
		p.b2 = q
		p.c2 = -D * SP
		p.d2 = D
		p.a3 = (2.0 - q0) * q
		p.b3 = -q
		p.c3 = D * SP
		p.d3 = -D
		p.a4 = (qwp - q0 - D*HP*math.Exp(-D*(HP-SP))) * q
		p.b4 = D * math.Exp(-D*(HP-SP)) * q
		return p
	}
	if b < 0 {
		// Negative local intensity uses the logarithmic-form coefficients. The
		// magnitude B keeps the power bases positive. B == 1 (b == -1) is a
		// removable singularity (1/(B-1)); nudge past it for stability.
		B := -b
		if math.Abs(B-1.0) < 1e-6 {
			B = 1.0 + 1e-6
		}
		qlp := (1.0 - math.Pow(1.0+D*B*(SP-LP), (B-1.0)/B)) / (B - 1.0)
		q0 := qlp - D*LP*math.Pow(1.0+D*B*(SP-LP), -1.0/B)
		qwp := (math.Pow(1.0+D*B*(HP-SP), (B-1.0)/B) - 1.0) / (B - 1.0)
		q1 := qwp + D*(1.0-HP)*math.Pow(1.0+D*B*(HP-SP), -1.0/B)
		q := 1.0 / (q1 - q0)
		p.b1 = D * math.Pow(1.0+D*B*(SP-LP), -1.0/B) * q
		p.a2 = (1.0/(B-1.0) - q0) * q
		p.b2 = -q / (B - 1.0)
		p.c2 = 1.0 + D*B*SP
		p.d2 = -D * B
		p.e2 = (B - 1.0) / B
		p.a3 = (-1.0/(B-1.0) - q0) * q
		p.b3 = q / (B - 1.0)
		p.c3 = 1.0 - D*B*SP
		p.d3 = D * B
		p.e3 = (B - 1.0) / B
		p.a4 = (qwp - q0 - D*HP*math.Pow(1.0+D*B*(HP-SP), -1.0/B)) * q
		p.b4 = D * math.Pow(1.0+D*B*(HP-SP), -1.0/B) * q
		return p
	}
	qlp := math.Pow(1.0+D*b*(SP-LP), -1.0/b)
	q0 := qlp - D*LP*math.Pow(1.0+D*b*(SP-LP), -(1.0+b)/b)
	qwp := 2.0 - math.Pow(1.0+D*b*(HP-SP), -1.0/b)
	q1 := qwp + D*(1.0-HP)*math.Pow(1.0+D*b*(HP-SP), -(1.0+b)/b)
	q := 1.0 / (q1 - q0)
	p.b1 = D * math.Pow(1.0+D*b*(SP-LP), -(1.0+b)/b) * q
	p.a2 = -q0 * q
	p.b2 = q
	p.c2 = 1.0 + D*b*SP
	p.d2 = -D * b
	p.e2 = -1.0 / b
	p.a3 = (2.0 - q0) * q
	p.b3 = -q
	p.c3 = 1.0 - D*b*SP
	p.d3 = D * b
	p.e3 = -1.0 / b
	p.a4 = (qwp - q0 - D*HP*math.Pow(1.0+D*b*(HP-SP), -(b+1.0)/b)) * q
	p.b4 = D * math.Pow(1.0+D*b*(HP-SP), -(b+1.0)/b) * q
	return p
}

// Eval maps x in [0,1] through the precomputed GHS transform.
func (p GHSParams) Eval(x float64) float64 {
	if p.identity {
		return clamp01(x)
	}
	if x < 0 {
		x = 0
	} else if x > 1 {
		x = 1
	}
	var out float64
	switch {
	case x < p.lp:
		out = p.b1 * x
	case x < p.sp:
		if p.expMode {
			out = p.a2 + p.b2*math.Exp(p.c2+p.d2*x)
		} else {
			out = p.a2 + p.b2*powGuard(p.c2+p.d2*x, p.e2)
		}
	case x < p.hp:
		if p.expMode {
			out = p.a3 + p.b3*math.Exp(p.c3+p.d3*x)
		} else {
			out = p.a3 + p.b3*powGuard(p.c3+p.d3*x, p.e3)
		}
	default:
		out = p.a4 + p.b4*x
	}
	return clamp01(out)
}

// powGuard avoids NaN from a non-positive base (possible when b < 0 and the
// stretch is strong) by clamping the base to a tiny positive value.
func powGuard(base, exp float64) float64 {
	if base <= 0 {
		base = 1e-12
	}
	return math.Pow(base, exp)
}

func clamp01(v float64) float64 {
	if v < 0 || math.IsNaN(v) {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func parallel(pixels, out []float32, fn func(i int, v float32) float32) {
	n := runtime.NumCPU()
	chunk := (len(pixels) + n - 1) / n
	var wg sync.WaitGroup
	for g := 0; g < n; g++ {
		start := g * chunk
		if start >= len(pixels) {
			break
		}
		end := start + chunk
		if end > len(pixels) {
			end = len(pixels)
		}
		wg.Add(1)
		go func(s, e int) {
			defer wg.Done()
			for i := s; i < e; i++ {
				out[i] = fn(i, pixels[i])
			}
		}(start, end)
	}
	wg.Wait()
}

func Apply(pixels []float32, mode Mode) []float32 {
	out := make([]float32, len(pixels))
	switch mode {
	case Linear:
		copy(out, pixels)
	case Log:
		scale := math.Log1p(9)
		parallel(pixels, out, func(_ int, v float32) float32 {
			return float32(math.Log1p(9*float64(v)) / scale)
		})
	case Asinh:
		scale := math.Asinh(10)
		parallel(pixels, out, func(_ int, v float32) float32 {
			return float32(math.Asinh(10*float64(v)) / scale)
		})
	case Sqrt:
		parallel(pixels, out, func(_ int, v float32) float32 {
			return float32(math.Sqrt(float64(v)))
		})
	case MTF:
		parallel(pixels, out, func(_ int, v float32) float32 {
			return float32(Mtf(DefaultMTFMidtone, float64(v)))
		})
	case GHS:
		g := NewGHS(DefaultGHSStretch, DefaultGHSLocal, DefaultGHSSymmetry, 0, 1)
		parallel(pixels, out, func(_ int, v float32) float32 {
			return float32(g.Eval(float64(v)))
		})
	case HistEq:
		hist := make([]int, 256)
		for _, v := range pixels {
			idx := int(v * 255)
			if idx < 0 {
				idx = 0
			}
			if idx > 255 {
				idx = 255
			}
			hist[idx]++
		}
		cdf := make([]float32, 256)
		total := float64(len(pixels))
		sum := 0
		for i, h := range hist {
			sum += h
			cdf[i] = float32(float64(sum) / total)
		}
		for i, v := range pixels {
			idx := int(v * 255)
			if idx < 0 {
				idx = 0
			}
			if idx > 255 {
				idx = 255
			}
			out[i] = cdf[idx]
		}
	}
	return out
}
