package processing

import (
	"context"
	"math"
	"path/filepath"
	"testing"

	"gofitsv3/internal/models"
)

func independentSpikeNeutralizer(t *testing.T, w, h int, cx, cy float64) ([3][]float32, *StarNeutralizer, []StarTreatmentFit) {
	t.Helper()
	imgs, fits := neutralizeScene(t, w, h, cx, cy)
	// Keep the prepared reference footprint deliberately smaller than the
	// runtime search area. The added RGB structure is therefore the only way
	// these tests can exercise current-pass spike detection.
	fits[0].OuterRadius = 14
	fits[0].InnerRadius = 8
	model, err := NewStarTreatmentModel(fits, w, h, *imgs[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	settings := models.StarWhiteningState{Enabled: true, Strength: 1, Level: models.StarWhiteningWhite, Red: true, Green: true, Blue: true}
	n, err := NewStarNeutralizer(model, DiskChannel{Image: *imgs[0]}, *imgs[1], w, h, settings)
	if err != nil {
		t.Fatal(err)
	}
	return neutralizePlanes(imgs), n, fits
}

func TestStarNeutralizerDetectsFourRotatedSpikeArms(t *testing.T) {
	const w, h, cx, cy = 180, 160, 90., 80.
	planes, n, _ := independentSpikeNeutralizer(t, w, h, cx, cy)
	before := [3][]float32{append([]float32(nil), planes[0]...), append([]float32(nil), planes[1]...), append([]float32(nil), planes[2]...)}
	const theta = 23 * math.Pi / 180
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			for _, angle := range []float64{theta, theta + math.Pi/2} {
				along := math.Abs(dx*math.Cos(angle) + dy*math.Sin(angle))
				across := math.Abs(-dx*math.Sin(angle) + dy*math.Cos(angle))
				planes[0][y*w+x] += float32(.16 * math.Exp(-.5*math.Pow((along-27)/8, 2)) * math.Exp(-.5*math.Pow(across/2, 2)))
			}
		}
	}
	if err := n.Apply(context.Background(), planes); err != nil {
		t.Fatal(err)
	}
	if len(n.stars[0].spikes) != 4 {
		t.Fatalf("expected four paired rotated spike arms, got %d: %+v", len(n.stars[0].spikes), n.stars[0].spikes)
	}
	for _, angle := range []float64{theta, theta + math.Pi/2} {
		for _, sign := range []float64{-1, 1} {
			x := int(math.Round(cx + sign*34*math.Cos(angle)))
			y := int(math.Round(cy + sign*34*math.Sin(angle)))
			idx := y*w + x
			if planes[1][idx] <= before[1][idx]+.002 || planes[2][idx] <= before[2][idx]+.002 {
				t.Fatalf("rotated spike at (%d,%d) was not whitened: G %.5f -> %.5f, B %.5f -> %.5f", x, y, before[1][idx], planes[1][idx], before[2][idx], planes[2][idx])
			}
		}
	}
}

func TestStarNeutralizerRejectsDetachedOuterRing(t *testing.T) {
	const w, h, cx, cy = 180, 160, 90., 80.
	planes, n, _ := independentSpikeNeutralizer(t, w, h, cx, cy)
	// An isolated ring separated from the reference footprint by quiet bins
	// must not be captured just because it lies inside the enlarged search.
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			d := math.Hypot(float64(x)-cx, float64(y)-cy)
			planes[0][y*w+x] += float32(.12 * math.Exp(-.5*math.Pow((d-34)/5, 2)))
		}
	}
	before := [3][]float32{append([]float32(nil), planes[0]...), append([]float32(nil), planes[1]...), append([]float32(nil), planes[2]...)}
	if err := n.Apply(context.Background(), planes); err != nil {
		t.Fatal(err)
	}
	idx := int(cy)*w + int(cx) + 34
	if planes[1][idx] != before[1][idx] || planes[2][idx] != before[2][idx] {
		t.Fatal("detached outer ring was captured as a halo")
	}
	far := int(cy)*w + int(cx) + 55
	for c := range planes {
		if math.Abs(float64(planes[c][far]-before[c][far])) > 1e-6 {
			t.Fatalf("far background changed in channel %d: %.7f -> %.7f", c, before[c][far], planes[c][far])
		}
	}
}

func TestStarNeutralizerRejectsPairedDisconnectedKnots(t *testing.T) {
	const w, h, cx, cy = 180, 160, 90., 80.
	planes, n, _ := independentSpikeNeutralizer(t, w, h, cx, cy)
	before := [3][]float32{append([]float32(nil), planes[0]...), append([]float32(nil), planes[1]...), append([]float32(nil), planes[2]...)}
	const angle = 31 * math.Pi / 180
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			along := dx*math.Cos(angle) + dy*math.Sin(angle)
			across := math.Abs(-dx*math.Sin(angle) + dy*math.Cos(angle))
			knot := math.Exp(-.5*math.Pow((math.Abs(along)-27)/.65, 2)) * math.Exp(-.5*math.Pow(across/1.5, 2))
			planes[0][y*w+x] += float32(.20 * knot)
		}
	}
	if err := n.Apply(context.Background(), planes); err != nil {
		t.Fatal(err)
	}
	if len(n.stars[0].spikes) != 0 {
		t.Fatalf("disconnected opposing knots were accepted as spikes: %+v", n.stars[0].spikes)
	}
	for _, sign := range []float64{-1, 1} {
		x := int(math.Round(cx + sign*27*math.Cos(angle)))
		y := int(math.Round(cy + sign*27*math.Sin(angle)))
		idx := y*w + x
		if planes[1][idx] != before[1][idx] || planes[2][idx] != before[2][idx] {
			t.Fatalf("disconnected knot at (%d,%d) was whitened", x, y)
		}
	}
}

func TestStarNeutralizerBoundsSpikeSearchByNeighbor(t *testing.T) {
	const w, h, cx, cy = 140, 80, 45., 40.
	planes, n, fits := independentSpikeNeutralizer(t, w, h, cx, cy)
	// A nearby catalog star limits the first star's runtime search to half the
	// separation. The first star must not whiten a red arm through that neighbor.
	neighbor := fits[0]
	neighbor.SourceID = 2
	neighbor.X = 70
	fits = append(fits, neighbor)
	// Rebuild with the original metadata, retaining the same deterministic
	// planes and settings while injecting the neighboring fit.
	imgs, _ := neutralizeScene(t, w, h, cx, cy)
	model, err := NewStarTreatmentModel(fits, w, h, *imgs[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	settings := models.StarWhiteningState{Enabled: true, Strength: 1, Level: models.StarWhiteningWhite, Red: true, Green: true, Blue: true}
	n, err = NewStarNeutralizer(model, DiskChannel{Image: *imgs[0]}, *imgs[1], w, h, settings)
	if err != nil {
		t.Fatal(err)
	}
	before := [3][]float32{append([]float32(nil), planes[0]...), append([]float32(nil), planes[1]...), append([]float32(nil), planes[2]...)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// Put the test arm on the side away from the neighbor so the
			// neighboring fit cannot itself own the assertion sample.
			dx, dy := float64(x)-cx, float64(y)-cy
			arm := .18 * math.Exp(-.5*math.Pow((dx+20)/4, 2)) * math.Exp(-.5*math.Pow(dy/2, 2))
			planes[0][y*w+x] += float32(arm)
		}
	}
	if err := n.Apply(context.Background(), planes); err != nil {
		t.Fatal(err)
	}
	idx := int(cy)*w + int(cx) - 20
	for c := 1; c < 3; c++ {
		if planes[c][idx] != before[c][idx] {
			t.Fatalf("neighbor-bounded arm changed channel %d at radius 20: %.6f -> %.6f", c, before[c][idx], planes[c][idx])
		}
	}
}

func TestStarNeutralizerSpikeApplyDiskParityAndReset(t *testing.T) {
	const w, h, cx, cy = 180, 160, 90., 80.
	planes, n, _ := independentSpikeNeutralizer(t, w, h, cx, cy)
	const theta = 23 * math.Pi / 180
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			for _, angle := range []float64{theta, theta + math.Pi/2} {
				along := math.Abs(dx*math.Cos(angle) + dy*math.Sin(angle))
				across := math.Abs(-dx*math.Sin(angle) + dy*math.Cos(angle))
				planes[0][y*w+x] += float32(.16 * math.Exp(-.5*math.Pow((along-27)/8, 2)) * math.Exp(-.5*math.Pow(across/2, 2)))
			}
		}
	}
	input := [3][]float32{append([]float32(nil), planes[0]...), append([]float32(nil), planes[1]...), append([]float32(nil), planes[2]...)}
	mem := [3][]float32{append([]float32(nil), input[0]...), append([]float32(nil), input[1]...), append([]float32(nil), input[2]...)}
	if err := n.Apply(context.Background(), mem); err != nil {
		t.Fatal(err)
	}
	if len(n.stars[0].spikes) == 0 {
		t.Fatal("expected runtime spikes in memory apply")
	}

	dir := t.TempDir()
	var paths [3]string
	for c := range paths {
		paths[c] = filepath.Join(dir, string(rune('r'+c))+".bin")
		writeStarTreatmentArtifact(t, paths[c], input[c], w, h)
	}
	if err := n.ApplyDisk(context.Background(), paths); err != nil {
		t.Fatal(err)
	}
	for c := range paths {
		disk := readStarTreatmentArtifact(t, paths[c], w, h)
		for i := range disk {
			if math.Abs(float64(disk[i]-mem[c][i])) > 1e-6 {
				t.Fatalf("ApplyDisk differs from Apply at channel %d pixel %d: %.7f vs %.7f", c, i, disk[i], mem[c][i])
			}
		}
	}

	base := neutralizePlanes(func() [3]*models.LoadedImage {
		imgs, _ := neutralizeScene(t, w, h, cx, cy)
		return imgs
	}())
	if err := n.Apply(context.Background(), base); err != nil {
		t.Fatal(err)
	}
	if len(n.stars[0].spikes) != 0 {
		t.Fatalf("runtime spike extensions survived a later no-spike apply: %+v", n.stars[0].spikes)
	}
}

func TestStarNeutralizerLargeHaloBeyondOldSearch(t *testing.T) {
	const w, h, cx, cy = 180, 160, 90., 80.
	planes, n, _ := independentSpikeNeutralizer(t, w, h, cx, cy)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r := math.Hypot(float64(x)-cx, float64(y)-cy)
			planes[0][y*w+x] += float32(.15 * math.Exp(-.5*math.Pow(r/15, 2)))
		}
	}
	wing, far := int(cy)*w+int(cx)+34, int(cy)*w+int(cx)+65
	beforeWing := planes[1][wing]
	beforeFar := [3]float32{planes[0][far], planes[1][far], planes[2][far]}
	if err := n.Apply(context.Background(), planes); err != nil {
		t.Fatal(err)
	}
	if planes[1][wing] <= beforeWing+.005 {
		t.Fatalf("large halo beyond old radius 28 was not corrected: %.6f -> %.6f", beforeWing, planes[1][wing])
	}
	for c := range planes {
		if planes[c][far] != beforeFar[c] {
			t.Fatalf("background outside large halo changed in channel %d", c)
		}
	}
}

func TestStarNeutralizerDoesNotTreatUnusableFit(t *testing.T) {
	const w, h = 90, 80
	imgs, fits := neutralizeScene(t, w, h, 45, 40)
	fits[0].Usable = false
	model, err := NewStarTreatmentModel(fits, w, h, *imgs[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	n, err := NewStarNeutralizer(model, DiskChannel{Image: *imgs[0]}, *imgs[1], w, h,
		models.StarWhiteningState{Enabled: true, Strength: 1, Red: true, Green: true, Blue: true})
	if err != nil {
		t.Fatal(err)
	}
	planes, before := neutralizePlanes(imgs), neutralizePlanes(imgs)
	if err := n.Apply(context.Background(), planes); err != nil {
		t.Fatal(err)
	}
	for c := range planes {
		for i := range planes[c] {
			if planes[c][i] != before[c][i] {
				t.Fatalf("unusable star changed channel %d pixel %d", c, i)
			}
		}
	}
}
