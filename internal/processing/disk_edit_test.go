package processing

import (
	"context"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
)

func makeEditArtifact(t *testing.T, path string, values []float32, w, h int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	a, err := fitsio.CreateFloat32Artifact(path, w, h)
	if err != nil {
		t.Fatal(err)
	}
	for y := 0; y < h; y++ {
		if err := a.WriteRow(y, values[y*w:(y+1)*w]); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
}

func readEditRow(t *testing.T, path string, w int) []float32 {
	t.Helper()
	a, err := fitsio.OpenFloat32ArtifactReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	r := make([]float32, w)
	if err := a.ReadRow(0, r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRenderDiskEditLevelsCurvesAndBoundedPreview(t *testing.T) {
	d := t.TempDir()
	var src, out [3]string
	for c := range src {
		src[c] = filepath.Join(d, "src", string(rune('0'+c))+".bin")
		out[c] = filepath.Join(d, "out", string(rune('0'+c))+".bin")
	}
	values := []float32{0, .25, .5, .75, 1, .1, .2, .3}
	for _, p := range src {
		makeEditArtifact(t, p, values, 4, 2)
	}
	r := DiskEditRecipe{Min: [3]float64{0, 0, 0}, Max: [3]float64{255, 255, 255}}
	for c := range r.Curves {
		for i := range r.Curves[c] {
			r.Curves[c][i] = byte(255 - i)
		}
	}
	got, err := RenderDiskEdit(context.Background(), DiskEditRenderRequest{Source: src, Output: out, Width: 4, Height: 2, Baseline: models.RgbLevels{Min: [3]float64{0, 0, 0}, Max: [3]float64{255, 255, 255}}, Recipe: r, PreviewMax: 3})
	if err != nil {
		t.Fatal(err)
	}
	if got.Preview.Bounds().Dx() > 3 || got.Preview.Bounds().Dy() > 3 {
		t.Fatalf("preview bounds=%v", got.Preview.Bounds())
	}
	row := readEditRow(t, out[0], 4)
	want := []float32{1, .75, .5, .25}
	for i := range row {
		if math.Abs(float64(row[i]-want[i])) > 0.01 {
			t.Fatalf("row=%v want=%v", row, want)
		}
	}
	if got.Bins[0][255] == 0 || got.Bins[0][0] == 0 {
		t.Fatalf("unexpected histogram bins: %+v", got.Bins[0][:4])
	}
}

func TestRenderDiskEditCancellationPreservesOutputs(t *testing.T) {
	d := t.TempDir()
	var src, out [3]string
	for c := range src {
		src[c] = filepath.Join(d, "src", string(rune('0'+c))+".bin")
		out[c] = filepath.Join(d, "out", string(rune('0'+c))+".bin")
		makeEditArtifact(t, src[c], []float32{.2}, 1, 1)
		makeEditArtifact(t, out[c], []float32{.9}, 1, 1)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := RenderDiskEdit(ctx, DiskEditRenderRequest{Source: src, Output: out, Width: 1, Height: 1, Baseline: models.RgbLevels{Min: [3]float64{0, 0, 0}, Max: [3]float64{255, 255, 255}}, Recipe: identityDiskEditRecipeForTest(), PreviewMax: 10})
	if err == nil {
		t.Fatal("canceled render succeeded")
	}
	for _, p := range out {
		if got := readEditRow(t, p, 1); !reflect.DeepEqual(got, []float32{.9}) {
			t.Fatalf("output changed: %v", got)
		}
	}
}

func TestRenderDiskEditPreviewDownscaleSamplesEveryPixel(t *testing.T) {
	d := t.TempDir()
	var src, out [3]string
	vals := []float32{0, .1, .2, .3, .4, .5, .6, .7, .8, .9, 1, .2, .3, .4, .5, .6}
	for c := range src {
		src[c] = filepath.Join(d, "s", string(rune('0'+c))+".bin")
		out[c] = filepath.Join(d, "o", string(rune('0'+c))+".bin")
		makeEditArtifact(t, src[c], vals, 4, 4)
	}
	r := identityDiskEditRecipeForTest()
	got, err := RenderDiskEdit(context.Background(), DiskEditRenderRequest{Source: src, Output: out, Width: 4, Height: 4, Baseline: models.RgbLevels{Max: [3]float64{255, 255, 255}}, Recipe: r, PreviewMax: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got.Preview.Bounds().Dx() != 2 || got.Preview.Bounds().Dy() != 2 {
		t.Fatalf("bounds=%v", got.Preview.Bounds())
	}
	for _, p := range []struct {
		x, y int
		want uint8
	}{{0, 0, 0}, {1, 0, 51}, {0, 1, 204}, {1, 1, 255}} {
		if got.Preview.RGBAAt(p.x, p.y).R != p.want {
			t.Fatalf("preview(%d,%d)=%d want %d", p.x, p.y, got.Preview.RGBAAt(p.x, p.y).R, p.want)
		}
	}
}

func TestRenderDiskEditPreviewNonIntegerScalePixelsAndHistogram(t *testing.T) {
	d := t.TempDir()
	var src, out [3]string
	vals := []float32{0, .1, .2, .3, .4, .5, .6, .7}
	for c := range src {
		src[c] = filepath.Join(d, "s", string(rune('0'+c))+".bin")
		out[c] = filepath.Join(d, "o", string(rune('0'+c))+".bin")
		makeEditArtifact(t, src[c], vals, 4, 2)
	}
	got, err := RenderDiskEdit(context.Background(), DiskEditRenderRequest{Source: src, Output: out, Width: 4, Height: 2, Baseline: models.RgbLevels{Max: [3]float64{255, 255, 255}}, Recipe: identityDiskEditRecipeForTest(), PreviewMax: 3})
	if err != nil {
		t.Fatal(err)
	}
	if got.Preview.Bounds().Dx() != 3 || got.Preview.Bounds().Dy() != 2 {
		t.Fatalf("bounds=%v", got.Preview.Bounds())
	}
	want := [2][3]uint8{{0, 26, 51}, {102, 128, 153}}
	for y := 0; y < 2; y++ {
		for x := 0; x < 3; x++ {
			if got.Preview.RGBAAt(x, y).R != want[y][x] {
				t.Fatalf("preview(%d,%d)=%d want %d", x, y, got.Preview.RGBAAt(x, y).R, want[y][x])
			}
		}
	}
	total := 0
	for _, n := range got.Bins[0] {
		total += n
	}
	if total != 6 {
		t.Fatalf("histogram total=%d want 6", total)
	}
}

type cancelAfterEditChecks struct {
	context.Context
	checks, limit int
}

func (c *cancelAfterEditChecks) Err() error {
	c.checks++
	if c.checks > c.limit {
		return context.Canceled
	}
	return nil
}

func TestRenderDiskEditPartialCancellationPreservesCurrentDescriptors(t *testing.T) {
	d := t.TempDir()
	var src, out [3]string
	for c := range src {
		src[c] = filepath.Join(d, "s", string(rune('0'+c))+".bin")
		out[c] = filepath.Join(d, "o", string(rune('0'+c))+".bin")
		makeEditArtifact(t, src[c], []float32{.2, .3, .4, .5}, 2, 2)
		makeEditArtifact(t, out[c], []float32{.9, .9, .9, .9}, 2, 2)
	}
	ctx := &cancelAfterEditChecks{Context: context.Background(), limit: 4}
	_, err := RenderDiskEdit(ctx, DiskEditRenderRequest{Source: src, Output: out, Width: 2, Height: 2, Baseline: models.RgbLevels{Max: [3]float64{255, 255, 255}}, Recipe: identityDiskEditRecipeForTest(), PreviewMax: 2})
	if err == nil {
		t.Fatal("partial cancellation unexpectedly succeeded")
	}
	for _, p := range out {
		if got := readEditRow(t, p, 2); got[0] != .9 {
			t.Fatalf("descriptor output changed: %v", got)
		}
	}
}

func TestRenderDiskEditSharpenMatchesInMemoryAcrossTileSeam(t *testing.T) {
	d := t.TempDir()
	const w, h = 7, 263
	var src, out [3]string
	in := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			in.SetRGBA(x, y, color.RGBA{R: uint8((x*31 + y*7) % 256), G: uint8((x*13 + y*19) % 256), B: uint8((x*47 + y*3) % 256), A: 255})
		}
	}
	for c := range src {
		src[c] = filepath.Join(d, "s", string(rune('0'+c))+".bin")
		out[c] = filepath.Join(d, "o", string(rune('0'+c))+".bin")
		values := make([]float32, w*h)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				p := in.RGBAAt(x, y)
				v := []uint8{p.R, p.G, p.B}[c]
				values[y*w+x] = float32(v) / 255
			}
		}
		makeEditArtifact(t, src[c], values, w, h)
	}
	r := identityDiskEditRecipeForTest()
	r.Strength, r.Radius = 1.35, 2.25
	if _, err := RenderDiskEdit(context.Background(), DiskEditRenderRequest{Source: src, Output: out, Width: w, Height: h, Baseline: models.RgbLevels{Max: [3]float64{255, 255, 255}}, Recipe: r, PreviewMax: 1600}); err != nil {
		t.Fatal(err)
	}
	want := SharpenRGBA(in, r.Strength, r.Radius)
	for c := range out {
		a, err := fitsio.OpenFloat32ArtifactReadOnly(out[c])
		if err != nil {
			t.Fatal(err)
		}
		row := make([]float32, w)
		for y := 0; y < h; y++ {
			if err := a.ReadRow(y, row); err != nil {
				t.Fatal(err)
			}
			for x, v := range row {
				p := want.RGBAAt(x, y)
				wantByte := []uint8{p.R, p.G, p.B}[c]
				if gotByte := uint8(v*255 + 0.5); math.Abs(float64(int(gotByte)-int(wantByte))) > 1 {
					t.Fatalf("channel %d pixel (%d,%d)=%d want %d", c, x, y, gotByte, wantByte)
				}
			}
		}
		_ = a.Close()
	}
}

func TestRenderDiskEditSharpenUsesBaselineOnlyOnce(t *testing.T) {
	d := t.TempDir()
	const w, h = 5, 1
	var src, out [3]string
	values := []float32{0, .25, .5, .75, 1}
	for c := range src {
		src[c] = filepath.Join(d, "s", string(rune('0'+c))+".bin")
		out[c] = filepath.Join(d, "o", string(rune('0'+c))+".bin")
		makeEditArtifact(t, src[c], values, w, h)
	}
	baseline := models.RgbLevels{Min: [3]float64{64, 64, 64}, Max: [3]float64{192, 192, 192}}
	r := identityDiskEditRecipeForTest()
	r.Strength, r.Radius = 1, 1
	if _, err := RenderDiskEdit(context.Background(), DiskEditRenderRequest{Source: src, Output: out, Width: w, Height: h, Baseline: baseline, Recipe: r, PreviewMax: 1600}); err != nil {
		t.Fatal(err)
	}
	expected := image.NewRGBA(image.Rect(0, 0, w, h))
	for x, v := range values {
		b := editBaselineByte(v, baseline.Min[0], baseline.Max[0])
		expected.SetRGBA(x, 0, color.RGBA{R: b, G: b, B: b, A: 255})
	}
	want := SharpenRGBA(expected, r.Strength, r.Radius)
	a, err := fitsio.OpenFloat32ArtifactReadOnly(out[0])
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	row := make([]float32, w)
	if err := a.ReadRow(0, row); err != nil {
		t.Fatal(err)
	}
	for x, v := range row {
		if got, wantByte := uint8(v*255+0.5), want.RGBAAt(x, 0).R; math.Abs(float64(int(got)-int(wantByte))) > 1 {
			t.Fatalf("pixel %d=%d want %d", x, got, wantByte)
		}
	}
}

func identityDiskEditRecipeForTest() DiskEditRecipe {
	r := DiskEditRecipe{Min: [3]float64{0, 0, 0}, Max: [3]float64{255, 255, 255}}
	for c := range r.Curves {
		for i := range r.Curves[c] {
			r.Curves[c][i] = byte(i)
		}
	}
	return r
}
