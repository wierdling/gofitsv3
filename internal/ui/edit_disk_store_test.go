package ui

import (
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gofitsv3/internal/export"
	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
)

func TestEditDiskStoreOwnsBaselineAndResetsRecipe(t *testing.T) {
	d := t.TempDir()
	var src [3]string
	for i := range src {
		src[i] = filepath.Join(d, "compose", string(rune('0'+i))+".bin")
		_ = os.MkdirAll(filepath.Dir(src[i]), 0o700)
		a, err := fitsio.CreateFloat32Artifact(src[i], 2, 1)
		if err != nil {
			t.Fatal(err)
		}
		_ = a.WriteRow(0, []float32{0, 1})
		_ = a.Close()
	}
	store, err := newEditDiskStore(filepath.Join(d, "edit"), src, 2, 1, models.RgbLevels{Min: [3]float64{0, 0, 0}, Max: [3]float64{255, 255, 255}})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	baselinePaths, _, _, _ := store.Source()
	recipe := processing.DiskEditRecipe{Min: [3]float64{20, 20, 20}, Max: [3]float64{200, 200, 200}}
	for c := range recipe.Curves {
		for i := range recipe.Curves[c] {
			recipe.Curves[c][i] = byte(255 - i)
		}
	}
	if _, err := store.Render(context.Background(), recipe, 1600); err != nil {
		t.Fatal(err)
	}
	first, _, _, _ := store.Source()
	if _, err := store.Render(context.Background(), recipe, 1600); err != nil {
		t.Fatal(err)
	}
	second, _, _, _ := store.Source()
	for i := range second {
		a, err := fitsio.OpenFloat32ArtifactReadOnly(second[i])
		if err != nil {
			t.Fatal(err)
		}
		defer a.Close()
		if a.Width != 2 || a.Height != 1 {
			t.Fatal("bad repeated render dimensions")
		}
	}
	if first == second {
		t.Fatal("repeated render reused descriptor")
	}
	current, _, _, gotRecipe := store.Source()
	if current == src {
		t.Fatal("render did not publish a replacement")
	}
	if gotRecipe.Min != recipe.Min {
		t.Fatalf("recipe not captured: %+v", gotRecipe)
	}
	store.Reset()
	reset, _, _, resetRecipe := store.Source()
	if reset != baselinePaths || resetRecipe.Min != [3]float64{0, 0, 0} {
		t.Fatalf("reset state: %v %+v", reset, resetRecipe)
	}
}

func TestEditDiskStoreCleanReplaysNonIdentityRecipeWithoutRepair(t *testing.T) {
	d := t.TempDir()
	var src [3]string
	for i := range src {
		src[i] = filepath.Join(d, "src", string(rune('0'+i))+".bin")
		makeEditArtifactForStoreTest(t, src[i], []float32{0.5}, 1, 1)
	}
	store, err := newEditDiskStore(filepath.Join(d, "edit"), src, 1, 1, models.RgbLevels{Max: [3]float64{255, 255, 255}})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	recipe := identityDiskEditRecipe()
	recipe.Min = [3]float64{0, 0, 0}
	if _, err := store.Render(context.Background(), recipe, 10); err != nil {
		t.Fatal(err)
	}
	result, err := store.Clean(context.Background(), ColorSpeckCleanConfigForStoreTest(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.Repaired != 0 {
		t.Fatalf("repaired=%d want 0", result.Repaired)
	}
	planes, _, _, got := store.Source()
	if got.Min != [3]float64{0, 0, 0} {
		t.Fatalf("recipe not reset: %+v", got)
	}
	a, err := fitsio.OpenFloat32ArtifactReadOnly(planes[0])
	if err != nil {
		t.Fatal(err)
	}
	row := make([]float32, 1)
	if err := a.ReadRow(0, row); err != nil {
		_ = a.Close()
		t.Fatal(err)
	}
	_ = a.Close()
	// Disk Edit renders through its 8-bit adjustment pipeline, so the
	// effective 0.5 input is intentionally quantized to 128/255.
	want := float32(128) / 255
	if row[0] != want {
		t.Fatalf("clean did not preserve effective recipe output: got %v want %v", row[0], want)
	}
}

func TestEditDiskStoreExportSnapshotReplaysPendingRecipeAndCancelsAtomically(t *testing.T) {
	d := t.TempDir()
	var source [3]string
	for i := range source {
		source[i] = filepath.Join(d, "source", string(rune('0'+i))+".bin")
		makeEditArtifactForStoreTest(t, source[i], []float32{0.5}, 1, 1)
	}
	store, err := newEditDiskStore(filepath.Join(d, "edit"), source, 1, 1, models.RgbLevels{Max: [3]float64{255, 255, 255}})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	recipe := identityDiskEditRecipe()
	recipe.Min = [3]float64{0, 0, 0}
	recipe.Max = [3]float64{200, 200, 200}
	snapshot, err := store.ExportSnapshot(context.Background(), recipe)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "edited.png")
	if err := export.FromFloat32Artifacts(context.Background(), out, snapshot.planes, snapshot.width, snapshot.height, export.PNG, export.Options{}); err != nil {
		snapshot.cleanup()
		t.Fatal(err)
	}
	snapshot.cleanup()
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(f)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	got, _, _, _ := img.At(0, 0).RGBA()
	if got>>8 < 150 || got>>8 > 170 {
		t.Fatalf("pending level was not exported: got %d", got>>8)
	}
	if _, _, _, recipeAfter := store.Source(); recipeAfter != identityDiskEditRecipe() {
		t.Fatal("export changed the Edit store recipe")
	}

	if err := os.WriteFile(out, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	canceled, err := store.ExportSnapshot(ctx, recipe)
	if err == nil || canceled != nil {
		t.Fatalf("expected canceled export snapshot, snapshot=%v err=%v", canceled, err)
	}
	if got, err := os.ReadFile(out); err != nil || string(got) != "keep" {
		t.Fatalf("destination changed after cancellation: %q, %v", got, err)
	}
}

func TestEditDiskStoreExportSnapshotSerializesAgainstStoreLock(t *testing.T) {
	store, _ := newSpatialStoreForTest(t)
	defer store.Close()
	result := make(chan error, 1)
	store.mu.Lock()
	go func() {
		snapshot, err := store.ExportSnapshot(context.Background(), identityDiskEditRecipe())
		if snapshot != nil {
			snapshot.cleanup()
		}
		result <- err
	}()
	select {
	case err := <-result:
		store.mu.Unlock()
		t.Fatalf("snapshot bypassed store lock: %v", err)
	default:
	}
	store.mu.Unlock()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("snapshot did not proceed after store unlock")
	}
}

func TestEditDiskStoreCropCancellationPreservesDescriptors(t *testing.T) {
	store, before := newSpatialStoreForTest(t)
	defer store.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, err := store.Crop(ctx, image.Rect(0, 0, 1, 1), 10); err == nil {
		t.Fatal("expected cancellation")
	}
	after, w, h, recipe := store.Source()
	if after != before || w != 2 || h != 2 || recipe != identityDiskEditRecipe() {
		t.Fatal("crop cancellation changed descriptor state")
	}
}

func TestEditDiskStoreHealCancellationPreservesDescriptorsAndUndo(t *testing.T) {
	store, before := newSpatialStoreForTest(t)
	defer store.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Heal(ctx, processing.DiskHealStroke{Source: image.Pt(0, 0), Destinations: []image.Point{{X: 1, Y: 1}}, Radius: 1}, 10); err == nil {
		t.Fatal("expected cancellation")
	}
	after, w, h, recipe := store.Source()
	if after != before || w != 2 || h != 2 || recipe != identityDiskEditRecipe() {
		t.Fatal("heal cancellation changed descriptor state")
	}
	if _, ok, err := store.UndoHeal(context.Background(), 10); err != nil || ok {
		t.Fatalf("canceled heal changed undo state: ok=%v err=%v", ok, err)
	}
}

func TestEditDiskStoreUndoHealCancellationPreservesCurrentAndUndo(t *testing.T) {
	store, _ := newSpatialStoreForTest(t)
	defer store.Close()
	if _, err := store.Heal(context.Background(), processing.DiskHealStroke{Source: image.Pt(0, 0), Destinations: []image.Point{{X: 1, Y: 1}}, Radius: 1}, 10); err != nil {
		t.Fatal(err)
	}
	current, w, h, recipe := store.Source()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok, err := store.UndoHeal(ctx, 10); err == nil || ok {
		t.Fatalf("expected canceled undo, ok=%v err=%v", ok, err)
	}
	after, aw, ah, arecipe := store.Source()
	if after != current || aw != w || ah != h || arecipe != recipe {
		t.Fatal("canceled undo changed current descriptor")
	}
	if _, ok, err := store.UndoHeal(context.Background(), 10); err != nil || !ok {
		t.Fatalf("undo was consumed by cancellation: ok=%v err=%v", ok, err)
	}
}

func newSpatialStoreForTest(t *testing.T) (*editDiskStore, [3]string) {
	t.Helper()
	d := t.TempDir()
	var src [3]string
	for i := range src {
		src[i] = filepath.Join(d, "src", string(rune('0'+i))+".bin")
		if err := os.MkdirAll(filepath.Dir(src[i]), 0o700); err != nil {
			t.Fatal(err)
		}
		a, err := fitsio.CreateFloat32Artifact(src[i], 2, 2)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.WriteRow(0, []float32{0, 1}); err != nil {
			t.Fatal(err)
		}
		if err := a.WriteRow(1, []float32{2, 3}); err != nil {
			t.Fatal(err)
		}
		if err := a.Close(); err != nil {
			t.Fatal(err)
		}
	}
	store, err := newEditDiskStore(filepath.Join(d, "edit"), src, 2, 2, models.RgbLevels{Max: [3]float64{255, 255, 255}})
	if err != nil {
		t.Fatal(err)
	}
	before, _, _, _ := store.Source()
	return store, before
}

func makeEditArtifactForStoreTest(t *testing.T, path string, values []float32, w, h int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	a, err := fitsio.CreateFloat32Artifact(path, w, h)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.WriteRow(0, values); err != nil {
		_ = a.Close()
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
}

func ColorSpeckCleanConfigForStoreTest() processing.ColorSpeckCleanConfig {
	cfg := processing.DefaultColorSpeckCleanConfig()
	cfg.MaxBlobPixels = 1
	return cfg
}
