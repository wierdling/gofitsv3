package starstretchpreview

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/mosaic"
	"gofitsv3/internal/processing"
	"gofitsv3/internal/stretch"
)

// Real-data regression for the gentler star stretch. It runs only when
// GOFITS_REALDATA is set, because it needs the reviewed M16 and Trifid
// mosaics and star maps under TestImages/. It asserts the decisions that were
// validated by inspection (usable/skipped, reasons, halo/spike coverage,
// blend groups) and the invariants no synthetic fixture exercises: bounded
// fit time on a 23k-source map, no dark ring on treated saturated stars, and
// untouched pixels outside every mask. Update the expectations deliberately
// when a model change alters an outcome on purpose.
//
//	GOFITS_REALDATA=1 go test ./internal/starstretchpreview -run RealData -v

type realDataSet struct {
	name, science, mapPath string
}

func realDataRoot(t *testing.T) string {
	t.Helper()
	if os.Getenv("GOFITS_REALDATA") == "" {
		t.Skip("set GOFITS_REALDATA=1; needs TestImages/M_16WFC3 and TestImages/Trifid")
	}
	root, err := filepath.Abs(filepath.Join("..", "..", "TestImages"))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func loadRealData(t *testing.T, set realDataSet) (fitsio.HDU, *mosaic.StarMapProduct) {
	t.Helper()
	if _, err := os.Stat(set.science); err != nil {
		t.Skipf("missing %s", set.science)
	}
	file, err := fitsio.LoadFile(set.science)
	if err != nil {
		t.Fatal(err)
	}
	hdu := file.HDUs[0]
	product, err := mosaic.LoadStarMapFITS(context.Background(), set.mapPath, hdu.Data, hdu.Header)
	if err != nil {
		t.Fatal(err)
	}
	return hdu, product
}

func realDataSource(t *testing.T, product *mosaic.StarMapProduct, id int) processing.StarMapSource {
	t.Helper()
	for _, s := range product.Map.Sources {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("source %d not in map", id)
	return processing.StarMapSource{}
}

func realDataFit(t *testing.T, fits []processing.StarTreatmentFit, id int) processing.StarTreatmentFit {
	t.Helper()
	for _, f := range fits {
		if f.SourceID == id {
			return f
		}
	}
	t.Fatalf("no fit for source %d", id)
	return processing.StarTreatmentFit{}
}

var realDataAsinh = models.LoadedImage{Mode: stretch.Asinh, Background: 0, Peak: 1, ScaledPeak: 10, AsinhScale: 1, MTFMidtone: .25}

// realDataAutoAsinh mirrors the preview's automatic source levels, as the app
// uses them, so a bright clipped core is not held at white by a low peak.
func realDataAutoAsinh(hdu fitsio.HDU) models.LoadedImage {
	meta := scalarMetadata(realDataAsinh)
	tmp := meta
	tmp.HDU = hdu
	tmp.Black, tmp.White = 0, 0
	processing.AutoScaleLikeFitsLiberator(&tmp)
	meta.Background, meta.Peak = tmp.Background, tmp.Peak
	return meta
}

// realDataTrifidMTF is the user's reported Trifid F673N setting.
var realDataTrifidMTF = models.LoadedImage{Mode: stretch.MTF, Background: -.0219, Peak: .173, ScaledPeak: 10, AsinhScale: 1, MTFMidtone: .5}

// assertTreatedPreview checks a usable preview for the invariants that were
// verified visually: the gentler render never exceeds the normal one, mask
// zero means pixel unchanged, and the core is actually reduced.
func assertTreatedPreview(t *testing.T, p StarPreview, label string) processing.StarTreatmentFit {
	t.Helper()
	if p.Status != "preview" {
		t.Fatalf("%s: expected a usable preview, got %s: %s", label, p.Status, p.Reason)
	}
	f, ok := p.Diagnostics.(processing.StarTreatmentFit)
	if !ok || !f.Usable {
		t.Fatalf("%s: preview without a usable fit: %+v", label, p.Diagnostics)
	}
	normal, gentle, mask := p.Normal.Bounds(), p.Gentle.Bounds(), p.Mask.Bounds()
	if normal != gentle || normal != mask {
		t.Fatalf("%s: panel sizes differ", label)
	}
	brightened, changedOutsideMask := 0, 0
	for y := normal.Min.Y; y < normal.Max.Y; y++ {
		for x := normal.Min.X; x < normal.Max.X; x++ {
			n, _, _, _ := p.Normal.At(x, y).RGBA()
			g, _, _, _ := p.Gentle.At(x, y).RGBA()
			m, _, _, _ := p.Mask.At(x, y).RGBA()
			if g > n {
				brightened++
			}
			// The mask panel is 8-bit: a weight below 1/255 renders as zero yet
			// can still move a pixel by one level.
			if m == 0 && (g > n+0x101 || n > g+0x101) {
				changedOutsideMask++
			}
		}
	}
	if brightened > 0 {
		t.Fatalf("%s: %d pixels brighter after the gentler stretch", label, brightened)
	}
	if changedOutsideMask > 0 {
		t.Fatalf("%s: %d pixels changed outside the treatment mask", label, changedOutsideMask)
	}
	if len(p.RadialProfile) < 3 {
		t.Fatalf("%s: missing radial profile", label)
	}
	// The star must actually be reduced somewhere in its full-strength region
	// (a clipped core can legitimately stay white below full strength).
	reduced := false
	for _, r := range p.RadialProfile {
		if r.Mask >= .999 && r.Gentle < r.Normal-.01 {
			reduced = true
		}
	}
	if !reduced {
		t.Fatalf("%s: no radial bin was reduced by the gentler stretch", label)
	}
	// No dark ring: inside the full-strength region the gentler profile must
	// not rise outward where the normal profile does not (real diffraction
	// rings rise in both; compression flattening a decline is expected).
	for i := 1; i < len(p.RadialProfile); i++ {
		a, b := p.RadialProfile[i-1], p.RadialProfile[i]
		if b.Mask >= .999 && a.Mask >= .999 && b.Gentle > a.Gentle+.02 && b.Normal <= a.Normal {
			t.Fatalf("%s: dark ring: gentler profile rises %.4f from r=%.1f to r=%.1f while normal rises %.4f", label, b.Gentle-a.Gentle, a.Radius, b.Radius, b.Normal-a.Normal)
		}
	}
	return f
}

func TestRealDataM16BrightSaturatedStars(t *testing.T) {
	root := realDataRoot(t)
	set := realDataSet{"m16-f502n", filepath.Join(root, "M_16WFC3", "F502N_drizzle.fits"), filepath.Join(root, "M_16WFC3", "F502N_starmap.fits")}
	hdu, product := loadRealData(t, set)
	w, h := hdu.Data.Width, hdu.Data.Height

	start := time.Now()
	fits, err := processing.FitStarTreatment(context.Background(), product.Map, hdu.Data.Pixels, w, h, processing.StarTreatmentOptions{})
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	t.Logf("%d sources fit in %s", len(product.Map.Sources), elapsed)
	if len(product.Map.Sources) < 20000 {
		t.Fatalf("unexpected map: %d sources (expected the 23k-source reviewed map)", len(product.Map.Sources))
	}
	if elapsed > 30*time.Second {
		t.Fatalf("fit took %s; neighbor prefilter regression?", elapsed)
	}
	usable, groups := 0, map[int]bool{}
	for _, f := range fits {
		if f.Usable {
			usable++
		}
		if f.GroupID != 0 {
			groups[f.GroupID] = true
		}
	}
	t.Logf("usable=%d blend groups=%d", usable, len(groups))
	if usable < 550 {
		t.Fatalf("only %d usable fits (expected about 630)", usable)
	}
	if len(groups) > 12 {
		t.Fatalf("%d blend groups (expected a handful; companion-only attachment too loose?)", len(groups))
	}

	// 10126 and 17294: amplitude ~90, saturated, with plateau detections that
	// must be dropped rather than modelled. Both were untreatable before the
	// blend/halo work.
	for _, id := range []int{10126, 17294} {
		f := realDataFit(t, fits, id)
		if !f.Usable || !f.Saturated || !f.WingValidated {
			t.Fatalf("star %d: expected validated saturated wings: %+v", id, f)
		}
		if f.Residual > .35 {
			t.Fatalf("star %d: wing residual %.3f above gate", id, f.Residual)
		}
		p, err := previewSource(context.Background(), hdu.Data, product.Map, realDataSource(t, product, id), realDataAutoAsinh(hdu), .75)
		if err != nil {
			t.Fatal(err)
		}
		pf := assertTreatedPreview(t, p, fmt.Sprintf("m16 %d", id))
		if !pf.HaloValidated || pf.HaloRadius < 60 {
			t.Fatalf("star %d: expected a validated halo beyond 60 px, got %v/%.1f (%s)", id, pf.HaloValidated, pf.HaloRadius, pf.ExtendedCoverage)
		}
		t.Logf("star %d: halo %.1f px, %d spike arms, reason=%s", id, pf.HaloRadius, len(pf.Spikes), pf.Reason)
	}
	if f := realDataFit(t, fits, 17294); f.Usable {
		p, _ := previewSource(context.Background(), hdu.Data, product.Map, realDataSource(t, product, 17294), realDataAutoAsinh(hdu), .75)
		if pf, ok := p.Diagnostics.(processing.StarTreatmentFit); ok && len(pf.Spikes) != 4 {
			t.Fatalf("star 17294: expected four validated spike arms, got %d", len(pf.Spikes))
		}
	}

	// 12462 has a genuinely bright, flat-topped companion 10 px away that a
	// profile basis cannot absorb. It must remain an explicit, explained skip;
	// if a better model treats it, update this expectation on purpose.
	if f := realDataFit(t, fits, 12462); f.Usable || f.Reason != "blended profile does not match circular model" {
		t.Fatalf("star 12462: expected an explicit blend-residual skip, got usable=%v reason=%q", f.Usable, f.Reason)
	}
}

func TestRealDataTrifidBlendAndBoundaryHalo(t *testing.T) {
	root := realDataRoot(t)
	set := realDataSet{"trifid-f673n", filepath.Join(root, "Trifid", "F673N_drizzle.fits"), filepath.Join(root, "Trifid", "working", "F673N_starmap.fits")}
	hdu, product := loadRealData(t, set)
	w, h := hdu.Data.Width, hdu.Data.Height
	fits, err := processing.FitStarTreatment(context.Background(), product.Map, hdu.Data.Pixels, w, h, processing.StarTreatmentOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// 817/819: two user-accepted sources whose cores run together (map FWHM
	// 8.0 vs 2.6). They must be fit jointly with per-member widths and one
	// background plane. 819 renders; 817 is an explained skip because 819's
	// real PSF wings, which the Gaussian companion model does not carry, read
	// as directional structure around it. 817's core is still covered by the
	// group footprint. If an empirical-PSF companion model makes 817 usable,
	// update this expectation on purpose.
	a, b := realDataFit(t, fits, 817), realDataFit(t, fits, 819)
	if !a.Usable || !b.Usable {
		t.Fatalf("blend 817/819 not jointly fit: %s | %s", a.Reason, b.Reason)
	}
	if a.GroupID == 0 || a.GroupID != b.GroupID {
		t.Fatalf("817/819 not in one group: %d vs %d", a.GroupID, b.GroupID)
	}
	if len(a.Companions) != 1 || a.Companions[0].SourceID != 819 || len(b.Companions) != 1 || b.Companions[0].SourceID != 817 {
		t.Fatalf("817/819 companions wrong: %+v | %+v", a.Companions, b.Companions)
	}
	if math.Abs(a.Background-b.Background) > 4*math.Max(a.Noise, 1e-9) {
		t.Fatalf("817/819 do not share a background plane: %g vs %g", a.Background, b.Background)
	}
	if a.Sigma < 1.5*b.Sigma {
		t.Fatalf("817/819 widths should differ (per-member widths): %.2f vs %.2f", a.Sigma, b.Sigma)
	}
	prepared, err := processing.PrepareStarStretchFits(context.Background(), hdu.Data.Pixels, w, h, realDataAsinh, []processing.StarTreatmentFit{a, b}, product.Map.Sources)
	if err != nil {
		t.Fatal(err)
	}
	if pa := prepared[0]; pa.Usable || pa.Reason != "structured local residual; stellar footprint is ambiguous" {
		t.Fatalf("817: expected the structured-residual skip, got usable=%v reason=%q", pa.Usable, pa.Reason)
	}
	if !prepared[1].Usable {
		t.Fatalf("819 rejected at preparation: %s", prepared[1].Reason)
	}
	p, err := previewSource(context.Background(), hdu.Data, product.Map, realDataSource(t, product, 819), realDataAsinh, .75)
	if err != nil {
		t.Fatal(err)
	}
	assertTreatedPreview(t, p, "trifid 817/819")
	partner := realDataSource(t, product, 817)
	if m, _, _, _ := p.Mask.At(int(partner.X)-p.CutoutX, int(partner.Y)-p.CutoutY).RGBA(); m < 0xc000 {
		t.Fatalf("817 core not covered by the group footprint: mask=%.2f", float64(m)/0xffff)
	}

	// 906: very bright saturated star whose halo stays visible to the halo
	// search limit under the user's MTF setting. It must close at the safe
	// boundary below the negligible-correction level instead of skipping.
	src := realDataSource(t, product, 906)
	for _, c := range []struct {
		name string
		meta models.LoadedImage
	}{{"mtf", realDataTrifidMTF}, {"asinh", realDataAsinh}} {
		p, err := previewSource(context.Background(), hdu.Data, product.Map, src, c.meta, .75)
		if err != nil {
			t.Fatal(err)
		}
		f := assertTreatedPreview(t, p, "trifid 906 "+c.name)
		if !f.Saturated || !f.WingValidated || !f.HaloValidated || f.HaloRadius < 40 || len(f.Spikes) != 4 {
			t.Fatalf("906 %s: expected validated wings, halo >= 40 px and four spikes: halo=%v/%.1f spikes=%d %s", c.name, f.HaloValidated, f.HaloRadius, len(f.Spikes), f.ExtendedCoverage)
		}
		t.Logf("906 %s: halo %.1f px, reason=%s", c.name, f.HaloRadius, f.Reason)
	}
}

// TestRealDataComposeTreatmentBuild exercises the Compose entry points end to
// end on M16: fit from the original file and map, reuse of current fits,
// model preparation for two stretch settings, and a whole-frame treated
// render that leaves pixels outside every footprint at the ordinary stretch.
func TestRealDataComposeTreatmentBuild(t *testing.T) {
	root := realDataRoot(t)
	science := filepath.Join(root, "M_16WFC3", "F502N_drizzle.fits")
	mapPath := filepath.Join(root, "M_16WFC3", "F502N_starmap.fits")
	if _, err := os.Stat(science); err != nil {
		t.Skip(err)
	}
	start := time.Now()
	fits, err := FitTreatment(context.Background(), science, mapPath, nil, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("FitTreatment (file reload): %d usable in %s", fits.Usable, time.Since(start))
	if !fits.Current(fits.SciencePath, fits.MapPath, 0, 0) || !fits.Current(fits.SciencePath, fits.MapPath, fits.Width, fits.Height) {
		t.Fatal("fresh fits are not current")
	}
	if fits.Current(fits.SciencePath, fits.MapPath, fits.Width+1, fits.Height) {
		t.Fatal("fits current for a different grid")
	}
	file, err := fitsio.LoadFile(science)
	if err != nil {
		t.Fatal(err)
	}
	hdu := file.HDUs[0]
	meta := realDataAutoAsinh(hdu)
	start = time.Now()
	model, stars, err := fits.Model(context.Background(), meta, .75, hdu.Data.Pixels, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Model: %d stars in %s", stars, time.Since(start))
	if stars < 500 {
		t.Fatalf("only %d treated stars", stars)
	}
	img := meta
	img.HDU = hdu
	img.StarTreatment = model
	start = time.Now()
	treated, err := processing.TreatedStretchForSource(context.Background(), &img)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("whole-frame treated render: %s", time.Since(start))
	plain, _ := processing.ApplyStretchParallel(&img)
	changed := 0
	for i := range treated.Pixels {
		if treated.Pixels[i] > plain.Pixels[i]+1e-6 {
			t.Fatalf("pixel %d brightened", i)
		}
		if treated.Pixels[i] < plain.Pixels[i]-1e-6 {
			changed++
		}
	}
	if changed == 0 {
		t.Fatal("treatment changed nothing")
	}
	t.Logf("%d pixels reduced", changed)
	// Different settings need a different model; the fits are reused.
	other := realDataTrifidMTF
	if _, _, err := fits.Model(context.Background(), other, .5, hdu.Data.Pixels, nil); err != nil {
		t.Fatal(err)
	}
	if model.MatchesStretch(other) {
		t.Fatal("model matched different settings")
	}
}
