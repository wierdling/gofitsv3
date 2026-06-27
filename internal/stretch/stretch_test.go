package stretch

import (
	"math"
	"testing"
)

func TestApplyLinearCopiesInput(t *testing.T) {
	input := []float32{0, 0.25, 1}
	got := Apply(input, Linear)

	for i := range input {
		if got[i] != input[i] {
			t.Fatalf("got[%d] = %v, want %v", i, got[i], input[i])
		}
	}

	got[0] = 99
	if input[0] == 99 {
		t.Fatal("Apply should return a copy for Linear mode")
	}
}

func TestApplyTransformModesAreMonotonicAndBounded(t *testing.T) {
	input := []float32{0, 0.25, 0.5, 1}
	tests := []struct {
		name string
		mode Mode
	}{
		{name: "log", mode: Log},
		{name: "asinh", mode: Asinh},
		{name: "sqrt", mode: Sqrt},
		{name: "mtf", mode: MTF},
		{name: "ghs", mode: GHS},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Apply(input, tt.mode)
			if len(got) != len(input) {
				t.Fatalf("len(got) = %d, want %d", len(got), len(input))
			}
			for i := range got {
				if got[i] < 0 || got[i] > 1 {
					t.Fatalf("got[%d] = %v, want value in [0,1]", i, got[i])
				}
				if i > 0 && got[i] < got[i-1] {
					t.Fatalf("sequence is not monotonic: %v", got)
				}
			}
			if got[0] != 0 {
				t.Fatalf("got[0] = %v, want 0", got[0])
			}
			if got[len(got)-1] != 1 {
				t.Fatalf("last value = %v, want 1", got[len(got)-1])
			}
		})
	}
}

func TestApplySqrtNegativeInputProducesNaN(t *testing.T) {
	got := Apply([]float32{-1, 0, 1}, Sqrt)
	if !math.IsNaN(float64(got[0])) {
		t.Fatalf("got[0] = %v, want NaN", got[0])
	}
	if got[1] != 0 || got[2] != 1 {
		t.Fatalf("unexpected remaining values: %v", got)
	}
}

func TestApplyHistEqProducesExpectedCDFValues(t *testing.T) {
	input := []float32{0, 0, 1, 1}
	got := Apply(input, HistEq)

	want := []float32{0.5, 0.5, 1, 1}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestApplyHistEqClampsOutOfRangeInput(t *testing.T) {
	input := []float32{-1, 2}
	got := Apply(input, HistEq)
	want := []float32{0.5, 1}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestMtfEndpointsAndMidtone(t *testing.T) {
	if got := Mtf(0.25, 0); got != 0 {
		t.Fatalf("Mtf(_,0) = %v, want 0", got)
	}
	if got := Mtf(0.25, 1); got != 1 {
		t.Fatalf("Mtf(_,1) = %v, want 1", got)
	}
	// The midtone maps to exactly 0.5 by definition.
	if got := Mtf(0.25, 0.25); math.Abs(got-0.5) > 1e-12 {
		t.Fatalf("Mtf(0.25,0.25) = %v, want 0.5", got)
	}
	// A low midtone lifts shadows: x below the midtone stretches above the line.
	if got := Mtf(0.25, 0.1); got <= 0.1 {
		t.Fatalf("Mtf(0.25,0.1) = %v, want > 0.1 (shadow lift)", got)
	}
}

func TestMtfMonotonic(t *testing.T) {
	prev := -1.0
	for i := 0; i <= 100; i++ {
		x := float64(i) / 100
		v := Mtf(0.2, x)
		if v < 0 || v > 1 {
			t.Fatalf("Mtf out of range at x=%v: %v", x, v)
		}
		if v < prev {
			t.Fatalf("Mtf not monotonic at x=%v: %v < %v", x, v, prev)
		}
		prev = v
	}
}

func TestGHSEndpointsMonotonicAndFiniteAcrossB(t *testing.T) {
	// Exercise the exponential branch (b==0) and both positive and negative
	// local-intensity branches, including a strong negative b that can drive the
	// power base non-positive (guarded against NaN).
	for _, b := range []float64{-3, -1, 0, 0.5, 2, 8} {
		g := NewGHS(2.5, b, 0.2, 0, 1)
		if got := g.Eval(0); math.Abs(got) > 1e-9 {
			t.Fatalf("b=%v: Eval(0) = %v, want 0", b, got)
		}
		if got := g.Eval(1); math.Abs(got-1) > 1e-9 {
			t.Fatalf("b=%v: Eval(1) = %v, want 1", b, got)
		}
		prev := -1.0
		for i := 0; i <= 200; i++ {
			x := float64(i) / 200
			v := g.Eval(x)
			if math.IsNaN(v) || math.IsInf(v, 0) {
				t.Fatalf("b=%v: Eval(%v) = %v, want finite", b, x, v)
			}
			if v < 0 || v > 1 {
				t.Fatalf("b=%v: Eval(%v) = %v, want in [0,1]", b, x, v)
			}
			if v < prev-1e-9 {
				t.Fatalf("b=%v: not monotonic at x=%v: %v < %v", b, x, v, prev)
			}
			prev = v
		}
	}
}

func TestGHSIdentityForNonPositiveD(t *testing.T) {
	g := NewGHS(0, 1, 0.2, 0, 1)
	for _, x := range []float64{0, 0.3, 0.7, 1} {
		if got := g.Eval(x); got != x {
			t.Fatalf("identity Eval(%v) = %v, want %v", x, got, x)
		}
	}
}

func TestApplyUnknownModeReturnsZeroedSliceWithPreservedLength(t *testing.T) {
	input := []float32{0.2, 0.4}
	got := Apply(input, Mode(999))

	if len(got) != len(input) {
		t.Fatalf("len(got) = %d, want %d", len(got), len(input))
	}
	for i := range got {
		if got[i] != 0 {
			t.Fatalf("got[%d] = %v, want 0", i, got[i])
		}
	}
}
