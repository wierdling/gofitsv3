package processing

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"testing"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/stretch"
)

// starTreatmentScene renders Gaussian stars on a sloped background, fits and
// prepares them, and returns the linear pixels with the prepared fits.
func starTreatmentScene(t *testing.T, w, h int, stars [][4]float64, meta models.LoadedImage) ([]float32, []StarTreatmentFit) {
	t.Helper()
	pixels := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := .05 + .0002*float64(x) + .0001*float64(y)
			for _, s := range stars {
				dx, dy := float64(x)-s[0], float64(y)-s[1]
				v += s[2] * math.Exp(-.5*(dx*dx+dy*dy)/(s[3]*s[3]))
			}
			pixels[y*w+x] = float32(v)
		}
	}
	m := &StarMap{Width: w, Height: h}
	for i, s := range stars {
		m.Sources = append(m.Sources, StarMapSource{ID: i + 1, X: s[0], Y: s[1], FWHM: 2.355 * s[3], Radius: 4 * s[3], Status: "accepted"})
	}
	fits, err := FitStarTreatment(context.Background(), m, pixels, w, h, StarTreatmentOptions{})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareStarStretchFits(context.Background(), pixels, w, h, meta, fits, m.Sources)
	if err != nil {
		t.Fatal(err)
	}
	usable := 0
	for _, f := range prepared {
		if f.Usable {
			usable++
		}
	}
	if usable == 0 {
		t.Fatalf("scene produced no usable fits: %+v", prepared)
	}
	return pixels, prepared
}

var starTreatmentTestMeta = models.LoadedImage{Mode: stretch.Asinh, Background: 0, Peak: 1, ScaledPeak: 10, AsinhScale: 1, MTFMidtone: .25}

func TestStarTreatmentModelIndexMatchesBruteForce(t *testing.T) {
	const w, h = 300, 200
	var stars [][4]float64
	for i := 0; i < 12; i++ {
		stars = append(stars, [4]float64{30 + float64(i%4)*70, 40 + float64(i/4)*60, .5 + .1*float64(i), 1.4})
	}
	pixels, fits := starTreatmentScene(t, w, h, stars, starTreatmentTestMeta)
	model, err := NewStarTreatmentModel(fits, w, h, starTreatmentTestMeta, .8)
	if err != nil {
		t.Fatal(err)
	}
	if len(model.Fits()) < 8 {
		t.Fatalf("expected most stars usable, got %d", len(model.Fits()))
	}
	// Brute force: the same rule over every fit, with no index.
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := pixels[y*w+x]
			base := stretchDiskValue(v, starTreatmentTestMeta)
			correction := 0.
			for _, f := range model.Fits() {
				weight := starTreatmentFitWeight(float64(x), float64(y), f)
				if weight == 0 {
					continue
				}
				q := unclippedStarStretch(float64(v), starTreatmentTestMeta)
				b := unclippedStarStretch(starTreatmentBackground(float64(x), float64(y), f), starTreatmentTestMeta)
				excess := math.Max(0, q-b)
				gentle := excess / (1 + 4*.8*excess)
				candidate := math.Max(0, math.Min(1, q-weight*(excess-gentle)))
				correction = math.Max(correction, math.Max(0, float64(base)-candidate))
			}
			want := float32(math.Max(0, float64(base)-correction))
			if got := model.TreatedStretch(v, float64(x), float64(y)); got != want {
				t.Fatalf("pixel (%d,%d): indexed %v brute force %v", x, y, got, want)
			}
		}
	}
	// The indexed model is what ApplyGentlerStarStretch renders.
	rendered, err := ApplyGentlerStarStretch(context.Background(), pixels, w, h, starTreatmentTestMeta, fits, nil, StarStretchOptions{Strength: .8})
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range rendered {
		if v != model.TreatedStretch(pixels[i], float64(i%w), float64(i/w)) {
			t.Fatalf("ApplyGentlerStarStretch differs from the model at %d", i)
		}
	}
}

func TestStarTreatmentModelZeroStrengthAndOutsideAreOrdinaryStretch(t *testing.T) {
	const w, h = 120, 100
	pixels, fits := starTreatmentScene(t, w, h, [][4]float64{{60, 50, 1, 1.5}}, starTreatmentTestMeta)
	zero, err := NewStarTreatmentModel(fits, w, h, starTreatmentTestMeta, 0)
	if err != nil {
		t.Fatal(err)
	}
	full, err := NewStarTreatmentModel(fits, w, h, starTreatmentTestMeta, 1)
	if err != nil {
		t.Fatal(err)
	}
	extent := StarTreatmentExtent(full.Fits()[0])
	reduced := false
	for i, v := range pixels {
		x, y := float64(i%w), float64(i/w)
		base := stretchDiskValue(v, starTreatmentTestMeta)
		if got := zero.TreatedStretch(v, x, y); got != base {
			t.Fatalf("strength zero changed pixel %d: %v vs %v", i, got, base)
		}
		got := full.TreatedStretch(v, x, y)
		if math.Hypot(x-60, y-50) >= extent && got != base {
			t.Fatalf("pixel %d outside the footprint changed: %v vs %v", i, got, base)
		}
		if got > base {
			t.Fatalf("pixel %d brightened: %v > %v", i, got, base)
		}
		if got < base {
			reduced = true
		}
	}
	if !reduced {
		t.Fatal("full strength changed nothing")
	}
	if !full.MatchesStretch(starTreatmentTestMeta) {
		t.Fatal("model does not match the settings it was built for")
	}
	other := starTreatmentTestMeta
	other.Peak = 2
	if full.MatchesStretch(other) {
		t.Fatal("model matched different stretch settings")
	}
	if _, err := NewStarTreatmentModel(fits, w, h, models.LoadedImage{Mode: stretch.HistEq, Peak: 1}, .5); err == nil {
		t.Fatal("HistEq must be rejected")
	}
}

func TestTreatedStretchForSourceRejectsStaleTreatment(t *testing.T) {
	const w, h = 100, 100
	pixels, fits := starTreatmentScene(t, w, h, [][4]float64{{50, 50, 1, 1.5}}, starTreatmentTestMeta)
	model, err := NewStarTreatmentModel(fits, w, h, starTreatmentTestMeta, .5)
	if err != nil {
		t.Fatal(err)
	}
	img := starTreatmentTestMeta
	img.HDU.Data = fitsio.ImageData{Width: w, Height: h, Pixels: pixels}
	img.StarTreatment = model
	if _, err := TreatedStretchForSource(context.Background(), &img); err != nil {
		t.Fatal(err)
	}
	stale := img
	stale.Peak = 3
	if _, err := TreatedStretchForSource(context.Background(), &stale); err == nil {
		t.Fatal("changed stretch settings must reject the prepared treatment")
	}
	// A stale treatment inside the composer falls back to the ordinary stretch
	// rather than rendering a stale footprint.
	plain := stale
	plain.StarTreatment = nil
	want, _ := ApplyStretchParallel(&plain)
	got := stretchForReferenceGrid(context.Background(), &stale, &stale)
	for i := range want.Pixels {
		if got.Pixels[i] != want.Pixels[i] {
			t.Fatalf("stale treatment altered pixel %d", i)
		}
	}
	resized := img
	resized.HDU.Data = fitsio.ImageData{Width: w / 2, Height: h, Pixels: pixels[:w*h/2]}
	if _, err := TreatedStretchForSource(context.Background(), &resized); err == nil {
		t.Fatal("a different grid must reject the prepared treatment")
	}
}

// writeStarTreatmentArtifact stores linear pixels as a disk artifact.
func writeStarTreatmentArtifact(t *testing.T, path string, pixels []float32, w, h int) {
	t.Helper()
	a, err := fitsio.CreateFloat32Artifact(path, w, h)
	if err != nil {
		t.Fatal(err)
	}
	for y := 0; y < h; y++ {
		if err := a.WriteRow(y, pixels[y*w:(y+1)*w]); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
}

func readStarTreatmentArtifact(t *testing.T, path string, w, h int) []float32 {
	t.Helper()
	a, err := fitsio.OpenFloat32ArtifactReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	out := make([]float32, w*h)
	for y := 0; y < h; y++ {
		if err := a.ReadRow(y, out[y*w:(y+1)*w]); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// TestComposeDiskStarTreatmentMatchesNormal renders one treated channel on a
// shared grid through both compositors, in artistic and weighted modes, and
// requires the same result; it also checks the treated red channel actually
// differs from the untreated one and that the other channels are unchanged.
func TestComposeDiskStarTreatmentMatchesNormal(t *testing.T) {
	const w, h = 96, 80
	meta := starTreatmentTestMeta
	pixels, fits := starTreatmentScene(t, w, h, [][4]float64{{48, 40, 1.5, 1.5}, {20, 60, .8, 1.3}}, meta)
	model, err := NewStarTreatmentModel(fits, w, h, meta, .75)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	imgs := make([]*models.LoadedImage, 3)
	var channels [3]DiskChannel
	for i := range imgs {
		img := meta
		img.HDU.Data = fitsio.ImageData{Width: w, Height: h, Pixels: append([]float32(nil), pixels...)}
		if i == 2 { // red channel carries the treatment
			img.StarTreatment = model
		}
		imgs[i] = &img
		path := filepath.Join(dir, fmt.Sprintf("src-%d.bin", i))
		writeStarTreatmentArtifact(t, path, pixels, w, h)
		channels[i] = DiskChannel{ArtifactPath: path, Image: img}
	}
	untreated := *imgs[2]
	untreated.StarTreatment = nil

	// Artistic mode: byte RGBA from the normal path vs disk artifacts.
	normalBuf, nw, nh, _ := ComposeRGB(context.Background(), imgs)
	if nw != w || nh != h {
		t.Fatalf("normal compose size %dx%d", nw, nh)
	}
	out := [3]string{filepath.Join(dir, "r.bin"), filepath.Join(dir, "g.bin"), filepath.Join(dir, "b.bin")}
	if _, err := ComposeDisk(context.Background(), DiskComposeRequest{Channels: channels, Output: out}); err != nil {
		t.Fatal(err)
	}
	treatedRed, err := TreatedStretchForSource(context.Background(), imgs[2])
	if err != nil {
		t.Fatal(err)
	}
	plainRed, _ := ApplyStretchParallel(&untreated)
	diskRed := readStarTreatmentArtifact(t, out[0], w, h)
	diskGreen := readStarTreatmentArtifact(t, out[1], w, h)
	plainGreen, _ := ApplyStretchParallel(imgs[1])
	changed := 0
	for i := range diskRed {
		if math.Abs(float64(diskRed[i]-treatedRed.Pixels[i])) > 1e-5 {
			t.Fatalf("artistic disk red %d = %v, normal treated %v", i, diskRed[i], treatedRed.Pixels[i])
		}
		if math.Abs(float64(diskGreen[i]-plainGreen.Pixels[i])) > 1e-5 {
			t.Fatalf("artistic disk green %d = %v, normal %v", i, diskGreen[i], plainGreen.Pixels[i])
		}
		if want := byte(clamp01(float64(treatedRed.Pixels[i]))*255 + .5); normalBuf[i*4] != want && normalBuf[i*4] != want-1 && normalBuf[i*4] != want+1 {
			t.Fatalf("normal RGBA red %d = %d, treated %d", i, normalBuf[i*4], want)
		}
		if diskRed[i] < plainRed.Pixels[i]-1e-6 {
			changed++
		}
		if diskRed[i] > plainRed.Pixels[i]+1e-6 {
			t.Fatalf("treated red brightened pixel %d", i)
		}
	}
	if changed == 0 {
		t.Fatal("treated red channel equals the untreated one")
	}

	// Weighted mode: float planes from both paths.
	weights := []models.ComposeMixWeight{{BlinkID: models.ComposeChannel1BlinkID, Blue: 1}, {BlinkID: models.ComposeChannel2BlinkID, Green: 1}, {BlinkID: models.ComposeChannel3BlinkID, Red: .9, Green: .1}}
	planes, _, _, err := ComposeWeightedRGBPlanes(context.Background(), imgs, nil, weights)
	if err != nil {
		t.Fatal(err)
	}
	wout := [3]string{filepath.Join(dir, "wr.bin"), filepath.Join(dir, "wg.bin"), filepath.Join(dir, "wb.bin")}
	if _, err := ComposeDisk(context.Background(), DiskComposeRequest{Channels: channels, Output: wout, CompositionMode: models.ComposeModeWeighted, MixWeights: weights}); err != nil {
		t.Fatal(err)
	}
	for c := 0; c < 3; c++ {
		disk := readStarTreatmentArtifact(t, wout[c], w, h)
		for i := range disk {
			if math.Abs(float64(disk[i]-planes[c][i])) > 1e-5 {
				t.Fatalf("weighted channel %d pixel %d: disk %v normal %v", c, i, disk[i], planes[c][i])
			}
		}
	}

	// A treatment prepared for other settings is an error on disk, never a
	// silent fallback.
	stale := channels
	stale[2].Image.Peak = 2
	if _, err := ComposeDisk(context.Background(), DiskComposeRequest{Channels: stale, Output: [3]string{filepath.Join(dir, "sr.bin"), filepath.Join(dir, "sg.bin"), filepath.Join(dir, "sb.bin")}, CompositionMode: models.ComposeModeWeighted, MixWeights: weights}); err == nil {
		t.Fatal("stale star treatment was accepted by the disk compositor")
	}
}
