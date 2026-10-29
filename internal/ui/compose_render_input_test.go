package ui

import (
	"context"
	"sync"
	"testing"

	fynetest "fyne.io/fyne/v2/test"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/stretch"
)

func composeRenderTestImage(w, h int, v float32) *models.LoadedImage {
	px := make([]float32, w*h)
	for i := range px {
		px[i] = v * float32(i%w+1) / float32(w)
	}
	return &models.LoadedImage{
		HDU:   fitsio.HDU{Data: fitsio.ImageData{Width: w, Height: h, Pixels: px}},
		Mode:  stretch.Linear,
		Black: 0, White: 1, Peak: 1, ScaledPeak: 1,
	}
}

// composeRenderTestWorkspace builds the minimum workspace composeRGB needs:
// three channels with widget-backed offsets and one open overlay layer.
func composeRenderTestWorkspace(t *testing.T) *composeWorkspace {
	t.Helper()
	ws := &composeWorkspace{
		imgs:            []*models.LoadedImage{composeRenderTestImage(16, 12, .3), composeRenderTestImage(16, 12, .5), composeRenderTestImage(16, 12, .7), composeRenderTestImage(16, 12, .9)},
		levels:          defaultRGBLevels(),
		compositionMode: models.ComposeModeAuto,
		mixWeights:      []models.ComposeMixWeight{{BlinkID: models.ComposeChannel1BlinkID, Blue: 1}},
		largeArtifacts:  map[int]composeArtifactDescriptor{0: {Path: "a"}},
	}
	ws.renderCache = make([]composeRenderCache, len(ws.imgs))
	for range ws.imgs {
		x, y, r := NewNumberEntry(1, 2), NewNumberEntry(1, 2), NewNumberEntry(1, 2)
		ws.controlSets = append(ws.controlSets, &models.ChannelControl{XOffsetEntry: x, YOffsetEntry: y, RotOffsetEntry: r})
	}
	ws.controlSets[0].XOffsetEntry.SetValue(1)
	ws.overlayLayers = []*overlayLayer{{idx: 3, settings: defaultOverlayLayerSettings(0), win: fynetest.NewWindow(nil)}}
	return ws
}

func TestComposeSnapshotRenderInputDoesNotAliasWorkspace(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()
	ws := composeRenderTestWorkspace(t)
	ws.largeMode = true

	in := ws.snapshotRenderInput()
	wantBlack := in.imgs[0].Black

	// Mutate everything the snapshot copied, as UI handlers would.
	ws.imgs[0].Black = 0.4 // Apply/Auto write stretch fields in place
	ws.imgs[1] = nil       // a slot is cleared
	ws.mixWeights[0].Red = 9
	ws.mixWeights = append(ws.mixWeights, models.ComposeMixWeight{BlinkID: "x"})
	ws.psfSettings.Enabled = true
	ws.compositionMode = models.ComposeModeWeighted
	ws.overlayLayers[0].settings.Opacity = 0.1
	ws.overlayLayers[0].win = nil
	ws.levels.Max[0] = 0.5
	ws.largeArtifacts[0] = composeArtifactDescriptor{Path: "b"}
	ws.controlSets[0].XOffsetEntry.SetValue(7)

	if in.imgs[0] == ws.imgs[0] {
		t.Fatal("snapshot image aliases the workspace image")
	}
	if in.imgs[0].Black != wantBlack {
		t.Errorf("snapshot Black = %v, want %v", in.imgs[0].Black, wantBlack)
	}
	if in.imgs[1] == nil {
		t.Error("clearing a workspace slot cleared the snapshot slot")
	}
	if len(in.weights) != 1 || in.weights[0].Red != 0 {
		t.Errorf("snapshot weights = %+v, want the original single weight", in.weights)
	}
	if in.psf.Enabled || in.mode != models.ComposeModeAuto {
		t.Errorf("snapshot psf/mode changed: %+v %v", in.psf, in.mode)
	}
	if len(in.overlays) != 1 || !in.overlays[0].active || in.overlays[0].settings.Opacity != 1 {
		t.Errorf("snapshot overlays = %+v, want one active layer at opacity 1", in.overlays)
	}
	if in.levels.Max[0] == 0.5 {
		t.Error("snapshot levels alias the workspace levels")
	}
	if in.largeArtifacts[0].Path != "a" {
		t.Errorf("snapshot artifact = %q, want a", in.largeArtifacts[0].Path)
	}
	if off := in.offset(0); !off.ok || off.dx != 1 {
		t.Errorf("snapshot offset = %+v, want dx 1", off)
	}
}

// TestComposeRenderRacesWithWorkspaceMutation runs renders from snapshots on
// worker goroutines while the "UI" goroutine keeps changing the settings the
// render reads. Run with -race: composeRGB must read only its snapshot.
func TestComposeRenderRacesWithWorkspaceMutation(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()
	ws := composeRenderTestWorkspace(t)

	var wg sync.WaitGroup
	for i := range 40 {
		in := ws.snapshotRenderInput()
		ctx, gen := ws.renderGen.begin(nil)
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf, w, h, _, err := ws.composeRGB(ctx, in, nil)
			if err == nil && ctx.Err() == nil && len(buf) != w*h*4 {
				t.Errorf("render %d: buffer %d bytes for %dx%d", gen, len(buf), w, h)
			}
		}()
		// UI-side mutations after the snapshot.
		ws.imgs[i%3].Black = float64(i%5) / 10
		ws.mixWeights = append(ws.mixWeights[:0], models.ComposeMixWeight{BlinkID: models.ComposeChannel2BlinkID, Green: float64(i)})
		ws.psfSettings.Enabled = false
		if i%2 == 0 {
			ws.compositionMode = models.ComposeModeWeighted
		} else {
			ws.compositionMode = models.ComposeModeAuto
		}
		ws.overlayLayers[0].settings.Opacity = float64(i%10) / 10
		ws.levels.Max[1] = 1 - float64(i%3)/10
		ws.controlSets[i%3].XOffsetEntry.SetValue(float64(i % 4))
		replaceComposeChannelImage(ws.imgs, i%3, composeRenderTestImage(16, 12, float32(i%7+1)/10))
	}
	wg.Wait()
	if _, _, _, _, err := ws.composeRGB(context.Background(), ws.snapshotRenderInput(), nil); err != nil {
		t.Fatalf("final render: %v", err)
	}
}
