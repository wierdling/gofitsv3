package processing

import "testing"

func TestShouldUpgradeTweakRegFitForFieldShear(t *testing.T) {
	pairs := []MatchedPair{
		{RefX: 0, RefY: 0, TargetX: 2, TargetY: -3},
		{RefX: 100, RefY: 0, TargetX: 104, TargetY: -2},
		{RefX: 0, RefY: 100, TargetX: 1, TargetY: 99},
		{RefX: 100, RefY: 100, TargetX: 103, TargetY: 100},
		{RefX: 50, RefY: 200, TargetX: 55, TargetY: 197},
		{RefX: 180, RefY: 220, TargetX: 191.2, TargetY: 199.6},
		{RefX: 240, RefY: 40, TargetX: 248.8, TargetY: 20.2},
		{RefX: 260, RefY: 260, TargetX: 276.8, TargetY: 234.6},
	}
	rscale, err := solveRScaleLeastSquares(pairs)
	if err != nil {
		t.Fatalf("solveRScaleLeastSquares returned error: %v", err)
	}
	general, err := solveLeastSquares(pairs)
	if err != nil {
		t.Fatalf("solveLeastSquares returned error: %v", err)
	}
	if !shouldUpgradeTweakRegFit(pairs, rscale, general, 300, 300) {
		t.Fatal("expected field-dependent distortion to upgrade from rscale to general")
	}
}

func TestShouldUpgradeTweakRegFitKeepsGoodRScale(t *testing.T) {
	pairs := []MatchedPair{
		{RefX: 0, RefY: 0, TargetX: 5, TargetY: -4},
		{RefX: 100, RefY: 0, TargetX: 115, TargetY: -4},
		{RefX: 0, RefY: 100, TargetX: 5, TargetY: 106},
		{RefX: 100, RefY: 100, TargetX: 115, TargetY: 106},
		{RefX: 50, RefY: 180, TargetX: 60, TargetY: 194},
		{RefX: 220, RefY: 140, TargetX: 247, TargetY: 150},
	}
	rscale, err := solveRScaleLeastSquares(pairs)
	if err != nil {
		t.Fatalf("solveRScaleLeastSquares returned error: %v", err)
	}
	general, err := solveLeastSquares(pairs)
	if err != nil {
		t.Fatalf("solveLeastSquares returned error: %v", err)
	}
	if shouldUpgradeTweakRegFit(pairs, rscale, general, 300, 300) {
		t.Fatal("expected good rscale fit to remain rscale")
	}
}
