package mosaic

import (
	"reflect"
	"testing"

	"gofitsv3/internal/processing"
)

func consensusStar(x, y, flux float64) processing.Star {
	return processing.Star{X: x, Y: y, Flux: flux, Peak: flux, Area: 3}
}

func projectedDetection(index, starIndex int, x, y float64) projectedCatalogDetection {
	return projectedCatalogDetection{inputIndex: index, starIndex: starIndex, x: x, y: y}
}

func hasStarAt(stars []processing.Star, x, y float64) bool {
	for _, star := range stars {
		if star.X == x && star.Y == y {
			return true
		}
	}
	return false
}

func TestSelectCrossFrameConsensusCatalogsPrefersRecurringFaintSources(t *testing.T) {
	candidates := [][]processing.Star{
		{consensusStar(10, 10, 1000), consensusStar(20, 20, 900), consensusStar(30, 30, 800), consensusStar(40, 40, 700), consensusStar(50, 50, 10)},
		{consensusStar(10.5, 10.5, 8), consensusStar(20.5, 20.5, 7), consensusStar(30.5, 30.5, 6), consensusStar(40.5, 40.5, 5), consensusStar(70, 70, 900)},
		{consensusStar(9.5, 9.5, 7), consensusStar(19.5, 19.5, 6), consensusStar(29.5, 29.5, 5), consensusStar(39.5, 39.5, 4), consensusStar(80, 80, 800)},
		{consensusStar(10.2, 10.2, 6), consensusStar(20.2, 20.2, 5), consensusStar(30.2, 30.2, 4), consensusStar(40.2, 40.2, 3), consensusStar(90, 90, 700)},
	}
	projected := [][]projectedCatalogDetection{
		{projectedDetection(0, 0, 10, 10), projectedDetection(0, 1, 20, 20), projectedDetection(0, 2, 30, 30), projectedDetection(0, 3, 40, 40), projectedDetection(0, 4, 50, 50)},
		{projectedDetection(1, 0, 10.5, 10.5), projectedDetection(1, 1, 20.5, 20.5), projectedDetection(1, 2, 30.5, 30.5), projectedDetection(1, 3, 40.5, 40.5), projectedDetection(1, 4, 70, 70)},
		{projectedDetection(2, 0, 9.5, 9.5), projectedDetection(2, 1, 19.5, 19.5), projectedDetection(2, 2, 29.5, 29.5), projectedDetection(2, 3, 39.5, 39.5), projectedDetection(2, 4, 80, 80)},
		{projectedDetection(3, 0, 10.2, 10.2), projectedDetection(3, 1, 20.2, 20.2), projectedDetection(3, 2, 30.2, 30.2), projectedDetection(3, 3, 40.2, 40.2), projectedDetection(3, 4, 90, 90)},
	}

	selected, stats := selectCrossFrameConsensusCatalogs(candidates, projected, []string{"a", "b", "c", "d"}, 500, 4)
	if len(selected) != len(candidates) || !hasStarAt(selected[0], 10, 10) {
		t.Fatalf("expected recurring source to remain selected: %#v", selected)
	}
	if hasStarAt(selected[0], 50, 50) {
		t.Fatal("expected bright one-off source to be removed")
	}
	if stats[0].Corroborated < 1 || stats[0].Selected == 0 {
		t.Fatalf("expected corroboration and selection stats, got %#v", stats[0])
	}
}

func TestSelectCrossFrameConsensusCatalogsRequiresThreeBeforeTwo(t *testing.T) {
	candidates := [][]processing.Star{{consensusStar(10, 10, 10), consensusStar(20, 20, 9)}, {consensusStar(10, 10, 8), consensusStar(20, 20, 7)}, {consensusStar(10, 10, 6)}, {consensusStar(30, 30, 5)}}
	projected := [][]projectedCatalogDetection{{projectedDetection(0, 0, 10, 10), projectedDetection(0, 1, 20, 20)}, {projectedDetection(1, 0, 10, 10), projectedDetection(1, 1, 20, 20)}, {projectedDetection(2, 0, 10, 10)}, {projectedDetection(3, 0, 30, 30)}}
	for frame := 0; frame < 3; frame++ {
		for anchor := 0; anchor < 4; anchor++ {
			index := len(candidates[frame])
			x := float64(100 + anchor*10)
			candidates[frame] = append(candidates[frame], consensusStar(x, 100, float64(anchor+1)))
			projected[frame] = append(projected[frame], projectedDetection(frame, index, x, 100))
		}
	}
	selected, stats := selectCrossFrameConsensusCatalogs(candidates, projected, []string{"a", "b", "c", "d"}, 1, 4)
	if stats[0].Fallback || len(selected[0]) != 1 || !hasStarAt(selected[0], 10, 10) {
		t.Fatalf("support-3 source should win the one-result cap: %#v", selected[0])
	}
}

func TestSelectCrossFrameConsensusCatalogsRescuesBelowBrightRankAndCaps(t *testing.T) {
	const n = 2000
	candidates := make([][]processing.Star, 4)
	projected := make([][]projectedCatalogDetection, 4)
	for frame := range candidates {
		candidates[frame] = make([]processing.Star, n)
		projected[frame] = make([]projectedCatalogDetection, n)
		for i := 0; i < n; i++ {
			x := float64(i * 10)
			candidates[frame][i] = consensusStar(x, 0, float64(n-i))
			projected[frame][i] = projectedDetection(frame, i, x+float64(frame*100000), 0)
		}
	}
	// The recurring source is deliberately below the first 500 brightest entries;
	// it has three distinct-exposure votes while rank-eligible competitors have two.
	for frame := 0; frame < 3; frame++ {
		projected[frame][1500].x = 15000
	}
	projected[3][1500].x = 19999
	for i := 0; i < 500; i++ {
		projected[1][i].x = float64(i * 10)
	}
	selected, stats := selectCrossFrameConsensusCatalogs(candidates, projected, []string{"a", "b", "c", "d"}, 500, 4)
	if len(selected[0]) != 500 || len(selected[1]) != 500 {
		t.Fatalf("expected 500-result cap, got %d and %d", len(selected[0]), len(selected[1]))
	}
	if !hasStarAt(selected[0], 15000, 0) {
		t.Fatal("expected recurring below-rank-500 source to be rescued")
	}
	if stats[0].Selected != 500 {
		t.Fatalf("expected selected stat to honor cap, got %#v", stats[0])
	}
}

func TestSelectCrossFrameConsensusCatalogsCountsDistinctExposuresOnly(t *testing.T) {
	candidates := make([][]processing.Star, 5)
	projected := make([][]projectedCatalogDetection, 5)
	for i := range candidates {
		candidates[i] = []processing.Star{consensusStar(10, 10, 10), consensusStar(20, 20, 9)}
		projected[i] = []projectedCatalogDetection{projectedDetection(i, 0, 10, 10), projectedDetection(i, 1, 20, 20)}
		for anchor := 0; anchor < 4; anchor++ {
			index := len(candidates[i])
			x := float64(200 + anchor*10)
			candidates[i] = append(candidates[i], consensusStar(x, 200, float64(anchor+1)))
			projected[i] = append(projected[i], projectedDetection(i, index, x, 200))
		}
	}
	projected[2][0].x, projected[2][0].y = 100, 100
	projected[3][0].x, projected[3][0].y = 100, 100
	projected[4][0].x, projected[4][0].y = 100, 100
	selected, stats := selectCrossFrameConsensusCatalogs(candidates, projected, []string{"exp", "exp", "other", "other2", "other3"}, 500, 4)
	if stats[0].Fallback || hasStarAt(selected[0], 10, 10) || !hasStarAt(selected[0], 20, 20) {
		t.Fatalf("same-file siblings must not add support: selected=%#v stats=%#v", selected[0], stats[0])
	}
}

func TestSelectCrossFrameConsensusCatalogsUsesInclusiveFourPixelRadius(t *testing.T) {
	candidates := [][]processing.Star{{consensusStar(0, 0, 1)}, {consensusStar(4, 0, 1)}, {consensusStar(0, 0, 1)}, {consensusStar(0, 0, 1)}}
	projected := [][]projectedCatalogDetection{{projectedDetection(0, 0, 0, 0)}, {projectedDetection(1, 0, 4, 0)}, {projectedDetection(2, 0, 0, 0)}, {projectedDetection(3, 0, 0, 0)}}
	for frame := range candidates {
		for anchor := 0; anchor < 4; anchor++ {
			index := len(candidates[frame])
			x := float64(100 + anchor*10)
			candidates[frame] = append(candidates[frame], consensusStar(x, 100, 1))
			projected[frame] = append(projected[frame], projectedDetection(frame, index, x, 100))
		}
	}
	selected, stats := selectCrossFrameConsensusCatalogs(candidates, projected, []string{"a", "b", "c", "d"}, 500, 4)
	if stats[1].Fallback || !hasStarAt(selected[1], 4, 0) {
		t.Fatal("expected exactly-four-pixel separation to corroborate inclusively")
	}
}

func TestSelectCrossFrameConsensusCatalogsExcludesJustOutsideFourPixels(t *testing.T) {
	candidates := make([][]processing.Star, 5)
	projected := make([][]projectedCatalogDetection, 5)
	for i := range candidates {
		candidates[i] = []processing.Star{consensusStar(0, 0, 1)}
		x := 0.0
		if i == 1 {
			x = 4.01
		}
		projected[i] = []projectedCatalogDetection{projectedDetection(i, 0, x, 0)}
		for anchor := 0; anchor < 4; anchor++ {
			index := len(candidates[i])
			ax := float64(100 + anchor*10)
			candidates[i] = append(candidates[i], consensusStar(ax, 100, 1))
			projected[i] = append(projected[i], projectedDetection(i, index, ax, 100))
		}
	}
	selected, stats := selectCrossFrameConsensusCatalogs(candidates, projected, []string{"a", "b", "c", "d", "e"}, 500, 4)
	if stats[1].Fallback || hasStarAt(selected[1], 4.01, 0) {
		t.Fatalf("just-outside source should be excluded without fallback: selected=%#v stats=%#v", selected[1], stats[1])
	}
}

func TestSelectCrossFrameConsensusCatalogsThreeSupportedFallsBackButFourFilters(t *testing.T) {
	makeCase := func(frames int) ([][]processing.Star, [][]projectedCatalogDetection, []string) {
		candidates := make([][]processing.Star, frames)
		projected := make([][]projectedCatalogDetection, frames)
		exposures := make([]string, frames)
		for i := range candidates {
			candidates[i] = []processing.Star{consensusStar(10, 10, 1), consensusStar(float64(100+10*i), 100, 100)}
			projected[i] = []projectedCatalogDetection{projectedDetection(i, 0, 10, 10), projectedDetection(i, 1, float64(100+10*i), 100)}
			if frames < 4 {
				exposures[i] = string(rune('a' + i))
				continue
			}
			for anchor := 0; anchor < 4; anchor++ {
				index := len(candidates[i])
				x := float64(200 + anchor*10)
				candidates[i] = append(candidates[i], consensusStar(x, 200, 1))
				projected[i] = append(projected[i], projectedDetection(i, index, x, 200))
			}
			exposures[i] = string(rune('a' + i))
		}
		return candidates, projected, exposures
	}
	threeCandidates, threeProjected, threeExposures := makeCase(3)
	for i := range threeCandidates {
		for n := 2; n < 600; n++ {
			threeCandidates[i] = append(threeCandidates[i], consensusStar(float64(10000+10*n+10000*i), 100, float64(n)))
			threeProjected[i] = append(threeProjected[i], projectedDetection(i, n, float64(10000+10*n+10000*i), 100))
		}
	}
	threeSelected, threeStats := selectCrossFrameConsensusCatalogs(threeCandidates, threeProjected, threeExposures, 500, 4)
	for i := range threeSelected {
		if !threeStats[i].Fallback || !reflect.DeepEqual(threeSelected[i], threeCandidates[i][:500]) {
			t.Fatalf("exactly three supported detections should preserve every original catalog: selected=%#v stats=%#v", threeSelected[i], threeStats[i])
		}
	}
	if !hasStarAt(threeSelected[0], 100, 100) {
		t.Fatal("exactly three supported detections should use the original-catalog fallback")
	}
	fourCandidates, fourProjected, fourExposures := makeCase(4)
	fourSelected, fourStats := selectCrossFrameConsensusCatalogs(fourCandidates, fourProjected, fourExposures, 500, 4)
	if fourStats[0].Fallback || hasStarAt(fourSelected[0], 100, 100) {
		t.Fatal("exactly four supported detections should filter the one-off")
	}
}

func TestSelectCrossFrameConsensusCatalogsFallsBackForSparseSupport(t *testing.T) {
	candidates := make([][]processing.Star, 3)
	projected := make([][]projectedCatalogDetection, 3)
	for i := range candidates {
		candidates[i] = []processing.Star{consensusStar(1, 1, 3), consensusStar(2, 2, 2)}
		projected[i] = []projectedCatalogDetection{projectedDetection(i, 0, 1, 1), projectedDetection(i, 1, 2, 2)}
		for n := 2; n < 600; n++ {
			candidates[i] = append(candidates[i], consensusStar(float64(10000+10*n+10000*i), 100, float64(n)))
			projected[i] = append(projected[i], projectedDetection(i, n, float64(10000+10*n+10000*i), 100))
		}
	}
	selected, stats := selectCrossFrameConsensusCatalogs(candidates, projected, []string{"a", "b"}, 500, 4)
	for i := range selected {
		if !stats[i].Fallback || !reflect.DeepEqual(selected[i], candidates[i][:500]) {
			t.Fatalf("expected original catalog fallback for every input, selected=%#v stats=%#v", selected[i], stats[i])
		}
	}
	if len(selected[0]) != 500 || selected[0][0].Flux != candidates[0][0].Flux {
		t.Fatalf("expected original catalog fallback, selected=%#v stats=%#v", selected[0], stats[0])
	}
}

func TestSelectCrossFrameConsensusCatalogsFallsBackWhenProjectionUnavailable(t *testing.T) {
	candidates := make([][]processing.Star, 3)
	for frame := range candidates {
		candidates[frame] = make([]processing.Star, 600)
		for i := range candidates[frame] {
			candidates[frame][i] = consensusStar(float64(i), float64(frame), float64(i))
		}
	}
	projections := make([][]projectedCatalogDetection, len(candidates))
	selected, stats := selectCrossFrameConsensusCatalogs(candidates, projections, []string{"a", "b", "c"}, 500, 4)
	for i := range selected {
		if !stats[i].Fallback || !reflect.DeepEqual(selected[i], candidates[i][:500]) {
			t.Fatalf("expected first 500 unmappable stars for frame %d, selected=%d stats=%#v", i, len(selected[i]), stats[i])
		}
	}
}

func TestSelectCrossFrameConsensusCatalogsIsRepeatable(t *testing.T) {
	candidates := [][]processing.Star{{consensusStar(1, 1, 3), consensusStar(2, 2, 2), consensusStar(3, 3, 1), consensusStar(4, 4, 1)}, {consensusStar(1.1, 1.1, 1), consensusStar(2.1, 2.1, 1), consensusStar(3.1, 3.1, 1), consensusStar(4.1, 4.1, 1)}, {consensusStar(1.2, 1.2, 1), consensusStar(2.2, 2.2, 1), consensusStar(3.2, 3.2, 1), consensusStar(4.2, 4.2, 1)}, {consensusStar(1.3, 1.3, 1), consensusStar(2.3, 2.3, 1), consensusStar(3.3, 3.3, 1), consensusStar(4.3, 4.3, 1)}}
	projected := [][]projectedCatalogDetection{{projectedDetection(0, 0, 1, 1), projectedDetection(0, 1, 2, 2), projectedDetection(0, 2, 3, 3), projectedDetection(0, 3, 4, 4)}, {projectedDetection(1, 0, 1.1, 1.1), projectedDetection(1, 1, 2.1, 2.1), projectedDetection(1, 2, 3.1, 3.1), projectedDetection(1, 3, 4.1, 4.1)}, {projectedDetection(2, 0, 1.2, 1.2), projectedDetection(2, 1, 2.2, 2.2), projectedDetection(2, 2, 3.2, 3.2), projectedDetection(2, 3, 4.2, 4.2)}, {projectedDetection(3, 0, 1.3, 1.3), projectedDetection(3, 1, 2.3, 2.3), projectedDetection(3, 2, 3.3, 3.3), projectedDetection(3, 3, 4.3, 4.3)}}
	exposures := []string{"a", "b", "c", "d"}
	first, firstStats := selectCrossFrameConsensusCatalogs(candidates, projected, exposures, 500, 4)
	second, secondStats := selectCrossFrameConsensusCatalogs(candidates, projected, exposures, 500, 4)
	if len(first) != len(second) || len(firstStats) != len(secondStats) {
		t.Fatalf("selector result shape changed between runs")
	}
	for frame := range first {
		if firstStats[frame].Fallback || secondStats[frame].Fallback {
			t.Fatalf("determinism fixture unexpectedly used fallback at frame %d", frame)
		}
		if firstStats[frame] != secondStats[frame] || len(first[frame]) != len(second[frame]) {
			t.Fatalf("selector stats/order changed between runs: %#v/%#v and %#v/%#v", first[frame], firstStats[frame], second[frame], secondStats[frame])
		}
		for i := range first[frame] {
			if first[frame][i] != second[frame][i] {
				t.Fatalf("selector order changed at frame %d index %d", frame, i)
			}
		}
	}
}
