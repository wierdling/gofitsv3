package mosaic

import (
	"math/rand"
	"sort"
	"testing"
)

// TestQuickSelectMedianMatchesSort guards that the O(n) quickselect median used
// by estimateSkyFromValues returns the exact same value as a full sort, across
// odd/even lengths and duplicate-heavy inputs (typical of quantised sky data).
func TestQuickSelectMedianMatchesSort(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for trial := 0; trial < 2000; trial++ {
		n := 1 + rng.Intn(200)
		v := make([]float64, n)
		for i := range v {
			switch rng.Intn(3) {
			case 0:
				v[i] = float64(rng.Intn(5)) // duplicate-heavy
			case 1:
				v[i] = rng.NormFloat64() * 1000
			default:
				v[i] = float64(rng.Intn(1000))
			}
		}
		ref := append([]float64(nil), v...)
		sort.Float64s(ref)
		want := medianFloat64(ref)
		got := quickSelectMedian(append([]float64(nil), v...))
		if got != want {
			t.Fatalf("n=%d got=%v want=%v", n, got, want)
		}
	}
	if quickSelectMedian([]float64{}) != 0 {
		t.Fatal("empty slice should return 0")
	}
	if quickSelectMedian([]float64{7}) != 7 {
		t.Fatal("single element")
	}
	if quickSelectMedian([]float64{5, 5}) != 5 {
		t.Fatal("all-equal even length")
	}
}
