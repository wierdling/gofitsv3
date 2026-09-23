package processing

import (
	"context"
	"math"
	"path/filepath"
	"testing"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/stretch"
)

// neutralizeScene builds a red star (strong in R, weak in G and B) on a
// colored background and returns the three linear channel images, the red
// channel's prepared fits and the star position.
func neutralizeScene(t *testing.T, w, h int, cx, cy float64) ([3]*models.LoadedImage, []StarTreatmentFit) {
	t.Helper()
	meta := models.LoadedImage{Mode: stretch.Linear, Background: 0, Peak: 1, ScaledPeak: 1}
	amps := [3]float64{.9, .25, .15} // R, G, B stellar amplitude
	bg := [3]float64{.30, .20, .10}  // R, G, B background: reddish nebula
	var imgs [3]*models.LoadedImage
	for c := 0; c < 3; c++ {
		pixels := make([]float32, w*h)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				dx, dy := float64(x)-cx, float64(y)-cy
				pixels[y*w+x] = float32(bg[c] + .0005*float64(x) + amps[c]*math.Exp(-.5*(dx*dx+dy*dy)/(1.6*1.6)))
			}
		}
		img := meta
		img.HDU.Data = fitsio.ImageData{Width: w, Height: h, Pixels: pixels}
		imgs[c] = &img
	}
	m := &StarMap{Width: w, Height: h, Sources: []StarMapSource{{ID: 1, X: cx, Y: cy, FWHM: 3.8, Radius: 7, Status: "accepted"}}}
	fits, err := FitStarTreatment(context.Background(), m, imgs[0].HDU.Data.Pixels, w, h, StarTreatmentOptions{})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareStarStretchFits(context.Background(), imgs[0].HDU.Data.Pixels, w, h, meta, fits, m.Sources)
	if err != nil {
		t.Fatal(err)
	}
	if !prepared[0].Usable {
		t.Fatalf("red star not usable: %+v", prepared[0])
	}
	return imgs, prepared
}

func neutralizePlanes(imgs [3]*models.LoadedImage) [3][]float32 {
	var planes [3][]float32
	for c := range imgs {
		d, _ := ApplyStretchParallel(imgs[c])
		planes[c] = d.Pixels
	}
	return planes
}

func TestStarNeutralizerWhitensCoreAndKeepsBackground(t *testing.T) {
	const w, h, cx, cy = 90, 80, 45, 40
	imgs, fits := neutralizeScene(t, w, h, cx, cy)
	model, err := NewStarTreatmentModel(fits, w, h, *imgs[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	settings := models.StarWhiteningState{Enabled: true, Strength: 1, Level: models.StarWhiteningWhite, Red: true, Green: true, Blue: true}
	n, err := NewStarNeutralizer(model, DiskChannel{Image: *imgs[0]}, *imgs[1], w, h, settings)
	if err != nil {
		t.Fatal(err)
	}
	if n.Stars() != 1 {
		t.Fatalf("expected one star, got %d", n.Stars())
	}
	before := neutralizePlanes(imgs)
	after := neutralizePlanes(imgs)
	if err := n.Apply(context.Background(), after); err != nil {
		t.Fatal(err)
	}
	center := int(cy)*w + int(cx)
	r, g, b := after[0][center], after[1][center], after[2][center]
	// Only the stellar excess is neutralized; the nebula under the core keeps
	// its color, so channel differences at the core equal the background's
	// (R is display-clipped in this fixture, so compare G and B).
	if got, want := float64(g-b), .20-.10; math.Abs(got-want) > .02 {
		t.Fatalf("core excess not neutral: G-B=%.3f, background G-B=%.3f (R=%.3f G=%.3f B=%.3f)", got, want, r, g, b)
	}
	if r < before[0][center]-.01 {
		t.Fatalf("white level dimmed the brightest channel: %.3f -> %.3f", before[0][center], r)
	}
	if g <= before[1][center] || b <= before[2][center] {
		t.Fatal("weak channels were not raised toward white")
	}
	extent := StarTreatmentExtent(model.Fits()[0])
	changedInside, changedOutside := 0, 0
	for i := range after[0] {
		x, y := float64(i%w), float64(i/w)
		d := math.Hypot(x-cx, y-cy)
		moved := false
		for c := 0; c < 3; c++ {
			if math.Abs(float64(after[c][i]-before[c][i])) > 1e-6 {
				moved = true
			}
		}
		if moved && d >= extent {
			changedOutside++
		}
		if moved && d < extent {
			changedInside++
		}
	}
	if changedOutside > 0 {
		t.Fatalf("%d pixels changed outside the footprint", changedOutside)
	}
	if changedInside == 0 {
		t.Fatal("nothing inside the footprint changed")
	}
	// In the feather the background keeps its color: the R-B difference of a
	// pixel dominated by background must remain close to the background's.
	fit := model.Fits()[0]
	edge := int(cy)*w + int(cx+fit.OuterRadius-1)
	weight := starTreatmentFitWeight(cx+fit.OuterRadius-1, cy, fit)
	if weight <= 0 || weight >= 1 {
		t.Fatalf("test pixel not in the feather: weight %.3f", weight)
	}
	bgDiff := float64(before[0][edge] - before[2][edge])
	if float64(after[0][edge]-after[2][edge]) < .5*bgDiff {
		t.Fatalf("feather pixel lost its background color: before R-B=%.3f after %.3f", bgDiff, after[0][edge]-after[2][edge])
	}
}

func TestStarNeutralizerChannelSelectionAndLuminance(t *testing.T) {
	const w, h, cx, cy = 90, 80, 45, 40
	imgs, fits := neutralizeScene(t, w, h, cx, cy)
	model, err := NewStarTreatmentModel(fits, w, h, *imgs[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	before := neutralizePlanes(imgs)
	center := int(cy)*w + int(cx)

	blueOnly := models.StarWhiteningState{Enabled: true, Strength: 1, Level: models.StarWhiteningWhite, Blue: true}
	n, err := NewStarNeutralizer(model, DiskChannel{Image: *imgs[0]}, *imgs[1], w, h, blueOnly)
	if err != nil {
		t.Fatal(err)
	}
	after := neutralizePlanes(imgs)
	if err := n.Apply(context.Background(), after); err != nil {
		t.Fatal(err)
	}
	for i := range after[0] {
		if after[0][i] != before[0][i] || after[1][i] != before[1][i] {
			t.Fatalf("unselected channel changed at %d", i)
		}
	}
	if after[2][center] <= before[2][center] {
		t.Fatal("selected blue channel did not move")
	}

	lum := models.StarWhiteningState{Enabled: true, Strength: 1, Level: models.StarWhiteningLuminance, Red: true, Green: true, Blue: true}
	n, err = NewStarNeutralizer(model, DiskChannel{Image: *imgs[0]}, *imgs[1], w, h, lum)
	if err != nil {
		t.Fatal(err)
	}
	after = neutralizePlanes(imgs)
	if err := n.Apply(context.Background(), after); err != nil {
		t.Fatal(err)
	}
	// Luminance of the stellar excess is preserved: the core is neutral but
	// dimmer than the white level, since red dominated.
	if got, want := float64(after[0][center]-after[1][center]), .30-.20; math.Abs(got-want) > .02 || after[0][center] >= before[0][center] {
		t.Fatalf("luminance level: R=%.3f G=%.3f (R-G want %.2f) before R=%.3f", after[0][center], after[1][center], want, before[0][center])
	}

	if _, err := NewStarNeutralizer(model, DiskChannel{Image: *imgs[0]}, *imgs[1], w, h, models.StarWhiteningState{Strength: .5}); err == nil {
		t.Fatal("no selected channel must be rejected")
	}
	zero := models.StarWhiteningState{Enabled: true, Strength: 0, Red: true, Green: true, Blue: true}
	n, _ = NewStarNeutralizer(model, DiskChannel{Image: *imgs[0]}, *imgs[1], w, h, zero)
	after = neutralizePlanes(imgs)
	_ = n.Apply(context.Background(), after)
	for c := range after {
		for i := range after[c] {
			if after[c][i] != before[c][i] {
				t.Fatalf("strength zero changed pixel %d", i)
			}
		}
	}
}

func TestStarNeutralizerFollowsReferenceOffsetAndMatchesDisk(t *testing.T) {
	const w, h, cx, cy = 90, 80, 45, 40
	imgs, fits := neutralizeScene(t, w, h, cx, cy)
	model, err := NewStarTreatmentModel(fits, w, h, *imgs[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	// The reference is composed with a manual offset of (+6, -3): the star
	// appears at (cx-6, cy+3) in the composite, and so must the whitening.
	shifted := neutralizePlanes(imgs)
	for c := range shifted {
		src := shifted[c]
		dst := make([]float32, w*h)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				sx, sy := x+6, y-3
				if sx >= 0 && sx < w && sy >= 0 && sy < h {
					dst[y*w+x] = src[sy*w+sx]
				}
			}
		}
		shifted[c] = dst
	}
	settings := models.StarWhiteningState{Enabled: true, Strength: 1, Level: models.StarWhiteningWhite, Red: true, Green: true, Blue: true}
	n, err := NewStarNeutralizer(model, DiskChannel{Image: *imgs[0], OffsetX: -6, OffsetY: 3}, *imgs[1], w, h, settings)
	if err != nil {
		t.Fatal(err)
	}
	mem := [3][]float32{append([]float32(nil), shifted[0]...), append([]float32(nil), shifted[1]...), append([]float32(nil), shifted[2]...)}
	if err := n.Apply(context.Background(), mem); err != nil {
		t.Fatal(err)
	}
	center := (int(cy)+3)*w + int(cx) - 6
	if got, want := float64(mem[1][center]-mem[2][center]), .20-.10; math.Abs(got-want) > .02 || mem[2][center] <= shifted[2][center] {
		t.Fatalf("shifted core excess not neutral: G-B=%.3f want %.2f, B %.3f -> %.3f", got, want, shifted[2][center], mem[2][center])
	}
	// A pixel beyond the footprint at the star's unshifted position is untouched.
	far := (int(cy)+16)*w + int(cx) + 16
	if mem[0][far] != shifted[0][far] {
		t.Fatal("pixel outside the shifted footprint was treated")
	}

	dir := t.TempDir()
	var paths [3]string
	for c := range paths {
		paths[c] = filepath.Join(dir, []string{"r", "g", "b"}[c]+".bin")
		writeStarTreatmentArtifact(t, paths[c], shifted[c], w, h)
	}
	n2, _ := NewStarNeutralizer(model, DiskChannel{Image: *imgs[0], OffsetX: -6, OffsetY: 3}, *imgs[1], w, h, settings)
	if err := n2.ApplyDisk(context.Background(), paths); err != nil {
		t.Fatal(err)
	}
	for c := range paths {
		disk := readStarTreatmentArtifact(t, paths[c], w, h)
		for i := range disk {
			if math.Abs(float64(disk[i]-mem[c][i])) > 1e-6 {
				t.Fatalf("disk whitening differs at channel %d pixel %d: %v vs %v", c, i, disk[i], mem[c][i])
			}
		}
	}
}
