package processing

import (
	"context"
	"fmt"
	"gofitsv3/internal/models"
	"math"
)

// PSFMeasurement describes the second-moment width of unsaturated stars in a
// channel. FWHM values are in pixels; samples is the number of stars used.
type PSFMeasurement struct {
	FWHMX, FWHMY float64
	Ellipticity  float64
	Samples      int
	Saturated    int
}

// PSFTarget is a suggested common PSF. It is the largest measured width in
// each axis, which means sharper channels can be convolved without sharpening.
type PSFTarget struct{ FWHMX, FWHMY float64 }

// MeasurePSF measures star widths using intensity-weighted local moments.
// saturation <= 0 disables the saturated-star rejection.
func MeasurePSF(pixels []float32, width, height int, thresholdSigma, saturation float64) PSFMeasurement {
	if width <= 0 || height <= 0 || len(pixels) < width*height {
		return PSFMeasurement{}
	}
	stars := ExtractStars(pixels[:width*height], width, height, thresholdSigma, 5)
	background, _ := EstimateBackground(pixels[:width*height])
	var sx, sy float64
	valid, saturated := 0, 0
	for _, star := range stars {
		x, y := int(math.Round(star.X)), int(math.Round(star.Y))
		if x < 3 || y < 3 || x >= width-3 || y >= height-3 {
			continue
		}
		peak := float64(pixels[y*width+x])
		if !finite(peak) {
			continue
		}
		if saturation > 0 && peak >= saturation {
			saturated++
			continue
		}
		bg := background
		if !finite(bg) {
			bg = 0
		}
		var sum, mx, my, mxx, myy float64
		for dy := -3; dy <= 3; dy++ {
			for dx := -3; dx <= 3; dx++ {
				v := float64(pixels[(y+dy)*width+x+dx]) - bg
				if !finite(v) || v <= 0 {
					continue
				}
				sum += v
				mx += v * float64(dx)
				my += v * float64(dy)
				mxx += v * float64(dx*dx)
				myy += v * float64(dy*dy)
			}
		}
		if sum <= 0 {
			continue
		}
		vx := mxx/sum - (mx/sum)*(mx/sum)
		vy := myy/sum - (my/sum)*(my/sum)
		if vx <= 0 || vy <= 0 || vx > 100 || vy > 100 {
			continue
		}
		sx += 2.354820045 * math.Sqrt(vx)
		sy += 2.354820045 * math.Sqrt(vy)
		valid++
	}
	if valid == 0 {
		return PSFMeasurement{Saturated: saturated}
	}
	n := valid
	x, y := sx/float64(n), sy/float64(n)
	return PSFMeasurement{FWHMX: x, FWHMY: y, Ellipticity: 1 - math.Min(x, y)/math.Max(x, y), Samples: n, Saturated: saturated}
}

// ApplyPSFMatching returns shallow image copies with convolved pixel slices;
// source channels are never modified. A zero target leaves the images intact.
func ApplyPSFMatching(imgs []*models.LoadedImage, target PSFTarget, protectSaturated bool, saturation float64) []*models.LoadedImage {
	out := append([]*models.LoadedImage(nil), imgs...)
	if target.FWHMX <= 0 || target.FWHMY <= 0 {
		return out
	}
	measurements := make([]PSFMeasurement, len(imgs))
	for i, img := range imgs {
		if img != nil {
			measurements[i] = MeasurePSF(img.HDU.Data.Pixels, img.HDU.Data.Width, img.HDU.Data.Height, 4, saturation)
		}
	}
	for i, img := range imgs {
		if img == nil {
			continue
		}
		if measurements[i].Samples == 0 || measurements[i].FWHMX <= 0 || measurements[i].FWHMY <= 0 {
			continue
		}
		p, err := ConvolveToPSF(context.Background(), img.HDU.Data.Pixels, img.HDU.Data.Width, img.HDU.Data.Height, measurements[i], target, protectSaturated, saturation)
		if err == nil {
			c := *img
			c.HDU.Data.Pixels = p
			out[i] = &c
		}
	}
	return out
}

func SuggestPSFTarget(measurements []PSFMeasurement) PSFTarget {
	t := PSFTarget{}
	for _, m := range measurements {
		if m.FWHMX > t.FWHMX {
			t.FWHMX = m.FWHMX
		}
		if m.FWHMY > t.FWHMY {
			t.FWHMY = m.FWHMY
		}
	}
	return t
}

// ConvolveToPSF applies the Gaussian covariance needed to reach target. The
// source is not modified. Saturated pixels can be copied through unchanged.
func ConvolveToPSF(ctx context.Context, pixels []float32, width, height int, measured PSFMeasurement, target PSFTarget, protectSaturated bool, saturation float64) ([]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if width <= 0 || height <= 0 || len(pixels) < width*height {
		return nil, fmt.Errorf("invalid image dimensions")
	}
	sx := target.FWHMX*target.FWHMX - measured.FWHMX*measured.FWHMX
	sy := target.FWHMY*target.FWHMY - measured.FWHMY*measured.FWHMY
	sx = math.Max(0, sx)
	sy = math.Max(0, sy)
	if sx == 0 && sy == 0 {
		return append([]float32(nil), pixels[:width*height]...), nil
	}
	// FWHM^2 = 8 ln(2) sigma^2.
	rx := math.Sqrt(sx / (8 * math.Ln2))
	ry := math.Sqrt(sy / (8 * math.Ln2))
	out := append([]float32(nil), pixels[:width*height]...)
	for y := 0; y < height; y++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for x := 0; x < width; x++ {
			if protectSaturated && saturation > 0 && float64(pixels[y*width+x]) >= saturation {
				continue
			}
			var sum, weight float64
			for j := maxInt(0, y-int(math.Ceil(3*ry))); j <= minInt(height-1, y+int(math.Ceil(3*ry))); j++ {
				for i := maxInt(0, x-int(math.Ceil(3*rx))); i <= minInt(width-1, x+int(math.Ceil(3*rx))); i++ {
					dx, dy := float64(i-x), float64(j-y)
					w := 1.0
					if rx > 0 {
						w *= math.Exp(-dx * dx / (2 * rx * rx))
					}
					if ry > 0 {
						w *= math.Exp(-dy * dy / (2 * ry * ry))
					}
					sum += w * float64(pixels[j*width+i])
					weight += w
				}
			}
			if weight > 0 {
				out[y*width+x] = float32(sum / weight)
			}
		}
	}
	return out, nil
}
