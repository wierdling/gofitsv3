package mosaic

import (
	"context"
	"math"
	"testing"

	"gofitsv3/internal/processing"
)

func TestReferenceStarsForTargetSelectsSparseTile(t *testing.T) {
	ref := Input{HDU: makeInput("ref", 10000, 10000, nil, headerWithCRPIX(5000, 5000)).HDU}
	target := Input{HDU: makeInput("target", 100, 100, nil, headerWithCRPIX(50, 50)).HDU}
	mapper, err := newInputMapper(target, ref)
	if err != nil {
		t.Fatal(err)
	}
	full := make([]processing.Star, 0, 2520)
	// Bright stars from the other tiles must not crowd out this target's faint
	// but matching sources.
	for i := 0; i < 2500; i++ {
		full = append(full, processing.Star{X: float64(i % 500), Y: float64(i/500) * 300, Peak: 10000 - float64(i)})
	}
	for i := 0; i < 20; i++ {
		full = append(full, processing.Star{X: 4960 + float64(i%5)*15, Y: 4960 + float64(i/5)*15, Peak: 10})
	}
	local, err := referenceStarsForTarget(target, ref, mapper, full, 1.5)
	if err != nil {
		t.Fatal(err)
	}
	if len(local) != 20 {
		t.Fatalf("local catalog contains %d stars, want all 20 sparse-tile stars", len(local))
	}
	for _, star := range local {
		if star.X < 4800 || star.X > 5200 || star.Y < 4800 || star.Y > 5200 {
			t.Fatalf("selected star outside target footprint: %+v", star)
		}
	}
}

func TestReferenceStarsForTargetCapsUsingLocalFootprint(t *testing.T) {
	ref := Input{HDU: makeInput("ref", 10000, 10000, nil, headerWithCRPIX(5000, 5000)).HDU}
	target := Input{HDU: makeInput("target", 100, 100, nil, headerWithCRPIX(50, 50)).HDU}
	mapper, err := newInputMapper(target, ref)
	if err != nil {
		t.Fatal(err)
	}
	full := make([]processing.Star, 0, 600)
	// The first 500 are bright and tightly clustered. The remaining 100 are
	// fainter but spread across the rest of the local footprint; a cap binned
	// against the 10,000-pixel reference would select only the cluster.
	for i := 0; i < 500; i++ {
		full = append(full, processing.Star{X: 4890 + float64(i%20), Y: 4890 + float64(i/20), Peak: 1000})
	}
	for i := 0; i < 100; i++ {
		full = append(full, processing.Star{X: 4810 + float64(i%10)*40, Y: 4810 + float64(i/10)*40, Peak: 10})
	}
	local, err := referenceStarsForTarget(target, ref, mapper, full, 1.5)
	if err != nil {
		t.Fatal(err)
	}
	if len(local) != processing.TweakRegCatalogMaxStars {
		t.Fatalf("local catalog contains %d stars, want %d", len(local), processing.TweakRegCatalogMaxStars)
	}
	minX, maxX := local[0].X, local[0].X
	minY, maxY := local[0].Y, local[0].Y
	for _, star := range local[1:] {
		minX, maxX = math.Min(minX, star.X), math.Max(maxX, star.X)
		minY, maxY = math.Min(minY, star.Y), math.Max(maxY, star.Y)
	}
	if maxX-minX < 250 || maxY-minY < 250 {
		t.Fatalf("local cap lost spatial coverage: x span %.1f, y span %.1f", maxX-minX, maxY-minY)
	}
}

func TestAlignInputsByStarsExternalReferenceUsesLocalCatalog(t *testing.T) {
	refHeader := headerWithCRPIX(1000, 1000)
	targetHeader := headerWithCRPIX(200, 200)
	local := []processing.Star{{X: 40, Y: 40}, {X: 320, Y: 40}, {X: 40, Y: 320}, {X: 320, Y: 320}, {X: 180, Y: 90}, {X: 90, Y: 180}}
	refLocal := make([]processing.Star, len(local))
	for i, star := range local {
		refLocal[i] = processing.Star{X: star.X + 800, Y: star.Y + 800}
	}
	refStars := append([]processing.Star(nil), refLocal...)
	for i := 0; i < 600; i++ {
		refStars = append(refStars, processing.Star{X: 40 + float64(i%25)*20, Y: 40 + float64(i/25)*20})
	}
	targetStars := make([]processing.Star, len(local))
	for i, star := range local {
		targetStars[i] = processing.Star{X: star.X, Y: star.Y}
	}
	inputs := []Input{
		makeInput("external-ref.fits", 2000, 2000, makeTestStarField(2000, 2000, refStars), refHeader),
		makeInput("target-flc.fits", 400, 400, makeTestStarField(400, 400, targetStars), targetHeader),
	}
	inputs[0].ReferenceOnly = true
	cats, _ := extractStarCatalogsForAlignmentCapsCtx(context.Background(), inputs, []int{0, 2000})
	if len(cats[0]) <= processing.TweakRegCatalogMaxStars || len(cats[1]) != len(targetStars) {
		t.Fatalf("catalog caps not exercised: ref=%d target=%d", len(cats[0]), len(cats[1]))
	}
	m, _ := newInputMapper(inputs[1], inputs[0])
	localRef, _ := referenceStarsForTarget(inputs[1], inputs[0], m, cats[0], 2.0)
	if len(localRef) != len(local) {
		t.Fatalf("local reference catalog has %d stars, want %d", len(localRef), len(local))
	}
	var diag processing.AlignmentDiag
	var gotDiag bool
	previousHook := processing.AlignmentDebugHook
	processing.AlignmentDebugHook = func(candidate processing.AlignmentDiag) {
		diag = candidate
		gotDiag = true
	}
	defer func() { processing.AlignmentDebugHook = previousHook }()
	results, err := AlignInputsByStarsWithMode(inputs, 1, AlignmentModeTweakRegRScale, 2.0)
	if err != nil {
		t.Fatalf("external-reference alignment failed: %v", err)
	}
	if !results[1].Applied || !results[1].HasManualTransform {
		t.Fatalf("target was not aligned: %+v", results[1])
	}
	if !gotDiag {
		t.Fatal("alignment debug hook was not called")
	}
	if len(diag.RefStars) != len(local) {
		t.Fatalf("solver received %d reference stars, want local subset of %d", len(diag.RefStars), len(local))
	}
	for _, star := range diag.RefStars {
		if star.X < 800 || star.Y < 800 {
			t.Fatalf("solver received off-footprint distractor: %+v", star)
		}
	}
	for i, star := range targetStars {
		x, y := processing.ApplyAffineTransform(results[1].ManualTransform, star.X, star.Y)
		if math.Hypot(x-local[i].X, y-local[i].Y) > 3 {
			t.Fatalf("star %d mapped to (%.2f, %.2f), want (%.2f, %.2f)", i, x, y, local[i].X, local[i].Y)
		}
	}
}

func TestReferenceStarsForTargetRejectsNonfiniteMapping(t *testing.T) {
	ref := Input{HDU: makeInput("ref", 10, 10, nil, headerWithCRPIX(5, 5)).HDU}
	target := Input{HDU: makeInput("target", 10, 10, nil, headerWithCRPIX(5, 5)).HDU}
	mapper, err := processing.NewNativeGWCSMapper(nanGWCS{}, ref.HDU.Header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := referenceStarsForTarget(target, ref, mapper, nil, 1); err == nil {
		t.Fatal("expected nonfinite footprint mapping error")
	}
}

type nanGWCS struct{}

func (nanGWCS) PixelToICRS(float64, float64) (float64, float64, error) {
	return math.NaN(), 0, nil
}
