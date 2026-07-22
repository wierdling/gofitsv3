package processing

import (
	"math"
	"strconv"
	"testing"

	"gofitsv3/internal/fitsio"
)

func TestEstimateTranslationAfterWCS(t *testing.T) {
	ref := makeSyntheticStarFieldForRefine(64, 64, [][2]int{{14, 14}, {30, 20}, {20, 42}, {46, 36}})
	target := shiftPixels(ref, 2, -1, 64, 64)

	header := func(crpix1, crpix2 float64) fitsio.Header {
		return fitsio.Header{Cards: map[string]string{
			"CRPIX1": strconv.FormatFloat(crpix1, 'f', -1, 64),
			"CRPIX2": strconv.FormatFloat(crpix2, 'f', -1, 64),
			"CRVAL1": "100",
			"CRVAL2": "22",
			"CD1_1":  "1",
			"CD1_2":  "0",
			"CD2_1":  "0",
			"CD2_2":  "1",
		}}
	}

	dx, dy, err := EstimateTranslationAfterWCS(target, 64, 64, header(10, 10), ref, 64, 64, header(10, 10), 0, 0)
	if err != nil {
		t.Fatalf("EstimateTranslationAfterWCS returned error: %v", err)
	}
	if math.Abs(dx-(-2)) > 0.6 {
		t.Fatalf("dx = %v, want about -2", dx)
	}
	if math.Abs(dy-1) > 0.6 {
		t.Fatalf("dy = %v, want about 1", dy)
	}
}

func TestEstimateTranslationAfterWCSRespectsExistingOffset(t *testing.T) {
	ref := makeSyntheticStarFieldForRefine(64, 64, [][2]int{{14, 14}, {30, 20}, {20, 42}, {46, 36}})
	target := shiftPixels(ref, 2, -1, 64, 64)

	header := func(crpix1, crpix2 float64) fitsio.Header {
		return fitsio.Header{Cards: map[string]string{
			"CRPIX1": strconv.FormatFloat(crpix1, 'f', -1, 64),
			"CRPIX2": strconv.FormatFloat(crpix2, 'f', -1, 64),
			"CRVAL1": "100",
			"CRVAL2": "22",
			"CD1_1":  "1",
			"CD1_2":  "0",
			"CD2_1":  "0",
			"CD2_2":  "1",
		}}
	}

	dx, dy, err := EstimateTranslationAfterWCS(target, 64, 64, header(10, 10), ref, 64, 64, header(10, 10), -2, 1)
	if err != nil {
		t.Fatalf("EstimateTranslationAfterWCS returned error: %v", err)
	}
	if math.Abs(dx) > 0.6 {
		t.Fatalf("dx = %v, want about 0", dx)
	}
	if math.Abs(dy) > 0.6 {
		t.Fatalf("dy = %v, want about 0", dy)
	}
}

func TestEstimateAffineAfterWCSWithRotation(t *testing.T) {
	// 5° rotation produces ~5 px displacement at the image edges — well above
	// sub-pixel centroid noise on a 128×128 synthetic star field.
	const W, H = 128, 128
	centers := [][2]int{{20, 20}, {100, 20}, {20, 100}, {100, 100}, {60, 60}, {40, 80}, {90, 40}}
	ref := makeSyntheticStarFieldForRefine(W, H, centers)

	const angleDeg = 5.0
	rad := angleDeg * math.Pi / 180
	// rotatePixels with -rad makes source stars appear rotated by -angleDeg
	// in the output; the corrective ManualTransform should be +angleDeg.
	target := rotatePixels(ref, W, H, float64(W)/2, float64(H)/2, -rad)

	header := func() fitsio.Header {
		return fitsio.Header{Cards: map[string]string{
			"CRPIX1": "64", "CRPIX2": "64",
			"CRVAL1": "100", "CRVAL2": "22",
			"CD1_1": "1", "CD1_2": "0",
			"CD2_1": "0", "CD2_2": "1",
		}}
	}

	affine, err := EstimateAffineAfterWCS(target, W, H, header(), ref, W, H, header(), 0, 0)
	if err != nil {
		t.Fatalf("EstimateAffineAfterWCS returned error: %v", err)
	}

	gotAngleDeg := math.Atan2(affine.D, affine.A) * 180 / math.Pi
	if math.Abs(gotAngleDeg-angleDeg) > 1.0 {
		t.Fatalf("rotation = %.4f°, want ≈ +%.4f°", gotAngleDeg, angleDeg)
	}
	scale := math.Sqrt(affine.A*affine.A + affine.D*affine.D)
	if math.Abs(scale-1) > 0.05 {
		t.Fatalf("scale = %.4f, want ≈ 1.0", scale)
	}
}

func makeSyntheticStarFieldForRefine(width, height int, centers [][2]int) []float32 {
	pixels := make([]float32, width*height)
	for _, c := range centers {
		for dy := -3; dy <= 3; dy++ {
			for dx := -3; dx <= 3; dx++ {
				x := c[0] + dx
				y := c[1] + dy
				if x < 0 || x >= width || y < 0 || y >= height {
					continue
				}
				d2 := float64(dx*dx + dy*dy)
				v := float32(100.0 * math.Exp(-d2/(2.0*1.2*1.2)))
				if v < 1.0 {
					v = 0
				}
				pixels[y*width+x] = v
			}
		}
	}
	return pixels
}

func shiftPixels(src []float32, dx, dy, width, height int) []float32 {
	out := make([]float32, len(src))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			sx := x - dx
			sy := y - dy
			if sx < 0 || sx >= width || sy < 0 || sy >= height {
				continue
			}
			out[y*width+x] = src[sy*width+sx]
		}
	}
	return out
}
