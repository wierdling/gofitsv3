package ui

import "testing"

func TestClearComposeOrigPixels(t *testing.T) {
	orig := [3][]float32{
		{1},
		{2},
		{3},
	}

	clearComposeOrigPixels(&orig, -1, 0, 2, 3)

	if orig[0] != nil {
		t.Fatalf("orig[0] = %#v, want nil", orig[0])
	}
	if orig[1] == nil || len(orig[1]) != 1 || orig[1][0] != 2 {
		t.Fatalf("orig[1] = %#v, want preserved slice", orig[1])
	}
	if orig[2] != nil {
		t.Fatalf("orig[2] = %#v, want nil", orig[2])
	}
}
