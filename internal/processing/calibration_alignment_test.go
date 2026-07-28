package processing

import (
	"context"
	"testing"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
)

func TestAlignedPlanesForCalibrationWCSPartialOverlapMasksFill(t *testing.T) {
	refHeader := fitsio.Header{Cards: map[string]string{
		"CRPIX1": "2", "CRPIX2": "1", "CRVAL1": "100", "CRVAL2": "20",
		"CD1_1": "1", "CD1_2": "0", "CD2_1": "0", "CD2_2": "1",
	}}
	sourceHeader := fitsio.Header{Cards: map[string]string{
		"CRPIX1": "2.5", "CRPIX2": "1", "CRVAL1": "100", "CRVAL2": "20",
		"CD1_1": "1", "CD1_2": "0", "CD2_1": "0", "CD2_2": "1",
	}}
	blue := makeLoadedImageForCompose(4, 2, 10, sourceHeader)
	green := makeLoadedImageForCompose(4, 2, 10, refHeader)
	red := makeLoadedImageForCompose(4, 2, 10, sourceHeader)
	planes, width, height, err := AlignedPlanesForCalibration(context.Background(), []*models.LoadedImage{blue, green, red})
	if err != nil {
		t.Fatal(err)
	}
	if width != 4 || height != 2 {
		t.Fatalf("reference dimensions = %dx%d, want 4x2", width, height)
	}
	for i, plane := range planes {
		if len(plane.Pixels) != 8 || len(plane.Valid) != 8 {
			t.Fatalf("plane %d lengths = %d/%d, want 8/8", i, len(plane.Pixels), len(plane.Valid))
		}
	}
	invalid := 0
	for _, ok := range planes[0].Valid {
		if !ok {
			invalid++
		}
	}
	if invalid == 0 {
		t.Fatal("WCS partial overlap did not mark source-footprint margins invalid")
	}
	estimate, err := EstimateBackgroundPlane(context.Background(), BackgroundPlane{Pixels: planes[0].Pixels, Valid: planes[0].Valid, Width: width, Height: height}, BackgroundConfig{MinSamples: 2, TileSize: 2})
	if err != nil || estimate.Transform.Offset != 10 {
		t.Fatalf("WCS fill affected background = %+v, err=%v", estimate, err)
	}
}

func TestAlignedPlanesForCalibrationResizeFallbackMasksFill(t *testing.T) {
	blue := makeLoadedImageForCompose(2, 2, 10, fitsio.Header{})
	green := makeLoadedImageForCompose(4, 2, 10, fitsio.Header{})
	red := makeLoadedImageForCompose(2, 2, 10, fitsio.Header{})
	planes, width, height, err := AlignedPlanesForCalibration(context.Background(), []*models.LoadedImage{blue, green, red})
	if err != nil {
		t.Fatal(err)
	}
	if width != 4 || height != 2 {
		t.Fatalf("fallback dimensions = %dx%d, want 4x2", width, height)
	}
	invalid := 0
	for _, ok := range planes[0].Valid {
		if !ok {
			invalid++
		}
	}
	if invalid == 0 {
		t.Fatal("resize fallback did not mark out-of-source margins invalid")
	}
	estimate, err := EstimateBackgroundPlane(context.Background(), BackgroundPlane{Pixels: planes[0].Pixels, Valid: planes[0].Valid, Width: width, Height: height}, BackgroundConfig{MinSamples: 2, TileSize: 2})
	if err != nil || estimate.Transform.Offset != 10 {
		t.Fatalf("resize fill affected background = %+v, err=%v", estimate, err)
	}
}
