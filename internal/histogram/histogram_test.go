package histogram

import (
	"math"
	"testing"
)

func TestComputeEmptyInput(t *testing.T) {
	got := Compute(nil)
	if got.Count != 0 {
		t.Fatalf("Count = %d, want 0", got.Count)
	}
	if got.Min != 0 || got.Max != 0 || got.Mean != 0 || got.Std != 0 {
		t.Fatalf("unexpected zero-value stats: %+v", got)
	}
}

func TestComputeIgnoresNaNAndInf(t *testing.T) {
	got := Compute([]float32{1, float32(math.NaN()), float32(math.Inf(1)), 3})

	if got.Count != 2 {
		t.Fatalf("Count = %d, want 2", got.Count)
	}
	if got.Min != 1 || got.Max != 3 {
		t.Fatalf("min/max = (%v,%v), want (1,3)", got.Min, got.Max)
	}
	if math.Abs(got.Mean-2) > 1e-9 {
		t.Fatalf("Mean = %v, want 2", got.Mean)
	}
}

func TestComputeCalculatesMomentsAndHistogram(t *testing.T) {
	got := Compute([]float32{1, 2, 3})

	if got.Count != 3 {
		t.Fatalf("Count = %d, want 3", got.Count)
	}
	if got.Min != 1 || got.Max != 3 {
		t.Fatalf("min/max = (%v,%v), want (1,3)", got.Min, got.Max)
	}
	if math.Abs(got.Mean-2) > 1e-9 {
		t.Fatalf("Mean = %v, want 2", got.Mean)
	}
	wantStd := math.Sqrt(2.0 / 3.0)
	if math.Abs(got.Std-wantStd) > 1e-9 {
		t.Fatalf("Std = %v, want %v", got.Std, wantStd)
	}
	if got.Hist[0] != 1 {
		t.Fatalf("Hist[0] = %d, want 1", got.Hist[0])
	}
	if got.Hist[127] != 1 {
		t.Fatalf("Hist[127] = %d, want 1", got.Hist[127])
	}
	if got.Hist[255] != 1 {
		t.Fatalf("Hist[255] = %d, want 1", got.Hist[255])
	}
}

func TestComputeUpdatesMinWhenSmallerValueAppearsLater(t *testing.T) {
	got := Compute([]float32{3, 1, 2})
	if got.Min != 1 {
		t.Fatalf("Min = %v, want 1", got.Min)
	}
	if got.Max != 3 {
		t.Fatalf("Max = %v, want 3", got.Max)
	}
}

func TestComputeAllEqualValuesKeepsZeroRangeHistogram(t *testing.T) {
	got := Compute([]float32{5, 5, 5})
	if got.Count != 3 {
		t.Fatalf("Count = %d, want 3", got.Count)
	}
	if got.Min != 5 || got.Max != 5 {
		t.Fatalf("min/max = (%v,%v), want (5,5)", got.Min, got.Max)
	}
	if got.Mean != 5 {
		t.Fatalf("Mean = %v, want 5", got.Mean)
	}
	if got.Std != 0 {
		t.Fatalf("Std = %v, want 0", got.Std)
	}
	if got.Hist[0] != 3 {
		t.Fatalf("Hist[0] = %d, want 3", got.Hist[0])
	}
}

func TestComputeAllInvalidValuesReturnsZeroStats(t *testing.T) {
	got := Compute([]float32{float32(math.NaN()), float32(math.Inf(-1))})
	if got.Count != 0 {
		t.Fatalf("Count = %d, want 0", got.Count)
	}
	if got.Min != 0 || got.Max != 0 || got.Mean != 0 || got.Std != 0 {
		t.Fatalf("unexpected zero-value stats: %+v", got)
	}
}
