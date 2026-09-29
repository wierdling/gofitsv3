package processing

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"testing"

	"gofitsv3/internal/models"
)

func stretchedNeutralizerCase(t *testing.T) ([3][]float32, *StarNeutralizer, float64, float64, float64) {
	t.Helper()
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
	return neutralizePlanes(imgs), n, cx, cy, StarTreatmentExtent(model.Fits()[0])
}

func radialIndex(w int, cx, cy, d int) int { return int(cy)*w + int(cx) + d }

func TestStarNeutralizerMapsBroadStretchedHaloWithoutFootprintRing(t *testing.T) {
	planes, n, cx, cy, extent := stretchedNeutralizerCase(t)
	const w = 90
	before := [3][]float32{append([]float32(nil), planes[0]...), append([]float32(nil), planes[1]...), append([]float32(nil), planes[2]...)}

	// This broad, symmetric red wing exists in the current stretched RGB
	// planes, while the prepared reference fit still ends at extent.
	for y := 0; y < 80; y++ {
		for x := 0; x < w; x++ {
			d := math.Hypot(float64(x)-cx, float64(y)-cy)
			wing := 0.10 * math.Exp(-0.5*math.Pow((d-(extent+2))/3.0, 2))
			planes[0][y*w+x] += float32(wing)
		}
	}
	if err := n.Apply(context.Background(), planes); err != nil {
		t.Fatal(err)
	}

	// A pixel in the broad wing beyond the reference footprint must be
	// whitened on the weak channels.
	wing := radialIndex(w, int(cx), int(cy), int(math.Round(extent+5)))
	if planes[1][wing] <= before[1][wing]+0.002 || planes[2][wing] <= before[2][wing]+0.002 {
		t.Fatalf("stretched halo outside reference footprint was not whitened: G %.4f -> %.4f, B %.4f -> %.4f", before[1][wing], planes[1][wing], before[2][wing], planes[2][wing])
	}

	// The correction must cross the old footprint boundary smoothly instead of
	// creating a bright ring where the static fit ended.
	inside := radialIndex(w, int(cx), int(cy), int(math.Floor(extent)))
	outside := radialIndex(w, int(cx), int(cy), int(math.Ceil(extent))+1)
	deltaInside := float64(planes[1][inside] - before[1][inside])
	deltaOutside := float64(planes[1][outside] - before[1][outside])
	if deltaOutside > deltaInside*2+0.01 {
		t.Fatalf("correction has a ring at old footprint edge: dInside=%.4f dOutside=%.4f", deltaInside, deltaOutside)
	}

	// A colored background well outside the wing is preserved.
	far := radialIndex(w, int(cx), int(cy), 30)
	for c := range planes {
		if math.Abs(float64(planes[c][far]-before[c][far])) > 1e-6 {
			t.Fatalf("background changed outside mapped footprint in channel %d: %.6f -> %.6f", c, before[c][far], planes[c][far])
		}
	}
}

func TestStarNeutralizerDoesNotGrowIntoOneSidedNebularFeature(t *testing.T) {
	planes, n, cx, cy, extent := stretchedNeutralizerCase(t)
	const w = 90
	before := [3][]float32{append([]float32(nil), planes[0]...), append([]float32(nil), planes[1]...), append([]float32(nil), planes[2]...)}
	// A red-only feature on one side of the star is intentionally not a star
	// halo: fewer than three sectors support extending the footprint.
	featureD := int(math.Ceil(extent)) + 5
	for y := int(cy) - 1; y <= int(cy)+1; y++ {
		planes[0][y*w+int(cx)+featureD] += 0.14
	}
	if err := n.Apply(context.Background(), planes); err != nil {
		t.Fatal(err)
	}
	for y := int(cy) - 1; y <= int(cy)+1; y++ {
		idx := y*w + int(cx) + featureD
		if math.Abs(float64(planes[1][idx]-before[1][idx])) > 1e-6 || math.Abs(float64(planes[2][idx]-before[2][idx])) > 1e-6 {
			t.Fatalf("one-sided feature was included in whitening at (%d,%d): G %.5f -> %.5f B %.5f -> %.5f", int(cx)+featureD, y, before[1][idx], planes[1][idx], before[2][idx], planes[2][idx])
		}
	}
}

func TestStarNeutralizerMapsMonotonicBroadGaussianHalo(t *testing.T) {
	planes, n, cx, cy, extent := stretchedNeutralizerCase(t)
	const w, h = 90, 80
	// The wing is centered on the star and decreases monotonically with
	// radius. It therefore cannot be mistaken for a detached ring or a
	// one-sided structure.
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			d := math.Hypot(float64(x)-cx, float64(y)-cy)
			planes[0][y*w+x] += float32(0.12 * math.Exp(-0.5*math.Pow(d/6.0, 2)))
		}
	}
	before := [3][]float32{append([]float32(nil), planes[0]...), append([]float32(nil), planes[1]...), append([]float32(nil), planes[2]...)}
	if err := n.Apply(context.Background(), planes); err != nil {
		t.Fatal(err)
	}
	oldEdge := radialIndex(w, int(cx), int(cy), int(math.Ceil(extent))+1)
	farWing := radialIndex(w, int(cx), int(cy), int(math.Ceil(extent))+3)
	for name, idx := range map[string]int{"old footprint edge": oldEdge, "far wing": farWing} {
		beforeExcess := float64(before[0][idx] - before[2][idx])
		afterExcess := float64(planes[0][idx] - planes[2][idx])
		if beforeExcess-afterExcess < 0.001 {
			t.Fatalf("%s red excess was not meaningfully reduced: before %.4f after %.4f", name, beforeExcess, afterExcess)
		}
	}
	// The broad wing ends well before this background sample.
	background := radialIndex(w, int(cx), int(cy), 30)
	for c := range planes {
		if math.Abs(float64(planes[c][background]-before[c][background])) > 1e-6 {
			t.Fatalf("background changed in channel %d: %.6f -> %.6f", c, before[c][background], planes[c][background])
		}
	}
}

func TestStarNeutralizerNoiseDoesNotGrowMappedFootprint(t *testing.T) {
	planes, n, cx, cy, extent := stretchedNeutralizerCase(t)
	const w, h = 90, 80
	// Deterministic, independent RGB noise at roughly 0.01-0.02 amplitude
	// surrounds the narrow star. Positive max-channel excursions must not be
	// treated as coherent stellar wings.
	seed := uint32(0x13579bdf)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			d := math.Hypot(float64(x)-cx, float64(y)-cy)
			if d <= extent+1 {
				continue
			}
			for c := 0; c < 3; c++ {
				seed = seed*1664525 + 1013904223
				noise := (float64(seed>>8)/float64(1<<24))*0.03 - 0.015
				planes[c][y*w+x] += float32(noise)
			}
		}
	}
	before := [3][]float32{append([]float32(nil), planes[0]...), append([]float32(nil), planes[1]...), append([]float32(nil), planes[2]...)}
	if err := n.Apply(context.Background(), planes); err != nil {
		t.Fatal(err)
	}
	// A sample beyond the reference footprint remains byte-for-byte unchanged
	// when no coherent halo is present.
	idx := radialIndex(w, int(cx), int(cy), int(math.Ceil(extent))+3)
	for c := range planes {
		if planes[c][idx] != before[c][idx] {
			t.Fatalf("noise widened mapped footprint in channel %d at radius %.1f: %.7f -> %.7f", c, float64(math.Ceil(extent)+3), before[c][idx], planes[c][idx])
		}
	}
}

func TestStarNeutralizerResetsMappedFootprintBetweenApplies(t *testing.T) {
	base, reused, cx, cy, extent := stretchedNeutralizerCase(t)
	const w, h = 90, 80
	addHalo := func(planes [3][]float32) [3][]float32 {
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				d := math.Hypot(float64(x)-cx, float64(y)-cy)
				planes[0][y*w+x] += float32(0.10 * math.Exp(-0.5*math.Pow((d-(extent+2))/3.0, 2)))
			}
		}
		return planes
	}
	clone := func(src [3][]float32) [3][]float32 {
		return [3][]float32{append([]float32(nil), src[0]...), append([]float32(nil), src[1]...), append([]float32(nil), src[2]...)}
	}
	expanded := addHalo(clone(base))
	if err := reused.Apply(context.Background(), expanded); err != nil {
		t.Fatal(err)
	}
	narrow := clone(base)
	if err := reused.Apply(context.Background(), narrow); err != nil {
		t.Fatal(err)
	}
	expandedAgain := addHalo(clone(base))
	if err := reused.Apply(context.Background(), expandedAgain); err != nil {
		t.Fatal(err)
	}

	freshBase, fresh, _, _, _ := stretchedNeutralizerCase(t)
	expandedFresh := addHalo(clone(freshBase))
	if err := fresh.Apply(context.Background(), expandedFresh); err != nil {
		t.Fatal(err)
	}
	for c := range expandedAgain {
		for i := range expandedAgain[c] {
			if math.Abs(float64(expandedAgain[c][i]-expandedFresh[c][i])) > 1e-6 {
				t.Fatalf("reused neutralizer differs from fresh after expanded-narrow-expanded sequence at channel %d pixel %d: %.7f vs %.7f", c, i, expandedAgain[c][i], expandedFresh[c][i])
			}
		}
	}
}

func TestStarNeutralizerNonuniformScaleMapsMajorAxisAndMatchesDisk(t *testing.T) {
	const srcW, h = 90, 80
	imgs, fits := neutralizeScene(t, srcW, h, 45, 40)
	model, err := NewStarTreatmentModel(fits, srcW, h, *imgs[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	const w = 180
	settings := models.StarWhiteningState{Enabled: true, Strength: 1, Level: models.StarWhiteningWhite, Red: true, Green: true, Blue: true}
	n, err := NewStarNeutralizer(model, DiskChannel{Image: *imgs[0]}, *imgs[1], w, h, settings)
	if err != nil {
		t.Fatal(err)
	}
	src := neutralizePlanes(imgs)
	planes := [3][]float32{make([]float32, w*h), make([]float32, w*h), make([]float32, w*h)}
	for c := range planes {
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				planes[c][y*w+x] = src[c][y*srcW+x/2]
			}
		}
	}
	// Add a red wing along the output major axis only. Its source-grid
	// equivalent is broad enough to be mapped, while the minor axis remains a
	// clean background outside the fitted footprint.
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx := math.Abs(float64(x - 90))
			dy := math.Abs(float64(y - 40))
			wing := 0.12 * math.Exp(-0.5*math.Pow((dx-19)/6.0, 2)) * math.Exp(-0.5*math.Pow(dy/8.0, 2))
			planes[0][y*w+x] += float32(wing)
		}
	}
	before := [3][]float32{append([]float32(nil), planes[0]...), append([]float32(nil), planes[1]...), append([]float32(nil), planes[2]...)}
	if err := n.Apply(context.Background(), planes); err != nil {
		t.Fatal(err)
	}
	major := 40*w + 90 + 24
	if planes[1][major] <= before[1][major]+0.002 || planes[2][major] <= before[2][major]+0.002 {
		t.Fatalf("major-axis halo was not whitened: G %.5f -> %.5f, B %.5f -> %.5f", before[1][major], planes[1][major], before[2][major], planes[2][major])
	}
	minor := (40+24)*w + 90
	for c := range planes {
		if math.Abs(float64(planes[c][minor]-before[c][minor])) > 1e-6 {
			t.Fatalf("unfeatured minor-axis background changed in channel %d: %.7f -> %.7f", c, before[c][minor], planes[c][minor])
		}
	}

	dir := t.TempDir()
	var paths [3]string
	for c := range paths {
		paths[c] = filepath.Join(dir, string(rune('r'+c))+".bin")
		writeStarTreatmentArtifact(t, paths[c], before[c], w, h)
	}
	if err := n.ApplyDisk(context.Background(), paths); err != nil {
		t.Fatal(err)
	}
	for c := range paths {
		disk := readStarTreatmentArtifact(t, paths[c], w, h)
		for i := range disk {
			if math.Abs(float64(disk[i]-planes[c][i])) > 1e-6 {
				t.Fatalf("nonuniform ApplyDisk differs from Apply at channel %d pixel %d: %.7f vs %.7f", c, i, disk[i], planes[c][i])
			}
		}
	}
}

func TestStarNeutralizerMappedHaloApplyAndDiskAgree(t *testing.T) {
	planes, n, cx, cy, extent := stretchedNeutralizerCase(t)
	const w, h = 90, 80
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			d := math.Hypot(float64(x)-cx, float64(y)-cy)
			planes[0][y*w+x] += float32(0.10 * math.Exp(-0.5*math.Pow((d-(extent+2))/3.0, 2)))
		}
	}
	mem := [3][]float32{append([]float32(nil), planes[0]...), append([]float32(nil), planes[1]...), append([]float32(nil), planes[2]...)}
	if err := n.Apply(context.Background(), mem); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	var paths [3]string
	for c := range paths {
		paths[c] = filepath.Join(dir, string(rune('r'+c))+".bin")
		writeStarTreatmentArtifact(t, paths[c], planes[c], w, h)
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
}

func TestStarNeutralizerApplyAndDiskHonorCancellation(t *testing.T) {
	planes, n, _, _, _ := stretchedNeutralizerCase(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := n.Apply(ctx, planes); !errors.Is(err, context.Canceled) {
		t.Fatalf("Apply error = %v, want context.Canceled", err)
	}

	dir := t.TempDir()
	var paths [3]string
	for c := range paths {
		paths[c] = filepath.Join(dir, string(rune('r'+c))+".bin")
		writeStarTreatmentArtifact(t, paths[c], planes[c], 90, 80)
	}
	if err := n.ApplyDisk(ctx, paths); !errors.Is(err, context.Canceled) {
		t.Fatalf("ApplyDisk error = %v, want context.Canceled", err)
	}
}

func TestStarNeutralizerDetectsPairedRGBSpikesBeyondReferenceFootprint(t *testing.T) {
	const w, h, cx, cy = 180, 160, 90, 80
	imgs, fits := neutralizeScene(t, w, h, cx, cy)
	// Make this a large source so the runtime search receives the larger
	// bounded extent. The reference fit intentionally has no spike geometry.
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
	planes := neutralizePlanes(imgs)
	before := [3][]float32{append([]float32(nil), planes[0]...), append([]float32(nil), planes[1]...), append([]float32(nil), planes[2]...)}
	// Two narrow opposing red arms start outside the reference footprint and
	// continue beyond the old 2x search limit. Their diagonal orientation also
	// ensures the detector is using angular rather than quadrant-wide support.
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			const angle = 23 * math.Pi / 180
			along := math.Abs(dx*math.Cos(angle) + dy*math.Sin(angle))
			across := math.Abs(-dx*math.Sin(angle) + dy*math.Cos(angle))
			arm := .16 * math.Exp(-.5*math.Pow((along-27)/8, 2)) * math.Exp(-.5*math.Pow(across/2.0, 2))
			if arm > 0 {
				planes[0][y*w+x] += float32(arm)
			}
		}
	}
	if err := n.Apply(context.Background(), planes); err != nil {
		t.Fatal(err)
	}
	if len(n.stars[0].spikes) < 2 || len(n.stars[0].spikes)%2 != 0 {
		t.Fatalf("expected paired runtime spike extensions, got %d arms", len(n.stars[0].spikes))
	}
	// Radius 34 is beyond the old 2x source search limit (28).
	idx := int(cy+13)*w + int(cx+31)
	if planes[1][idx] <= before[1][idx]+.002 || planes[2][idx] <= before[2][idx]+.002 {
		t.Fatalf("paired diagonal RGB spike was not whitened: G %.5f -> %.5f, B %.5f -> %.5f", before[1][idx], planes[1][idx], before[2][idx], planes[2][idx])
	}
	// A perpendicular location at the same radius remains background.
	far := int(cy-31)*w + int(cx+13)
	for c := range planes {
		if math.Abs(float64(planes[c][far]-before[c][far])) > 1e-6 {
			t.Fatalf("perpendicular background changed in channel %d: %.6f -> %.6f", c, before[c][far], planes[c][far])
		}
	}
}
