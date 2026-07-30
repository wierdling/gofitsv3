package processing

import (
	"math"
	"testing"

	"gofitsv3/internal/catalog/gaia"
)

func TestRefinedPixelToSkyUsesResidualInverse(t *testing.T) {
	base := func(x, y float64) (gaia.Coordinate, error) { return gaia.Coordinate{RA: x, Dec: y}, nil }
	residual := AffineTransform{A: 1, E: 1, C: 4, F: -3}
	refined, err := RefinedPixelToSky(base, residual)
	if err != nil {
		t.Fatal(err)
	}
	got, err := refined(14, 7)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(got.RA-10) > 1e-9 || math.Abs(got.Dec-10) > 1e-9 {
		t.Fatalf("got %+v, want nominal (10,10)", got)
	}
}

func TestProjectGaiaSourcesPropagatesEpoch(t *testing.T) {
	sources := []gaia.Source{{SourceID: 1<<54 + 1, RA: 10, Dec: 20, ReferenceEpoch: 2000, ProperMotionRA: 3600, ProperMotionDec: 0}}
	stars, err := ProjectGaiaSources(sources, 2001, func(c gaia.Coordinate) (float64, float64, error) { return c.RA, c.Dec, nil })
	if err != nil {
		t.Fatal(err)
	}
	expected := PropagateGaiaPosition(sources[0], 2001)
	if len(stars) != 1 || stars[0].SourceID != sources[0].SourceID || math.Abs(stars[0].Star.X-expected.RA) > 1e-9 {
		t.Fatalf("stars=%+v", stars)
	}
}
