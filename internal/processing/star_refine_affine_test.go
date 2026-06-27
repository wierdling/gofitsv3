package processing

import (
	"fmt"
	"math"
	"testing"

	"gofitsv3/internal/fitsio"
)

func TestComposeAffineTransforms(t *testing.T) {
	// translation then scale: scale(translate(x)) = scale*x + scale*tx
	translate := AffineTransform{A: 1, E: 1, C: 3, F: 5}
	scale := AffineTransform{A: 2, E: 2}
	composed := ComposeAffineTransforms(scale, translate)

	// composed(1,1) = scale(translate(1,1)) = scale(4,6) = (8,12)
	x, y := ApplyAffineTransform(composed, 1, 1)
	if math.Abs(x-8) > 1e-9 || math.Abs(y-12) > 1e-9 {
		t.Fatalf("ComposeAffineTransforms(scale,translate)(1,1) = (%.4f,%.4f), want (8,12)", x, y)
	}
}

func TestComposeAffineTransformsIdentity(t *testing.T) {
	id := IdentityTransform()
	other := AffineTransform{A: 2, B: 0.5, C: 3, D: -0.5, E: 1.5, F: -2}
	if c := ComposeAffineTransforms(id, other); c != other {
		t.Fatalf("id ∘ other ≠ other: %+v", c)
	}
	if c := ComposeAffineTransforms(other, id); c != other {
		t.Fatalf("other ∘ id ≠ other: %+v", c)
	}
}

// makeWideStarField creates a 400×300 pixel array with bright, well-separated
// stars modeled as 2D Gaussians to pass cosmic ray filtering.
func makeWideStarField() (pixels []float32, positions [][2]int, w, h int) {
	w, h = 400, 300
	positions = [][2]int{
		{50, 50}, {200, 50}, {350, 50},
		{125, 200}, {275, 200},
	}
	pixels = make([]float32, w*h)
	for _, c := range positions {
		for dy := -3; dy <= 3; dy++ {
			for dx := -3; dx <= 3; dx++ {
				x, y := c[0]+dx, c[1]+dy
				if x < 0 || x >= w || y < 0 || y >= h {
					continue
				}
				d2 := float64(dx*dx + dy*dy)
				v := float32(200.0 * math.Exp(-d2/(2.0*1.5*1.5)))
				if v < 1.0 {
					v = 0
				}
				pixels[y*w+x] = v
			}
		}
	}
	return
}

func identityWCSHeader() fitsio.Header {
	return fitsio.Header{Cards: map[string]string{
		"CRPIX1": "10", "CRPIX2": "10",
		"CRVAL1": "100", "CRVAL2": "22",
		"CD1_1": "1", "CD1_2": "0", "CD2_1": "0", "CD2_2": "1",
	}}
}

func TestEstimateAffineFromRefStarsTranslation(t *testing.T) {
	ref, positions, w, h := makeWideStarField()
	// target shifted by (+5, -3): target[y][x] = ref[y+3][x-5]
	target := shiftPixels(ref, 5, -3, w, h)

	hdr := identityWCSHeader()
	refStars := make([]Star, len(positions))
	for i, p := range positions {
		refStars[i] = Star{X: float64(p[0]), Y: float64(p[1])}
	}

	T, err := EstimateAffineFromRefStars(refStars, target, w, h, hdr, hdr, 0, 0, nil)
	if err != nil {
		t.Fatalf("EstimateAffineFromRefStars: %v", err)
	}

	// With identity WCS and no offset, current=identity and T is applied to source
	// positions directly (T(src) ≈ ref). A target star at (rx+5, ry-3) should map
	// back to (rx, ry).
	for _, p := range positions {
		rx, ry := float64(p[0]), float64(p[1])
		tx, ty := rx+5, ry-3
		gotX, gotY := ApplyAffineTransform(T, tx, ty)
		if math.Abs(gotX-rx) > 1.5 || math.Abs(gotY-ry) > 1.5 {
			t.Errorf("T(%.0f,%.0f) = (%.2f,%.2f), want (%.0f,%.0f)", tx, ty, gotX, gotY, rx, ry)
		}
	}
}

// rotatePixels creates a new image where all content is rotated by theta radians
// around (cx, cy). Stars at (x,y) in src appear at the rotated position in the output.
func rotatePixels(src []float32, w, h int, cx, cy, theta float64) []float32 {
	out := make([]float32, w*h)
	cosT := math.Cos(theta)
	sinT := math.Sin(theta)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// Inverse rotation: find which source pixel maps to (x,y) in output
			dx := float64(x) - cx
			dy := float64(y) - cy
			sx := cx + dx*cosT + dy*sinT
			sy := cy - dx*sinT + dy*cosT
			ix := int(math.Round(sx))
			iy := int(math.Round(sy))
			if ix >= 0 && ix < w && iy >= 0 && iy < h {
				out[y*w+x] = src[iy*w+ix]
			}
		}
	}
	return out
}

func TestEstimateAffineFromRefStarsRotation(t *testing.T) {
	ref, positions, w, h := makeWideStarField()

	// Rotate target by 1 degree around image center.
	cx, cy := float64(w)/2, float64(h)/2
	theta := 1.0 * math.Pi / 180.0
	target := rotatePixels(ref, w, h, cx, cy, theta)

	hdr := identityWCSHeader()
	refStars := make([]Star, len(positions))
	for i, p := range positions {
		refStars[i] = Star{X: float64(p[0]), Y: float64(p[1])}
	}

	T, err := EstimateAffineFromRefStars(refStars, target, w, h, hdr, hdr, 0, 0, nil)
	if err != nil {
		t.Fatalf("EstimateAffineFromRefStars: %v", err)
	}

	// With identity WCS, current=identity, so T is applied directly to source pixels.
	// A star at rotated position should map back to the original position.
	cosT := math.Cos(theta)
	sinT := math.Sin(theta)
	for _, p := range positions {
		rx, ry := float64(p[0]), float64(p[1])
		// The rotated (target) position of this star:
		tx := cx + (rx-cx)*cosT - (ry-cy)*sinT
		ty := cy + (rx-cx)*sinT + (ry-cy)*cosT
		gotX, gotY := ApplyAffineTransform(T, tx, ty)
		if math.Abs(gotX-rx) > 2.0 || math.Abs(gotY-ry) > 2.0 {
			t.Errorf("T(%.1f,%.1f) = (%.2f,%.2f), want (%.0f,%.0f) [error: %.2f,%.2f]",
				tx, ty, gotX, gotY, rx, ry, gotX-rx, gotY-ry)
		}
	}
}

func TestEstimateAffineFromRefStarsRotationWithWCSOffset(t *testing.T) {
	// Simulate: reference and target share same stars, but target is shifted by (10,-7)
	// AND rotated by 0.5 degrees. The WCS knows about the shift but NOT the rotation.
	ref, positions, w, h := makeWideStarField()

	cx, cy := float64(w)/2, float64(h)/2
	theta := 0.5 * math.Pi / 180.0
	shiftX, shiftY := 10, -7

	// Target: first rotate, then shift
	rotated := rotatePixels(ref, w, h, cx, cy, theta)
	target := shiftPixels(rotated, shiftX, shiftY, w, h)

	// WCS captures the shift but not the rotation.
	// The target header has a shifted CRPIX to account for the translation.
	refHdr := fitsio.Header{Cards: map[string]string{
		"CRPIX1": "10", "CRPIX2": "10",
		"CRVAL1": "100", "CRVAL2": "22",
		"CD1_1": "1", "CD1_2": "0", "CD2_1": "0", "CD2_2": "1",
	}}
	// Shift CRPIX by the known offset so WCS handles translation.
	targetHdr := fitsio.Header{Cards: map[string]string{
		"CRPIX1": fmt.Sprintf("%d", 10-shiftX), "CRPIX2": fmt.Sprintf("%d", 10+shiftY),
		"CRVAL1": "100", "CRVAL2": "22",
		"CD1_1": "1", "CD1_2": "0", "CD2_1": "0", "CD2_2": "1",
	}}

	refStars := make([]Star, len(positions))
	for i, p := range positions {
		refStars[i] = Star{X: float64(p[0]), Y: float64(p[1])}
	}

	T, err := EstimateAffineFromRefStars(refStars, target, w, h, targetHdr, refHdr, 0, 0, nil)
	if err != nil {
		t.Fatalf("EstimateAffineFromRefStars: %v", err)
	}

	// After applying T ∘ current, every source pixel should map to the correct ref position.
	// Build 'current' the same way the function does internally.
	refToTarget, err := ComputeWCSTransform(targetHdr, refHdr)
	if err != nil {
		t.Fatal(err)
	}
	sourceToRef, err := InvertAffineTransform(refToTarget)
	if err != nil {
		t.Fatal(err)
	}
	current := ComposeAffineTransforms(translationTransform(0, 0), sourceToRef)
	full := ComposeAffineTransforms(T, current)

	cosT := math.Cos(theta)
	sinT := math.Sin(theta)
	for _, p := range positions {
		rx, ry := float64(p[0]), float64(p[1])
		// Target position of this star: rotate then shift
		rotX := cx + (rx-cx)*cosT - (ry-cy)*sinT
		rotY := cy + (rx-cx)*sinT + (ry-cy)*cosT
		tx := rotX + float64(shiftX)
		ty := rotY + float64(shiftY)
		gotX, gotY := ApplyAffineTransform(full, tx, ty)
		if math.Abs(gotX-rx) > 2.0 || math.Abs(gotY-ry) > 2.0 {
			t.Errorf("full(%.1f,%.1f) = (%.2f,%.2f), want (%.0f,%.0f) [error: %.2f,%.2f]",
				tx, ty, gotX, gotY, rx, ry, gotX-rx, gotY-ry)
		}
	}
}

func TestEstimateAffineFromRefStarsWithExistingOffset(t *testing.T) {
	// Same shift (+5,-3) but with initialOffset (-5,+3) which exactly cancels it.
	// The back-projected position lands directly on the target star, so T ≈ identity.
	ref, positions, w, h := makeWideStarField()
	target := shiftPixels(ref, 5, -3, w, h)

	hdr := identityWCSHeader()
	refStars := make([]Star, len(positions))
	for i, p := range positions {
		refStars[i] = Star{X: float64(p[0]), Y: float64(p[1])}
	}

	T, err := EstimateAffineFromRefStars(refStars, target, w, h, hdr, hdr, -5, 3, nil)
	if err != nil {
		t.Fatalf("EstimateAffineFromRefStars: %v", err)
	}

	// With offset (-5,+3) baked in, current maps (rx+5,ry-3) → (rx,ry), so the full
	// pipeline T(current(src)) ≈ ref for each target star.
	for _, p := range positions {
		rx, ry := float64(p[0]), float64(p[1])
		tx, ty := rx+5, ry-3
		curX := tx - 5   // current = translation(-5,+3): src_x - 5
		curY := ty + 3   // src_y + 3
		gotX, gotY := ApplyAffineTransform(T, curX, curY)
		if math.Abs(gotX-rx) > 1.5 || math.Abs(gotY-ry) > 1.5 {
			t.Errorf("T(current(%.0f,%.0f)) = (%.2f,%.2f), want (%.0f,%.0f)", tx, ty, gotX, gotY, rx, ry)
		}
	}
}
