package ui

import (
	"context"
	"encoding/json"
	"image"
	"testing"

	"gofitsv3/internal/models"
)

func TestLegacyZeroOverlayGetsBlinkIdentityAcrossSaveLoad(t *testing.T) {
	state := defaultOverlayLayerSettings(0)
	state = models.OrangeLayerState{}
	state = normalizeComposeOverlayState(state, 0)
	if state.BlinkID != "overlay-legacy-0" || state.Opacity == 0 {
		t.Fatalf("normalized overlay = %#v", state)
	}
	project := models.ComposeProject{OverlayLayers: []models.OrangeLayerState{state}}
	data, err := json.Marshal(project)
	if err != nil {
		t.Fatal(err)
	}
	var decoded models.ComposeProject
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.OverlayLayers) != 1 || decoded.OverlayLayers[0].BlinkID != state.BlinkID {
		t.Fatalf("decoded overlay identity = %#v, want %q", decoded.OverlayLayers, state.BlinkID)
	}
}

func TestBuildComposeBlinkFramesOrdersRGBAndOverlayAndSkipsMissing(t *testing.T) {
	imgs := make([]*models.LoadedImage, 5)
	for i := 0; i < 4; i++ {
		imgs[i] = composeMagicTestImage("")
	}
	sources := []composeBlinkSource{{ProjectIndex: 0, RuntimeIndex: 0, Name: "Blue"}, {ProjectIndex: 1, RuntimeIndex: 3, Name: "Gold"}, {ProjectIndex: 2, RuntimeIndex: 4, Name: "Gone"}}
	base := [4]composeViewportPreview{{Image: image.NewRGBA(image.Rect(0, 0, 100, 100)), OrigW: 100, OrigH: 100}}
	frames := buildComposeBlinkFrames(context.Background(), imgs, sources, []int{1, 99, 0, 2}, base)
	if len(frames) != 2 || frames[0].ProjectIndex != 1 || frames[1].ProjectIndex != 0 {
		t.Fatalf("frames = %#v, want selected order with missing skipped", frames)
	}
}

func TestBuildComposeBlinkFramesCancellationReturnsNil(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	frames := buildComposeBlinkFrames(ctx, nil, []composeBlinkSource{{ProjectIndex: 0, RuntimeIndex: 0}}, []int{0}, [4]composeViewportPreview{})
	if frames != nil {
		t.Fatalf("frames = %#v, want nil on cancellation", frames)
	}
}

func TestEnumerateComposeBlinkSourcesCompactsSparseOverlays(t *testing.T) {
	imgs := make([]*models.LoadedImage, 8)
	imgs[0], imgs[2], imgs[5], imgs[7] = &models.LoadedImage{}, &models.LoadedImage{}, &models.LoadedImage{}, &models.LoadedImage{}
	sources := enumerateComposeBlinkSources(imgs, []composeBlinkOverlaySource{{RuntimeIndex: 7, Name: "Gold"}, {RuntimeIndex: 5, Name: "Ha"}})
	if len(sources) != 4 {
		t.Fatalf("source count = %d, want 4", len(sources))
	}
	wantNames := []string{"Blue", "Red", "Ha", "Gold"}
	wantRuntime := []int{0, 2, 5, 7}
	for i, source := range sources {
		if source.Name != wantNames[i] || source.RuntimeIndex != wantRuntime[i] || source.ProjectIndex != i {
			t.Fatalf("source[%d] = %#v, want name %q runtime %d project %d", i, source, wantNames[i], wantRuntime[i], i)
		}
	}
}

func TestResolveComposeBlinkSelectionMigratesLegacyAndFiltersStale(t *testing.T) {
	sources := []composeBlinkSource{{ProjectIndex: 0, RuntimeIndex: 0}, {ProjectIndex: 1, RuntimeIndex: 1}, {ProjectIndex: 2, RuntimeIndex: 2}, {ProjectIndex: 3, RuntimeIndex: 5}}
	if got := resolveComposeBlinkSelection(sources, nil, true, 1); !equalInts(got, []int{0, 2}) {
		t.Fatalf("legacy selection = %#v, want [0 2]", got)
	}
	if got := resolveComposeBlinkSelection(sources, []int{2, 99, 2, 0}, true, 1); !equalInts(got, []int{2, 0}) {
		t.Fatalf("custom selection = %#v, want [2 0]", got)
	}
	if got := resolveComposeBlinkSelection(sources, nil, false, 0); !equalInts(got, []int{0, 1, 2, 3}) {
		t.Fatalf("default selection = %#v, want all sources", got)
	}
}

func TestRemapComposeBlinkSelectionDropsRemovedOverlayAndKeepsRemainingIdentity(t *testing.T) {
	oldSources := []composeBlinkSource{
		{ProjectIndex: 0, RuntimeIndex: 0}, {ProjectIndex: 1, RuntimeIndex: 3}, {ProjectIndex: 2, RuntimeIndex: 5},
	}
	newSources := []composeBlinkSource{
		{ProjectIndex: 0, RuntimeIndex: 0}, {ProjectIndex: 1, RuntimeIndex: 5},
	}
	if got := remapComposeBlinkSelection([]int{1, 2}, oldSources, newSources); !equalInts(got, []int{1}) {
		t.Fatalf("remapped selection = %#v, want [1]", got)
	}
	// A later reuse of runtime slot 3 starts from the remapped state and does
	// not inherit the removed overlay's old selection.
	selection := remapComposeBlinkSelection([]int{1, 2}, oldSources, newSources)
	if got := filterComposeBlinkSelection(selection, append(newSources, composeBlinkSource{ProjectIndex: 2, RuntimeIndex: 3})); !equalInts(got, []int{1}) {
		t.Fatalf("reused slot selection = %#v, want [1]", got)
	}
}

func TestComposeBlinkSelectionMapsAndCycles(t *testing.T) {
	sources := []composeBlinkSource{{ProjectIndex: 0, RuntimeIndex: 0}, {ProjectIndex: 1, RuntimeIndex: 5}, {ProjectIndex: 2, RuntimeIndex: 9}}
	if got := composeBlinkRuntimeIndices([]int{2, 99, 0}, sources); !equalInts(got, []int{9, 0}) {
		t.Fatalf("runtime indices = %#v, want [9 0]", got)
	}
	if got, ok := cycleComposeBlinkSelection([]int{0, 2}, 0); !ok || got != 2 {
		t.Fatalf("cycle after 0 = (%d,%v), want (2,true)", got, ok)
	}
	if got, ok := cycleComposeBlinkSelection([]int{0, 2}, 7); !ok || got != 0 {
		t.Fatalf("cycle stale = (%d,%v), want (0,true)", got, ok)
	}
	if _, ok := cycleComposeBlinkSelection(nil, 0); ok {
		t.Fatal("empty cycle reported success")
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
