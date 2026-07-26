package mosaic

import (
	"math"
	"reflect"
	"testing"
)

func TestEqualizeDisconnectedSkyComponentsPlaneMeetsDarkestWithoutChangingRelativePlanes(t *testing.T) {
	planned := []plannedInput{
		planeTestInput("dark-a_cal.fits", 1024, 1024),
		planeTestInput("dark-b_cal.fits", 1024, 1024),
		planeTestInput("bright-a_cal.fits", 1024, 1024),
		planeTestInput("bright-b_cal.fits", 1024, 1024),
	}
	maps := []map[int64]float64{{}, {}, {}, {}}
	darkDifference := skyPlane{A: 0.002, B: -0.001, C: 3, Valid: true}
	brightDifference := skyPlane{A: -0.0015, B: 0.0025, C: -2, Valid: true}
	addComponentCells(maps[0], maps[1], 64, 64, 10, darkDifference)
	addComponentCells(maps[2], maps[3], 640, 640, 30, brightDifference)

	options := SkysubOptions{
		Method:                          SkyMethodMatchPlane,
		Clip:                            2,
		LSigma:                          4,
		USigma:                          4,
		EqualizeDisconnectedBackgrounds: true,
	}
	planes, matched, components := computeDifferenceSkyPlanesWithComponents(planned, maps, options)
	if !reflect.DeepEqual(matched, []bool{true, true, true, true}) {
		t.Fatalf("matched = %v, want all data frames matched", matched)
	}
	if !reflect.DeepEqual(components, [][]int{{0, 1}, {2, 3}}) {
		t.Fatalf("components = %v, want two disconnected pairs", components)
	}

	before := append([]skyPlane(nil), planes...)
	rawSky := []float64{10, 13, 30, 28}
	subtractSky := make([]float64, len(planned))
	darkBefore := componentCorrectedBaseline(t, components[0], maps, rawSky, subtractSky, planes)
	brightBefore := componentCorrectedBaseline(t, components[1], maps, rawSky, subtractSky, planes)
	if brightBefore <= darkBefore {
		t.Fatalf("test setup backgrounds = (%v, %v), want second component brighter", darkBefore, brightBefore)
	}

	equalizeDisconnectedSkyComponents(planned, maps, rawSky, subtractSky, planes, components, options)

	darkAfter := componentCorrectedBaseline(t, components[0], maps, rawSky, subtractSky, planes)
	brightAfter := componentCorrectedBaseline(t, components[1], maps, rawSky, subtractSky, planes)
	if math.Abs(darkAfter-brightAfter) > 1e-9 {
		t.Fatalf("equalized component backgrounds = (%v, %v), want equal", darkAfter, brightAfter)
	}
	for i := range planes {
		if planes[i].A != before[i].A || planes[i].B != before[i].B {
			t.Fatalf("planes[%d] slope changed from %+v to %+v", i, before[i], planes[i])
		}
	}
	if got, want := planes[1].C-planes[0].C, before[1].C-before[0].C; math.Abs(got-want) > 1e-12 {
		t.Fatalf("dark within-component C difference = %v, want %v", got, want)
	}
	if got, want := planes[3].C-planes[2].C, before[3].C-before[2].C; math.Abs(got-want) > 1e-12 {
		t.Fatalf("bright within-component C difference = %v, want %v", got, want)
	}
}

func TestEqualizeDisconnectedSkyComponentsConnectedGraphIsExactNoOp(t *testing.T) {
	planned := []plannedInput{planeTestInput("a.fits", 64, 64), planeTestInput("b.fits", 64, 64)}
	maps := []map[int64]float64{{overlapCellKey(16, 16): 10}, {overlapCellKey(16, 16): 13}}
	rawSky := []float64{10, 13}
	subtractSky := []float64{0, 3}
	planes := []skyPlane{{}, {}}
	wantOffsets := append([]float64(nil), subtractSky...)
	wantPlanes := append([]skyPlane(nil), planes...)

	equalizeDisconnectedSkyComponents(planned, maps, rawSky, subtractSky, planes, [][]int{{0, 1}}, SkysubOptions{
		Method:                          SkyMethodMatch,
		EqualizeDisconnectedBackgrounds: true,
	})

	if !reflect.DeepEqual(subtractSky, wantOffsets) || !reflect.DeepEqual(planes, wantPlanes) {
		t.Fatalf("connected graph changed: offsets=%v planes=%+v", subtractSky, planes)
	}
}

func TestEqualizeDisconnectedSkyComponentsDisabledIsExactNoOp(t *testing.T) {
	planned := []plannedInput{planeTestInput("a.fits", 64, 64), planeTestInput("b.fits", 64, 64)}
	maps := []map[int64]float64{{overlapCellKey(16, 16): 10}, {overlapCellKey(160, 160): 30}}
	rawSky := []float64{10, 30}
	subtractSky := []float64{1, 2}
	planes := []skyPlane{{A: 0.1, B: 0.2, C: 3, Valid: true}, {A: -0.1, B: -0.2, C: 4, Valid: true}}
	wantOffsets := append([]float64(nil), subtractSky...)
	wantPlanes := append([]skyPlane(nil), planes...)

	equalizeDisconnectedSkyComponents(planned, maps, rawSky, subtractSky, planes, [][]int{{0}, {1}}, SkysubOptions{
		Method: SkyMethodMatchPlane,
	})

	if !reflect.DeepEqual(subtractSky, wantOffsets) || !reflect.DeepEqual(planes, wantPlanes) {
		t.Fatalf("disabled option changed behavior: offsets=%v planes=%+v", subtractSky, planes)
	}
}

func TestEqualizeDisconnectedSkyComponentsScalarMethodsUseCorrectedBaselines(t *testing.T) {
	planned := []plannedInput{
		planeTestInput("dark-a.fits", 64, 64),
		planeTestInput("dark-b.fits", 64, 64),
		planeTestInput("bright-a.fits", 64, 64),
		planeTestInput("bright-b.fits", 64, 64),
	}
	key := overlapCellKey(16, 16)
	components := [][]int{{0, 1}, {2, 3}}
	tests := []struct {
		name       string
		method     SkyMethod
		maps       []map[int64]float64
		rawSky     []float64
		offsets    []float64
		wantOffset []float64
	}{
		{
			name:       "match shifts only the brighter component",
			method:     SkyMethodMatch,
			maps:       []map[int64]float64{{key: 10}, {key: 12}, {key: 30}, {key: 33}},
			rawSky:     []float64{10, 12, 30, 33},
			offsets:    []float64{0, 2, 0, 3},
			wantOffset: []float64{0, 2, 20, 23},
		},
		{
			name:       "globalmin match compares already corrected backgrounds",
			method:     SkyMethodGlobalMinMatch,
			maps:       []map[int64]float64{{key: 10}, {key: 12}, {key: 25}, {key: 28}},
			rawSky:     []float64{10, 12, 25, 28},
			offsets:    []float64{5, 7, 5, 8},
			wantOffset: []float64{5, 7, 20, 23},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			offsets := append([]float64(nil), tc.offsets...)
			planes := make([]skyPlane, len(planned))
			equalizeDisconnectedSkyComponents(planned, tc.maps, tc.rawSky, offsets, planes, components, SkysubOptions{
				Method:                          tc.method,
				EqualizeDisconnectedBackgrounds: true,
			})
			if !reflect.DeepEqual(offsets, tc.wantOffset) {
				t.Fatalf("offsets = %v, want %v", offsets, tc.wantOffset)
			}
			if !reflect.DeepEqual(planes, make([]skyPlane, len(planned))) {
				t.Fatalf("scalar method changed planes: %+v", planes)
			}
		})
	}
}

func TestEqualizeDisconnectedSkyComponentsUsesRawSkyFallbackForUnmatchedSingleton(t *testing.T) {
	planned := []plannedInput{planeTestInput("mapped.fits", 64, 64), planeTestInput("singleton.fits", 64, 64)}
	key := overlapCellKey(16, 16)
	maps := []map[int64]float64{{key: 10}, nil}
	offsets := []float64{0, 30}

	equalizeDisconnectedSkyComponents(planned, maps, []float64{10, 30}, offsets, make([]skyPlane, 2), [][]int{{0}, {1}}, SkysubOptions{
		Method:                          SkyMethodMatch,
		EqualizeDisconnectedBackgrounds: true,
	})

	if want := []float64{10, 30}; !reflect.DeepEqual(offsets, want) {
		t.Fatalf("offsets = %v, want %v; singleton's raw-sky subtraction should remain unchanged", offsets, want)
	}
}

func TestEqualizeDisconnectedSkyComponentsPlaneSingletonAddsOnlyConstant(t *testing.T) {
	planned := []plannedInput{planeTestInput("dark.fits", 64, 64), planeTestInput("bright-singleton.fits", 64, 64)}
	key := overlapCellKey(16, 16)
	maps := []map[int64]float64{{key: 10}, {key: 40}}
	offsets := []float64{10, 30}
	planes := make([]skyPlane, 2)

	equalizeDisconnectedSkyComponents(planned, maps, []float64{10, 30}, offsets, planes, [][]int{{0}, {1}}, SkysubOptions{
		Method:                          SkyMethodMatchPlane,
		EqualizeDisconnectedBackgrounds: true,
	})

	if !reflect.DeepEqual(offsets, []float64{10, 30}) {
		t.Fatalf("plane equalization changed scalar offsets: %v", offsets)
	}
	if planes[1] != (skyPlane{C: 10, Valid: true}) {
		t.Fatalf("bright singleton plane = %+v, want constant C=10 with zero A/B", planes[1])
	}
	if planes[0] != (skyPlane{}) {
		t.Fatalf("dark singleton plane = %+v, want unchanged", planes[0])
	}
}

func TestEqualizeDisconnectedSkyComponentsLeavesReferenceOnlyInputUntouched(t *testing.T) {
	planned := []plannedInput{
		planeTestInput("dark.fits", 64, 64),
		planeTestInput("bright.fits", 64, 64),
		planeTestInput("reference.fits", 64, 64),
	}
	planned[2].input.ReferenceOnly = true
	key := overlapCellKey(16, 16)
	maps := []map[int64]float64{{key: 10}, {key: 30}, {key: 100}}
	offsets := []float64{0, 0, 7}
	planes := []skyPlane{{}, {}, {A: 1, B: 2, C: 3, Valid: true}}
	components := activeSkyComponents(planned, nil)

	equalizeDisconnectedSkyComponents(planned, maps, []float64{10, 30, 100}, offsets, planes, components, SkysubOptions{
		Method:                          SkyMethodMatch,
		EqualizeDisconnectedBackgrounds: true,
	})

	if !reflect.DeepEqual(offsets, []float64{0, 20, 7}) {
		t.Fatalf("offsets = %v, want data components equalized and reference unchanged", offsets)
	}
	if planes[2] != (skyPlane{A: 1, B: 2, C: 3, Valid: true}) {
		t.Fatalf("reference plane changed: %+v", planes[2])
	}
}

func TestEqualizeDisconnectedSkyComponentsUnsupportedMethodsAreExactNoOp(t *testing.T) {
	for _, method := range []SkyMethod{SkyMethodLocalMin, SkyMethodGlobalMin} {
		t.Run(skyMethodTestName(method), func(t *testing.T) {
			planned := []plannedInput{planeTestInput("a.fits", 64, 64), planeTestInput("b.fits", 64, 64)}
			key := overlapCellKey(16, 16)
			maps := []map[int64]float64{{key: 10}, {key: 30}}
			offsets := []float64{1, 2}
			planes := []skyPlane{{A: 0.1, C: 3, Valid: true}, {B: 0.2, C: 4, Valid: true}}
			wantOffsets := append([]float64(nil), offsets...)
			wantPlanes := append([]skyPlane(nil), planes...)

			equalizeDisconnectedSkyComponents(planned, maps, []float64{10, 30}, offsets, planes, [][]int{{0}, {1}}, SkysubOptions{
				Method:                          method,
				EqualizeDisconnectedBackgrounds: true,
			})

			if !reflect.DeepEqual(offsets, wantOffsets) || !reflect.DeepEqual(planes, wantPlanes) {
				t.Fatalf("unsupported method changed offsets/planes: %v %+v", offsets, planes)
			}
		})
	}
}

func skyMethodTestName(method SkyMethod) string {
	if method == SkyMethodGlobalMin {
		return "globalmin"
	}
	return "localmin"
}

func TestActiveSkyComponentsIncludesSingletonDataAndSkipsReference(t *testing.T) {
	planned := []plannedInput{
		planeTestInput("a.fits", 64, 64),
		planeTestInput("b.fits", 64, 64),
		planeTestInput("singleton.fits", 64, 64),
		planeTestInput("reference.fits", 64, 64),
	}
	planned[3].input.ReferenceOnly = true

	got := activeSkyComponents(planned, []skyEdge{{i: 0, j: 1}})
	want := [][]int{{0, 1}, {2}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("components = %v, want %v", got, want)
	}
}

func addComponentCells(root, other map[int64]float64, startX, startY, background float64, difference skyPlane) {
	for x := startX; x < startX+128; x += 32 {
		for y := startY; y < startY+128; y += 32 {
			key := overlapCellKey(float64(x), float64(y))
			cx, cy := overlapCellCenter(key)
			root[key] = background
			other[key] = background + difference.value(cx, cy)
		}
	}
}

func componentCorrectedBaseline(t *testing.T, component []int, maps []map[int64]float64, rawSky, subtractSky []float64, planes []skyPlane) float64 {
	t.Helper()
	values := make([]float64, 0, len(component))
	for _, frameIndex := range component {
		value, ok := correctedFrameBackground(frameIndex, maps, rawSky, subtractSky, planes)
		if !ok {
			t.Fatalf("frame %d has no corrected background", frameIndex)
		}
		values = append(values, value)
	}
	return quickSelectMedian(values)
}
