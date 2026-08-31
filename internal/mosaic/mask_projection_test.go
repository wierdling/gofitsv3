package mosaic

import (
	"context"
	"math"
	"path/filepath"
	"testing"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/processing"
)

type nonlinearNativeMIRI struct{}

func (nonlinearNativeMIRI) PixelToICRS(x, y float64) (float64, float64, error) {
	return 100 + x + 0.1*x*x, 20 + y + 0.05*y*y, nil
}

func nativeMIRIHeader() fitsio.Header {
	return fitsio.Header{Cards: map[string]string{
		"CRPIX1": "1", "CRPIX2": "1", "CRVAL1": "100", "CRVAL2": "20",
		"CD1_1": "1", "CD1_2": "0", "CD2_1": "0", "CD2_2": "1",
		"CTYPE1": "RA---TAN", "CTYPE2": "DEC--TAN",
	}}
}

func TestNativeReferenceAffinePairUsesBothGWCSDirections(t *testing.T) {
	ref := makeInput("ref.asdf", 8, 8, filledPixels(8, 8, 1), nativeMIRIHeader())
	target := makeInput("target.asdf", 8, 8, filledPixels(8, 8, 1), nativeMIRIHeader())
	ref.NativeGWCS = nonlinearNativeMIRI{}
	target.NativeGWCS = nonlinearNativeMIRI{}
	w0toR, wRto0, err := referenceAffinePair(ref, target)
	if err != nil {
		t.Fatalf("referenceAffinePair error = %v", err)
	}
	// The native model is nonlinear; the affine is only a local conversion,
	// but it must be derived from native evaluations rather than header WCS.
	if math.Abs(w0toR.A-1) < 1e-6 || math.Abs(w0toR.D-1) < 1e-6 {
		t.Fatalf("native forward affine = %+v, want nonlinear native projection", w0toR)
	}
	if math.Abs(wRto0.A-1) < 1e-6 || math.Abs(wRto0.D-1) < 1e-6 {
		t.Fatalf("native reverse affine = %+v, want nonlinear native projection", wRto0)
	}
}

func TestProjectAuthoringMaskUsesNativeGWCSMapper(t *testing.T) {
	ref := makeInput("ref.asdf", 8, 8, filledPixels(8, 8, 1), nativeMIRIHeader())
	target := makeInput("target.asdf", 8, 8, filledPixels(8, 8, 1), nativeMIRIHeader())
	ref.NativeGWCS = nonlinearNativeMIRI{}
	target.NativeGWCS = nonlinearNativeMIRI{}
	geom := MaskOutputGeometry{Width: 32, Height: 32, OriginX: 0, OriginY: 0, Scale: 1}
	authoring := make([]bool, geom.Width*geom.Height)
	nativeMapper, err := newInputMapper(target, ref)
	if err != nil {
		t.Fatalf("native mapper error = %v", err)
	}
	nx, ny := nativeMapper.MapPixel(4, 3)
	fxMapper, err := processing.NewWCSMapperToLinearRef(target.HDU.Header, nil, nil, ref.HDU.Header)
	if err != nil {
		t.Fatalf("flattened mapper error = %v", err)
	}
	fx, fy := fxMapper.MapPixel(4, 3)
	if math.Abs(nx-fx) < 0.1 || math.Abs(ny-fy) < 0.1 {
		t.Fatalf("native map (%.3f,%.3f) did not differ from flattened (%.3f,%.3f)", nx, ny, fx, fy)
	}
	authoring[int(math.Round(ny))*geom.Width+int(math.Round(nx))] = true
	mask, err := ProjectAuthoringMaskToDetector(target, ref, geom, authoring, MaskProjectionOptions{ConservativeRadius: 0.49})
	if err != nil {
		t.Fatalf("native mask projection error = %v", err)
	}
	if !mask[3*8+4] {
		t.Fatalf("native projection did not select nonlinear target pixel: %v", mask)
	}
	if mask[0] {
		t.Fatalf("native projection selected unrelated detector pixel 0: %v", mask)
	}
}

func TestDetectorOutputMapperMatchesPlannedInputPlacement(t *testing.T) {
	ref := makeInput("ref_cal.fits", 5, 5, filledPixels(5, 5, 1), headerWithCRPIX(10, 10))
	target := makeInput("target_cal.fits", 5, 5, filledPixels(5, 5, 2), headerWithCRPIX(8, 9))
	target.OffsetX = 1.25
	target.OffsetY = -0.5
	target.ManualTransform = processing.AffineTransform{A: 1, B: 0, C: 0.5, D: 0, E: 1, F: 0.25}
	target.HasManualTransform = true
	scale := 1.5

	planned, _, minX, minY, maxX, maxY, err := planInputs([]Input{ref, target}, scale)
	if err != nil {
		t.Fatalf("planInputs error = %v", err)
	}
	if len(planned) != 2 {
		t.Fatalf("len(planned) = %d, want 2", len(planned))
	}
	geom := MaskOutputGeometry{
		Width:   int(math.Ceil((maxX - minX + 1) * scale)),
		Height:  int(math.Ceil((maxY - minY + 1) * scale)),
		OriginX: minX,
		OriginY: minY,
		Scale:   scale,
	}
	mapper, err := NewDetectorOutputMapper(target, ref, geom)
	if err != nil {
		t.Fatalf("NewDetectorOutputMapper error = %v", err)
	}

	gotX, gotY := mapper.MapDetectorToOutput(2, 3)
	wantX, wantY := planned[1].mapOutputPixel(2, 3, minX, minY, scale)
	if math.Abs(gotX-wantX) > 1e-9 || math.Abs(gotY-wantY) > 1e-9 {
		t.Fatalf("mapped output = (%.12f, %.12f), want planned placement (%.12f, %.12f)", gotX, gotY, wantX, wantY)
	}
}

func TestProjectAuthoringMaskToDetectorUsesOutputGeometry(t *testing.T) {
	ref := makeInput("ref_cal.fits", 4, 4, filledPixels(4, 4, 1), headerWithCRPIX(10, 10))
	target := makeInput("target_cal.fits", 4, 4, filledPixels(4, 4, 2), headerWithCRPIX(8, 10))
	scale := 1.0
	planned, _, minX, minY, maxX, maxY, err := planInputs([]Input{ref, target}, scale)
	if err != nil {
		t.Fatalf("planInputs error = %v", err)
	}
	geom := MaskOutputGeometry{
		Width:   int(math.Ceil((maxX - minX + 1) * scale)),
		Height:  int(math.Ceil((maxY - minY + 1) * scale)),
		OriginX: minX,
		OriginY: minY,
		Scale:   scale,
	}

	outX, outY := planned[1].mapOutputPixel(1, 2, minX, minY, scale)
	authoring := make([]bool, geom.Width*geom.Height)
	authoring[int(math.Round(outY))*geom.Width+int(math.Round(outX))] = true

	mask, err := ProjectAuthoringMaskToDetector(target, ref, geom, authoring, MaskProjectionOptions{ConservativeRadius: 0.49})
	if err != nil {
		t.Fatalf("ProjectAuthoringMaskToDetector error = %v", err)
	}
	if !mask[2*target.HDU.Data.Width+1] {
		t.Fatalf("projected mask did not include target detector pixel (1,2): %v", mask)
	}
	if mask[0] {
		t.Fatalf("projected mask included unrelated detector pixel 0: %v", mask)
	}
}

func TestProjectAuthoringMaskToDetectorHandlesNoOverlapAndCancellation(t *testing.T) {
	input := makeInput("target_cal.fits", 3, 3, filledPixels(3, 3, 1), headerWithCRPIX(10, 10))
	geom := MaskOutputGeometry{Width: 2, Height: 2, OriginX: 100, OriginY: 100, Scale: 1}
	authoring := []bool{true, true, true, true}

	mask, err := ProjectAuthoringMaskToDetector(input, input, geom, authoring, MaskProjectionOptions{})
	if err != nil {
		t.Fatalf("ProjectAuthoringMaskToDetector no-overlap error = %v", err)
	}
	for i, v := range mask {
		if v {
			t.Fatalf("mask[%d] = true, want no projected pixels outside geometry: %v", i, mask)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ProjectAuthoringMaskToDetector(input, input, geom, authoring, MaskProjectionOptions{Ctx: ctx}); err != ErrCancelled {
		t.Fatalf("cancelled projection error = %v, want ErrCancelled", err)
	}
}

func TestRasterizeMaskPrimitives(t *testing.T) {
	rect, err := RasterizeRectangleMask(5, 5, 1, 1, 2, 2)
	if err != nil {
		t.Fatalf("RasterizeRectangleMask error = %v", err)
	}
	if !rect[1*5+1] || !rect[2*5+2] || rect[0] {
		t.Fatalf("rectangle mask = %v, want clipped filled rectangle", rect)
	}

	brush, err := RasterizeBrushStrokeMask(5, 5, []MaskPoint{{X: 1, Y: 1}, {X: 3, Y: 1}}, 0.5)
	if err != nil {
		t.Fatalf("RasterizeBrushStrokeMask error = %v", err)
	}
	if !brush[1*5+1] || !brush[1*5+2] || !brush[1*5+3] {
		t.Fatalf("brush mask = %v, want stroke samples connected", brush)
	}

	poly, err := RasterizePolygonMask(5, 5, []MaskPoint{{X: 1, Y: 1}, {X: 4, Y: 1}, {X: 1, Y: 4}})
	if err != nil {
		t.Fatalf("RasterizePolygonMask error = %v", err)
	}
	if !poly[2*5+2] || poly[4*5+4] {
		t.Fatalf("polygon mask = %v, want inside selected and outside clear", poly)
	}

	threshold, err := RasterizeThresholdMask([]float32{
		1, 2, 3,
		4, 5, 6,
		7, 8, 9,
	}, 3, 3, 0, 0, 2, 2, 4, 7)
	if err != nil {
		t.Fatalf("RasterizeThresholdMask error = %v", err)
	}
	if !threshold[3] || !threshold[5] || threshold[1] || threshold[7] {
		t.Fatalf("threshold mask = %v, want only finite values in [4,7]", threshold)
	}
}

func TestSelectConnectedThresholdComponentUsesSeedOrLargest(t *testing.T) {
	mask := []bool{
		true, false, true, true,
		true, false, false, true,
		false, false, false, false,
	}
	seeded, err := SelectConnectedThresholdComponent(mask, 4, 3, 0, 1)
	if err != nil {
		t.Fatalf("SelectConnectedThresholdComponent seeded error = %v", err)
	}
	wantSeeded := []bool{
		true, false, false, false,
		true, false, false, false,
		false, false, false, false,
	}
	for i := range wantSeeded {
		if seeded[i] != wantSeeded[i] {
			t.Fatalf("seeded[%d] = %v, want %v in %v", i, seeded[i], wantSeeded[i], seeded)
		}
	}

	largest, err := SelectConnectedThresholdComponent(mask, 4, 3, -1, -1)
	if err != nil {
		t.Fatalf("SelectConnectedThresholdComponent largest error = %v", err)
	}
	if CountMaskPixels(largest) != 3 || !largest[2] || !largest[3] || !largest[7] {
		t.Fatalf("largest component = %v, want 3-pixel right component", largest)
	}
}

func TestProjectedMIRIArtifactMaskExportsAndExcludesBuildPixels(t *testing.T) {
	dir := t.TempDir()
	input := miriTestInput(filepath.Join(dir, "jw12345_cal.fits"), 3, 3, []float32{
		1, 2, 3,
		4, 99, 6,
		7, 8, 9,
	})
	geom := MaskOutputGeometry{Width: 3, Height: 3, OriginX: 0, OriginY: 0, Scale: 1}
	authoring := make([]bool, geom.Width*geom.Height)
	authoring[1*geom.Width+1] = true

	projected, err := ProjectAuthoringMaskToDetector(input, input, geom, authoring, MaskProjectionOptions{ConservativeRadius: 0.49})
	if err != nil {
		t.Fatalf("ProjectAuthoringMaskToDetector error = %v", err)
	}
	if !projected[4] {
		t.Fatalf("projected mask = %v, want center detector pixel masked", projected)
	}
	if _, err := ExportMIRIArtifactMaskAtomic(input, dir, projected, false); err != nil {
		t.Fatalf("ExportMIRIArtifactMaskAtomic error = %v", err)
	}

	result, err := Build([]Input{input}, Options{
		Scale:    1,
		CRMethod: CRMethodNone,
		Skysub: SkysubOptions{
			MIRIArtifactMask:    true,
			MIRIArtifactMaskDir: dir,
		},
	})
	if err != nil {
		t.Fatalf("Build error = %v", err)
	}
	if result.Weights[4] != 0 || !math.IsNaN(float64(result.Pixels[4])) {
		t.Fatalf("center output = pixel %v weight %v, want excluded by projected MIRI mask", result.Pixels[4], result.Weights[4])
	}
}

func TestNewDetectorOutputMapperRejectsMissingWCS(t *testing.T) {
	input := makeInput("bad.fits", 2, 2, []float32{1, 2, 3, 4}, fitsio.Header{Cards: map[string]string{}})
	geom := MaskOutputGeometry{Width: 2, Height: 2, OriginX: 0, OriginY: 0, Scale: 1}
	if _, err := NewDetectorOutputMapper(input, input, geom); err == nil {
		t.Fatal("NewDetectorOutputMapper error = nil, want missing-WCS rejection")
	}
}
