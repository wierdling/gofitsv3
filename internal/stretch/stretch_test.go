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
