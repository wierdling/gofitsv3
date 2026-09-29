package processing

import (
	"context"
	"math"
	"testing"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/stretch"
)

func extendedValidationMeta(w, h int) models.LoadedImage {
	return models.LoadedImage{
		HDU:  fitsio.HDU{Data: fitsio.ImageData{Width: w, Height: h}},
		Mode: stretch.Linear, Background: 0, Peak: 2,
		ScaledPeak: 1, AsinhScale: 1, MTFMidtone: .5,
	}
}

// errAfterChecks makes cancellation deterministic without depending on a
// scheduler racing the footprint scan.
type errAfterChecks struct {
	context.Context
	limit, checks int
}

func (c *errAfterChecks) Err() error {
	c.checks++
	if c.checks >= c.limit {
		return context.Canceled
	}
	return nil
}

func runExtendedValidationPipeline(t *testing.T, pixels []float32, w, h int, m *StarMap, strength float64) ([]StarTreatmentFit, []float32, []float32, error) {
	t.Helper()
	meta := extendedValidationMeta(w, h)
	fits, err := FitStarTreatment(context.Background(), m, pixels, w, h, StarTreatmentOptions{})
	if err != nil {
		return nil, nil, nil, err
	}
	prepared, err := PrepareStarStretchFits(context.Background(), pixels, w, h, meta, fits, m.Sources)
	if err != nil {
		return nil, nil, nil, err
	}
	mask, err := BuildStarTreatmentMask(context.Background(), prepared, w, h)
	if err != nil {
		return nil, nil, nil, err
	}
	rendered, err := ApplyGentlerStarStretch(context.Background(), pixels, w, h, meta, prepared, mask, StarStretchOptions{Strength: strength})
	return prepared, mask, rendered, err
}

func TestStarStretchExtendedValidationSubpixelBroadHaloHasSmoothSupport(t *testing.T) {
	const w, h = 101, 101
	const cx, cy = 49.35, 50.65
	pixels := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			background := .24 + .002*dx - .0015*dy
			halo := 2.7 * math.Exp(-.5*(dx*dx+dy*dy)/(4.2*4.2))
			pixels[y*w+x] = float32(background + halo)
		}
	}
	m := &StarMap{Width: w, Height: h, Sources: []StarMapSource{{ID: 301, X: cx, Y: cy, FWHM: 5.5, Radius: 7, Status: "accepted"}}}
	fits, mask, rendered, err := runExtendedValidationPipeline(t, pixels, w, h, m, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(fits) != 1 || !fits[0].Usable {
		t.Fatalf("subpixel broad halo was skipped: %+v", fits)
	}
	if fits[0].OuterRadius <= 1.25*m.Sources[0].Radius {
		t.Fatalf("halo support did not extend beyond catalog radius: %+v", fits[0])
	}
	// The correction must taper continuously, including between pixel samples,
	// without a ring or hollow center along a diagonal through the source.
	centerCorrection := 0.0
	for r := 0; r <= int(math.Floor(fits[0].OuterRadius)); r++ {
		x := int(math.Round(cx + float64(r)/math.Sqrt2))
		y := int(math.Round(cy + float64(r)/math.Sqrt2))
		if x < 0 || x >= w || y < 0 || y >= h {
			continue
		}
		i := y*w + x
		correction := float64(DiskStretchPreviewValue(pixels[i], extendedValidationMeta(w, h)) - rendered[i])
		if r == 0 {
			centerCorrection = correction
		}
		if correction > centerCorrection+0.08 {
			t.Fatalf("halo correction has a ring at r=%d: center=%g current=%g fit=%+v", r, centerCorrection, correction, fits[0])
		}
	}
	boundary := int(math.Round(cy))*w + int(math.Round(cx+StarTreatmentExtent(fits[0])+2))
	if mask[boundary] != 0 {
		t.Fatalf("mask did not end outside measured halo: extent=%v mask=%v", StarTreatmentExtent(fits[0]), mask[boundary])
	}
}

func TestStarStretchExtendedValidationExtendsFaintHaloPastLegacyCap(t *testing.T) {
	const w, h = 385, 385
	const cx, cy = 192.35, 191.65
	pixels := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			background := .2 + .0004*dx - .0003*dy
			core := 6.0 * math.Exp(-.5*(dx*dx+dy*dy)/(1.45*1.45))
			halo := .55 * math.Exp(-.5*(dx*dx+dy*dy)/(20*20))
			pixels[y*w+x] = float32(background + core + halo)
		}
	}
	m := &StarMap{Width: w, Height: h, Sources: []StarMapSource{{ID: 306, X: cx, Y: cy, FWHM: 3.4, Radius: 6, Status: "accepted"}}}
	meta := extendedValidationMeta(w, h)
	fits, err := FitStarTreatment(context.Background(), m, pixels, w, h, StarTreatmentOptions{})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareStarStretchFits(context.Background(), pixels, w, h, meta, fits, m.Sources)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared) != 1 || !prepared[0].Usable {
		t.Fatalf("compact star with faint halo was skipped: %+v", prepared)
	}
	if StarTreatmentExtent(prepared[0]) <= 32 {
		t.Fatalf("faint halo remained bounded by the legacy 32-pixel cap: %+v", prepared[0])
	}
	mask, err := BuildStarTreatmentMask(context.Background(), prepared, w, h)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := ApplyGentlerStarStretch(context.Background(), pixels, w, h, meta, prepared, mask, StarStretchOptions{Strength: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []int{34, 42, 50} {
		x, y := int(math.Round(cx))+r, int(math.Round(cy))
		i := y*w + x
		normal := DiskStretchPreviewValue(pixels[i], meta)
		if mask[i] == 0 || float64(normal-rendered[i]) <= 1e-5 {
			t.Fatalf("faint halo at radius %d was not treated: mask=%g correction=%g fit=%+v", r, mask[i], float64(normal-rendered[i]), prepared[0])
		}
	}
	for _, r := range []int{33, 34, 35, 42, 50, 58} {
		x, y := int(math.Round(cx))+r, int(math.Round(cy))
		if x >= w {
			continue
		}
		i := y*w + x
		correction := float64(DiskStretchPreviewValue(pixels[i], meta) - rendered[i])
		if correction < 0 || correction > .1 {
			t.Fatalf("halo correction was not bounded at radius %d: %g", r, correction)
		}
	}
}

func TestStarStretchExtendedValidationCrossedRotatedSpikesPreserveOffSpikeBackground(t *testing.T) {
	const w, h = 201, 201
	const cx, cy = 100.25, 99.7
	pixels := make([]float32, w*h)
	angles := []float64{.11, .11 + math.Pi/2}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			background := .18 + .0012*dx + .0008*dy
			star := 5.2 * math.Exp(-.5*(dx*dx+dy*dy)/(1.35*1.35))
			spikes := 0.0
			for n, angle := range angles {
				ca, sa := math.Cos(angle), math.Sin(angle)
				along := dx*ca + dy*sa
				across := -dx*sa + dy*ca
				amplitude := .62 - .08*float64(n)
				fade := math.Max(0, 1-math.Abs(along)/75)
				spikes += amplitude * fade * fade * math.Exp(-.5*across*across/.42)
			}
			pixels[y*w+x] = float32(background + star + spikes)
		}
	}
	m := &StarMap{Width: w, Height: h, Sources: []StarMapSource{{ID: 302, X: cx, Y: cy, FWHM: 3.2, Radius: 6, Status: "accepted"}}}
	fits, mask, rendered, err := runExtendedValidationPipeline(t, pixels, w, h, m, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(fits) != 1 || !fits[0].Usable {
		t.Fatalf("clean crossed spikes were skipped: %+v", fits)
	}
	if StarTreatmentExtent(fits[0]) <= 60 {
		t.Fatalf("crossed spikes did not produce extended support: %+v", fits[0])
	}
	meta := extendedValidationMeta(w, h)
	for n, angle := range angles {
		ca, sa := math.Cos(angle), math.Sin(angle)
		for _, r := range []float64{40, 60} {
			x := int(math.Round(cx + r*ca))
			y := int(math.Round(cy + r*sa))
			if x < 0 || x >= w || y < 0 || y >= h {
				continue
			}
			i := y*w + x
			normal := DiskStretchPreviewValue(pixels[i], meta)
			if mask[i] == 0 || float64(normal-rendered[i]) <= 1e-5 {
				t.Fatalf("rotated spike %d at radius %.1f was not treated: mask=%g correction=%g fit=%+v", n, r, mask[i], float64(normal-rendered[i]), fits[0])
			}
		}
	}
	// A point between the narrow arms has no meaningful stellar increment. Any
	// correction there must stay within the known synthetic stellar contribution.
	x, y := int(math.Round(cx+45)), int(math.Round(cy+20))
	i := y*w + x
	normal := DiskStretchPreviewValue(pixels[i], meta)
	dx, dy := float64(x)-cx, float64(y)-cy
	oracleBackground := .18 + .0012*dx + .0008*dy
	oracle := float64(normal - DiskStretchPreviewValue(float32(oracleBackground), meta))
	if correction := float64(normal - rendered[i]); correction > 1.5*oracle+0.002 {
		t.Fatalf("off-spike background leaked into correction at (%d,%d): correction=%g oracle=%g mask=%g", x, y, correction, oracle, mask[i])
	}
}

func TestStarStretchExtendedValidationDoesNotValidateOneSidedRidgeAsSpikes(t *testing.T) {
	const w, h = 201, 201
	const cx, cy = 100.2, 99.6
	pixels := make([]float32, w*h)
	angle := .43
	ca, sa := math.Cos(angle), math.Sin(angle)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			background := .2 + .0006*dx - .0004*dy
			core := 5.0 * math.Exp(-.5*(dx*dx+dy*dy)/(1.35*1.35))
			along := dx*ca + dy*sa
			across := -dx*sa + dy*ca
			ridge := .65 * math.Max(0, 1-along/70) * math.Exp(-.5*across*across/.5) * boolFloat(along > 4)
			pixels[y*w+x] = float32(background + core + ridge)
		}
	}
	m := &StarMap{Width: w, Height: h, Sources: []StarMapSource{{ID: 307, X: cx, Y: cy, FWHM: 3.2, Radius: 6, Status: "accepted"}}}
	fits, err := FitStarTreatment(context.Background(), m, pixels, w, h, StarTreatmentOptions{})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareStarStretchFits(context.Background(), pixels, w, h, extendedValidationMeta(w, h), fits, m.Sources)
	if err != nil {
		return // explicit safe rejection is an accepted outcome
	}
	if len(prepared) != 1 {
		t.Fatalf("unexpected fit count: %d", len(prepared))
	}
	if prepared[0].HaloValidated || len(prepared[0].Spikes) != 0 {
		t.Fatalf("one-sided ridge was claimed as extended stellar structure: %+v", prepared[0])
	}
}

func TestStarStretchExtendedValidationStopsArmAtAcceptedNeighbor(t *testing.T) {
	const w, h = 241, 241
	const cx, cy = 120.2, 119.6
	const neighborX, neighborY = 164.0, 125.0
	pixels := make([]float32, w*h)
	angles := []float64{.11, .11 + math.Pi/2}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			background := .18 + .0005*dx + .0003*dy
			value := background + 5.0*math.Exp(-.5*(dx*dx+dy*dy)/(1.35*1.35))
			for _, angle := range angles {
				ca, sa := math.Cos(angle), math.Sin(angle)
				along := dx*ca + dy*sa
				across := -dx*sa + dy*ca
				fade := math.Max(0, 1-math.Abs(along)/75)
				value += .55 * fade * fade * math.Exp(-.5*across*across/.42)
			}
			nx, ny := float64(x)-neighborX, float64(y)-neighborY
			value += 4.5 * math.Exp(-.5*(nx*nx+ny*ny)/(1.5*1.5))
			pixels[y*w+x] = float32(value)
		}
	}
	m := &StarMap{Width: w, Height: h, Sources: []StarMapSource{
		{ID: 308, X: cx, Y: cy, FWHM: 3.2, Radius: 6, Status: "accepted"},
		{ID: 309, X: neighborX, Y: neighborY, FWHM: 3.4, Radius: 7, Status: "accepted"},
	}}
	fits, err := FitStarTreatment(context.Background(), m, pixels, w, h, StarTreatmentOptions{})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareStarStretchFits(context.Background(), pixels, w, h, extendedValidationMeta(w, h), fits, m.Sources)
	if err != nil {
		return // explicit safe rejection is an accepted outcome
	}
	for _, f := range prepared {
		if f.SourceID != 308 {
			continue
		}
		for _, spike := range f.Spikes {
			if spike.EndRadius > math.Hypot(neighborX-cx, neighborY-cy)-7 {
				t.Fatalf("arm crossed accepted neighbor: %+v", f)
			}
		}
		if f.HaloValidated && f.HaloRadius > math.Hypot(neighborX-cx, neighborY-cy)-7 {
			t.Fatalf("halo crossed accepted neighbor: %+v", f)
		}
		return
	}
	t.Fatal("target source missing from prepared fits")
}

func TestStarStretchExtendedValidationRejectsArmReachingImageBoundary(t *testing.T) {
	const w, h = 201, 201
	const cx, cy = 100.2, 99.6
	pixels := make([]float32, w*h)
	angles := []float64{0, math.Pi / 2}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			value := .2 + 5.0*math.Exp(-.5*(dx*dx+dy*dy)/(1.35*1.35))
			for _, angle := range angles {
				ca, sa := math.Cos(angle), math.Sin(angle)
				along := dx*ca + dy*sa
				across := -dx*sa + dy*ca
				value += .45 * math.Exp(-.5*across*across/.42) * boolFloat(math.Abs(along) > 5)
			}
			pixels[y*w+x] = float32(value)
		}
	}
	m := &StarMap{Width: w, Height: h, Sources: []StarMapSource{{ID: 310, X: cx, Y: cy, FWHM: 3.2, Radius: 6, Status: "accepted"}}}
	fits, err := FitStarTreatment(context.Background(), m, pixels, w, h, StarTreatmentOptions{})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareStarStretchFits(context.Background(), pixels, w, h, extendedValidationMeta(w, h), fits, m.Sources)
	if err != nil {
		return // explicit safe rejection is an accepted outcome
	}
	if len(prepared) != 1 {
		t.Fatalf("unexpected fit count: %d", len(prepared))
	}
	if prepared[0].HaloValidated || len(prepared[0].Spikes) != 0 {
		t.Fatalf("boundary-reaching arm was claimed complete: %+v", prepared[0])
	}
}

func boolFloat(v bool) float64 {
	if v {
		return 1
	}
	return 0
}

func TestStarStretchExtendedValidationRejectsUnsafeBoundaryAndNonfiniteSamples(t *testing.T) {
	const w, h = 41, 41
	pixels := make([]float32, w*h)
	for i := range pixels {
		pixels[i] = .2
	}
	m := &StarMap{Width: w, Height: h, Sources: []StarMapSource{{ID: 303, X: 2.2, Y: 2.4, FWHM: 2, Radius: 5, Status: "accepted"}}}
	fits, err := FitStarTreatment(context.Background(), m, pixels, w, h, StarTreatmentOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if fits[0].Usable {
		t.Fatalf("boundary source unexpectedly usable: %+v", fits[0])
	}
	if _, err := PrepareStarStretchFits(context.Background(), pixels, w, h, extendedValidationMeta(w, h), fits, m.Sources); err != nil {
		t.Fatalf("skipped boundary source should remain safe: %v", err)
	}

	// A nonfinite sample inside a footprint keeps its ordinary rendering
	// while the rest of the star is still treated; it is never reconstructed.
	pixels[20*w+20] = float32(math.NaN())
	unsafe := StarTreatmentFit{SourceID: 304, X: 20, Y: 20, Background: .2, Signal: 2, Sigma: 1, OuterRadius: 6, Usable: true}
	mask, err := BuildStarTreatmentMask(context.Background(), []StarTreatmentFit{unsafe}, w, h)
	if err != nil {
		t.Fatal(err)
	}
	assertNonfiniteLeftAlone := func(fit StarTreatmentFit, mask []float32, index int) {
		t.Helper()
		out, err := ApplyGentlerStarStretch(context.Background(), pixels, w, h, extendedValidationMeta(w, h), []StarTreatmentFit{fit}, mask, StarStretchOptions{Strength: 1})
		if err != nil {
			t.Fatalf("nonfinite sample failed the whole star: %v", err)
		}
		if normal := DiskStretchPreviewValue(pixels[index], extendedValidationMeta(w, h)); out[index] != normal {
			t.Fatalf("nonfinite sample was altered: got %v want %v", out[index], normal)
		}
		for i := range out {
			if !starFinite(float64(out[i])) {
				t.Fatalf("rendered sample %d is nonfinite", i)
			}
		}
	}
	assertNonfiniteLeftAlone(unsafe, mask, 20*w+20)

	// The same rule applies inside a validated extended halo.
	pixels[20*w+20] = .2
	pixels[20*w+28] = float32(math.NaN())
	extended := unsafe
	extended.HaloValidated = true
	extended.HaloInnerRadius = 7
	extended.HaloRadius = 10
	extended.ExtendedNoise = .1
	extendedMask, err := BuildStarTreatmentMask(context.Background(), []StarTreatmentFit{extended}, w, h)
	if err != nil {
		t.Fatal(err)
	}
	assertNonfiniteLeftAlone(extended, extendedMask, 20*w+28)
}

func TestStarStretchExtendedValidationCancellationAndZeroStrengthParity(t *testing.T) {
	const w, h = 61, 61
	pixels := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x-30), float64(y-30)
			pixels[y*w+x] = float32(.2 + 3*math.Exp(-.5*(dx*dx+dy*dy)/(2.1*2.1)))
		}
	}
	m := &StarMap{Width: w, Height: h, Sources: []StarMapSource{{ID: 305, X: 30, Y: 30, FWHM: 4.8, Radius: 7, Status: "accepted"}}}
	fits, err := FitStarTreatment(context.Background(), m, pixels, w, h, StarTreatmentOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cancelled := &errAfterChecks{Context: context.Background(), limit: 4}
	if _, err := PrepareStarStretchFits(cancelled, pixels, w, h, extendedValidationMeta(w, h), fits, m.Sources); err == nil {
		t.Fatal("footprint preparation did not observe cancellation during its scan")
	}
	meta := extendedValidationMeta(w, h)
	prepared, err := PrepareStarStretchFits(context.Background(), pixels, w, h, meta, fits, m.Sources)
	if err != nil {
		t.Fatal(err)
	}
	mask, err := BuildStarTreatmentMask(context.Background(), prepared, w, h)
	if err != nil {
		t.Fatal(err)
	}
	zero, err := ApplyGentlerStarStretch(context.Background(), pixels, w, h, meta, prepared, mask, StarStretchOptions{Strength: 0})
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range zero {
		want := DiskStretchPreviewValue(pixels[i], meta)
		if v != want {
			t.Fatalf("zero strength changed pixel %d: got=%g want=%g", i, v, want)
		}
	}
}
