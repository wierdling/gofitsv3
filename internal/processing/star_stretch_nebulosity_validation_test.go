package processing

import (
	"context"
	"math"
	"testing"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/stretch"
)

// nebulosityScene is deliberately analytic: the stellar increment and the
// non-stellar background are both known, so a successful correction can be
// checked for background leakage without relying on a visual judgement.
type nebulosityScene struct {
	pixels, background, stellar []float32
	w, h                        int
	star                        StarMapSource
}

func makeNebulosityScene(kind string) nebulosityScene {
	const w, h = 101, 101
	starX, starY := 50.0, 50.0
	if kind == "curved-edge-rotated-subpixel" {
		starX, starY = 50.35, 49.6
	}
	s := nebulosityScene{w: w, h: h, pixels: make([]float32, w*h), background: make([]float32, w*h), stellar: make([]float32, w*h), star: StarMapSource{ID: 401, X: starX, Y: starY, FWHM: 3.8, Radius: 8, Status: "accepted"}}
	starAmplitude, starSigma := 4.0, 1.6
	if kind == "centered-knot" || kind == "centered-halo" {
		// The two interpretations below intentionally generate the same
		// observed pixels, including the catalogued broad source geometry.
		s.star.FWHM, s.star.Radius = 7.3, 11
		starAmplitude, starSigma = 1.25, 3.1
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x)-s.star.X, float64(y)-s.star.Y
			base := .22 + .0012*dx + .0007*dy
			var nebula float64
			switch kind {
			case "curved-edge", "curved-edge-rotated-subpixel":
				// A curved bright rim passes through one side of the footprint.
				ux, uy := dx, dy
				if kind == "curved-edge-rotated-subpixel" {
					const c, s = .7071067811865476, .7071067811865476
					ux, uy = c*dx+s*dy, -s*dx+c*dy
				}
				curve := uy - (.18*ux*ux + 3)
				nebula = .22 * math.Exp(-.5*curve*curve/(1.35*1.35))
			case "offset-knot":
				nebula = .20 * math.Exp(-.5*((dx-4.5)*(dx-4.5)+dy*dy)/(3.8*3.8))
			case "filament":
				line := dy - .35*dx
				nebula = .16 * math.Exp(-.5*line*line/(.8*.8)) * math.Exp(-.5*dx*dx/(7.5*7.5))
			case "centered-knot":
				// This is intentionally identical to a broad stellar halo. There
				// is no pixel-only test that can distinguish the two explanations.
				nebula = 1.25 * math.Exp(-.5*(dx*dx+dy*dy)/(3.1*3.1))
				starAmplitude = 0
			case "centered-halo":
				// Same pixels as centered-knot, but the broad component is
				// labelled stellar in this interpretation.
				nebula = 0
			case "planar":
				nebula = 0
			}
			star := starAmplitude * math.Exp(-.5*(dx*dx+dy*dy)/(starSigma*starSigma))
			i := y*w + x
			s.background[i] = float32(base + nebula)
			s.stellar[i] = float32(star)
			s.pixels[i] = s.background[i] + s.stellar[i]
		}
	}
	return s
}

func nebulosityMeta(w, h int) (meta models.LoadedImage) {
	return models.LoadedImage{HDU: fitsio.HDU{Data: fitsio.ImageData{Width: w, Height: h}}, Mode: stretch.Linear, Background: 0, Peak: 5, ScaledPeak: 1, AsinhScale: 1, MTFMidtone: .5}
}

func nebulosityOutcomeWithMeta(t *testing.T, scene nebulosityScene, meta models.LoadedImage) ([]StarTreatmentFit, []float32, []float32, error) {
	t.Helper()
	m := &StarMap{Width: scene.w, Height: scene.h, Sources: []StarMapSource{scene.star}}
	fits, err := FitStarTreatment(context.Background(), m, scene.pixels, scene.w, scene.h, StarTreatmentOptions{})
	if err != nil {
		return fits, nil, nil, err
	}
	if len(fits) == 0 || !fits[0].Usable {
		return fits, nil, nil, nil
	}
	prepared, err := PrepareStarStretchFits(context.Background(), scene.pixels, scene.w, scene.h, meta, fits, m.Sources)
	if err != nil {
		return fits, nil, nil, err
	}
	if !prepared[0].Usable {
		return prepared, nil, nil, footprintSkipError{prepared[0].Reason}
	}
	mask, err := BuildStarTreatmentMask(context.Background(), prepared, scene.w, scene.h)
	if err != nil {
		return prepared, nil, nil, err
	}
	rendered, err := ApplyGentlerStarStretch(context.Background(), scene.pixels, scene.w, scene.h, meta, prepared, mask, StarStretchOptions{Strength: 1})
	return prepared, mask, rendered, err
}

// footprintSkipError reports that preparation marked the single scene star
// unusable (a per-star skip, not a pipeline error).
type footprintSkipError struct{ reason string }

func (e footprintSkipError) Error() string { return e.reason }

func unsafeFootprintSkip(err error) (string, bool) {
	skip, ok := err.(footprintSkipError)
	if !ok {
		return "", false
	}
	return skip.reason, skip.reason == "structured local residual; stellar footprint is ambiguous" || skip.reason == "visible halo reaches a neighbor or preview boundary"
}

func nebulosityOutcome(t *testing.T, scene nebulosityScene) ([]StarTreatmentFit, []float32, []float32, error) {
	return nebulosityOutcomeWithMeta(t, scene, nebulosityMeta(scene.w, scene.h))
}

func assertNebulosityBound(t *testing.T, scene nebulosityScene, meta models.LoadedImage, fits []StarTreatmentFit, mask, rendered []float32) {
	t.Helper()
	maxLeak, maxRatio, maxExcess := 0.0, 0.0, math.Inf(-1)
	var worstIndex, maxCorrectionIndex int
	var worstOracle, worstCorrection, maxCorrectionOracle float64
	for i := range scene.pixels {
		if mask[i] == 0 {
			continue
		}
		normal := float64(DiskStretchPreviewValue(scene.pixels[i], meta))
		changed := normal - float64(rendered[i])
		stellarOnly := float64(DiskStretchPreviewValue(scene.background[i]+scene.stellar[i], meta)) - float64(DiskStretchPreviewValue(scene.background[i], meta))
		if changed > maxLeak {
			maxLeak = changed
			maxCorrectionIndex, maxCorrectionOracle = i, stellarOnly
		}
		if stellarOnly > 1e-5 && changed/stellarOnly > maxRatio {
			maxRatio = changed / stellarOnly
		}
		excess := changed - 1.5*stellarOnly - 0.003
		if excess > maxExcess {
			maxExcess = excess
			worstIndex, worstOracle, worstCorrection = i, stellarOnly, changed
		}
		// A small multiplier allows numerical/model mismatch while rejecting
		// compression dominated by the known nebular component.
	}
	t.Logf("bounded outcome: max correction=%g at pixel %d (oracle=%g), max correction/oracle=%g, max excess=%g, fit=%+v", maxLeak, maxCorrectionIndex, maxCorrectionOracle, maxRatio, maxExcess, fits[0])
	if maxExcess > 0 {
		t.Errorf("background leakage at pixel %d: correction=%g stellarOracle=%g fit=%+v", worstIndex, worstCorrection, worstOracle, fits[0])
	}
}

func TestStarStretchComplexNebulosityValidation(t *testing.T) {
	for _, kind := range []string{"curved-edge", "curved-edge-rotated-subpixel", "offset-knot", "filament", "planar"} {
		t.Run(kind, func(t *testing.T) {
			scene := makeNebulosityScene(kind)
			fits, mask, rendered, err := nebulosityOutcome(t, scene)
			if reason, ok := unsafeFootprintSkip(err); ok {
				if kind == "planar" {
					t.Fatalf("unexpected footprint skip: %q; fits=%+v", reason, fits)
				}
				t.Logf("safe whole-star skip during preparation: %s; fits=%+v", reason, fits)
				return
			}
			if len(fits) == 0 || !fits[0].Usable {
				t.Fatalf("initial fit was not usable; this case did not exercise footprint validation: %+v", fits)
			}
			if err != nil {
				t.Fatalf("unexpected footprint validation error: %v; initial fits=%+v", err, fits)
			}
			if kind == "planar" && len(mask) == 0 {
				t.Fatal("planar control produced no treatment mask")
			}
			assertNebulosityBound(t, scene, nebulosityMeta(scene.w, scene.h), fits, mask, rendered)
		})
	}
}

func TestStarStretchComplexNebulosityValidationMTF(t *testing.T) {
	scene := makeNebulosityScene("curved-edge")
	meta := nebulosityMeta(scene.w, scene.h)
	// m=.25 is deliberately non-identity for this normalized source range;
	// m=.5 would make the MTF transform equal to the input and add no mode
	// coverage.
	meta.Mode, meta.MTFMidtone = stretch.MTF, .25
	fits, mask, rendered, err := nebulosityOutcomeWithMeta(t, scene, meta)
	if reason, ok := unsafeFootprintSkip(err); ok {
		t.Logf("MTF safely skipped complex background: %s", reason)
		return
	}
	if len(fits) == 0 || !fits[0].Usable {
		t.Fatalf("MTF curved-edge initial fit was not usable: %+v", fits)
	}
	if err != nil {
		t.Fatalf("unexpected MTF footprint validation error: %v", err)
	}
	assertNebulosityBound(t, scene, meta, fits, mask, rendered)
}

func TestStarStretchCenteredKnotIsObservationallyAmbiguous(t *testing.T) {
	stellar := makeNebulosityScene("centered-halo")
	knot := makeNebulosityScene("centered-knot")
	// The centered case is deliberately treated as an ambiguity test. A
	// broad Gaussian added to the background has exactly the same pixels as a
	// broad stellar halo, so no conservative algorithm can identify its origin
	// from this image alone.
	for i := range stellar.pixels {
		if math.Abs(float64(stellar.pixels[i]-knot.pixels[i])) > 2e-7 {
			t.Fatalf("ambiguity fixtures diverged at pixel %d: stellar=%g knot=%g", i, stellar.pixels[i], knot.pixels[i])
		}
	}
	fs, ms, rs, es := nebulosityOutcome(t, stellar)
	fk, mk, rk, ek := nebulosityOutcome(t, knot)
	if (es == nil) != (ek == nil) {
		t.Fatalf("identical morphology took different fit paths: stellar=%v knot=%v", es, ek)
	}
	if len(fs) != len(fk) || len(fs) == 0 {
		t.Fatalf("ambiguous cases produced different fit cardinality: stellar=%+v knot=%+v", fs, fk)
	}
	t.Logf("centered broad-knot ambiguity is expected; stellar fit=%+v knot fit=%+v masks=%d/%d rendered=%d/%d", fs[0], fk[0], len(ms), len(mk), len(rs), len(rk))
	if fs[0].Usable != fk[0].Usable || math.Abs(fs[0].Sigma-fk[0].Sigma) > 1e-4 || math.Abs(fs[0].OuterRadius-fk[0].OuterRadius) > 1e-4 {
		t.Fatalf("identical observations produced materially different fits: stellar=%+v knot=%+v", fs[0], fk[0])
	}
	if len(ms) != len(mk) || len(rs) != len(rk) {
		t.Fatalf("identical observations produced different output shapes: masks=%d/%d rendered=%d/%d", len(ms), len(mk), len(rs), len(rk))
	}
	for i := range ms {
		if math.Abs(float64(ms[i]-mk[i])) > 1e-5 || math.Abs(float64(rs[i]-rk[i])) > 1e-5 {
			t.Fatalf("identical observations produced different mask/render at pixel %d: mask=%g/%g render=%g/%g", i, ms[i], mk[i], rs[i], rk[i])
		}
	}
}

func TestFootprintStructuredResidualGuardRetainsInnerRadiusRejection(t *testing.T) {
	const w, h = 81, 81
	pixels := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x-40), float64(y-40)
			star := 4 * math.Exp(-.5*(dx*dx+dy*dy)/(1.6*1.6))
			knot := 3 * math.Exp(-.5*((dx-4)*(dx-4)+dy*dy)/(3.8*3.8))
			pixels[y*w+x] = float32(.2 + star + knot)
		}
	}
	f := StarTreatmentFit{X: 40, Y: 40, Background: .2, Signal: 4, Sigma: 1.6, Noise: .0005}
	// The guard must still see a strong directional residual when the
	// proposed footprint begins close to the compact core. The Gaussian-tail
	// threshold should suppress weak profile scatter, not erase this branch.
	if !footprintHasStructuredResidual(context.Background(), pixels, w, h, f, 3.2, 7.2) {
		t.Fatal("strong inner-radius directional residual was not rejected")
	}
}

func TestFootprintStructuredResidualGuardKeepsWeakInnerDirectionalCap(t *testing.T) {
	const w, h = 81, 81
	pixels := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x-40), float64(y-40)
			r := math.Hypot(dx, dy)
			// Repeat a modest one-sided residual across multiple annuli. At
			// r≈3.6 the old 1.5%-of-signal cap is .06, while 3 Gaussian
			// tails plus noise is about .996 for this fit. The directional
			// residual is intentionally .15: large enough for the legacy
			// guard, below the uncapped tail threshold.
			if r >= 3.2 && r < 4.7 {
				pixels[y*w+x] = .01
				if dx > 0 {
					pixels[y*w+x] = .16
				}
			}
		}
	}
	f := StarTreatmentFit{X: 40, Y: 40, Background: 0, Signal: 4, Sigma: 1.6, Noise: .0005}
	if !footprintHasStructuredResidual(context.Background(), pixels, w, h, f, 3.2, 7.2) {
		t.Fatal("weak repeated inner directional residual was not rejected")
	}
}
