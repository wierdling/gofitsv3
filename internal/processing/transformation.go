package processing

import (
	"context"
	"errors"
	"math"

	"gonum.org/v1/gonum/mat"
)

type AffineTransform struct {
	A, B, C float64
	D, E, F float64
}

func IdentityTransform() AffineTransform {
	return AffineTransform{A: 1, E: 1}
}

func RotationAround(centerX, centerY, angleRad float64) AffineTransform {
	cosA := math.Cos(angleRad)
	sinA := math.Sin(angleRad)
	return AffineTransform{
		A: cosA,
		B: -sinA,
		C: centerX - (cosA*centerX - sinA*centerY),
		D: sinA,
		E: cosA,
		F: centerY - (sinA*centerX + cosA*centerY),
	}
}

func ApplyAffineTransform(t AffineTransform, x, y float64) (float64, float64) {
	return t.A*x + t.B*y + t.C, t.D*x + t.E*y + t.F
}

// ComposeAffineTransforms returns after(before(x, y)).
func ComposeAffineTransforms(after, before AffineTransform) AffineTransform {
	return AffineTransform{
		A: after.A*before.A + after.B*before.D,
		B: after.A*before.B + after.B*before.E,
		C: after.A*before.C + after.B*before.F + after.C,
		D: after.D*before.A + after.E*before.D,
		E: after.D*before.B + after.E*before.E,
		F: after.D*before.C + after.E*before.F + after.F,
	}
}
func InvertAffineTransform(t AffineTransform) (AffineTransform, error) {
	det := t.A*t.E - t.B*t.D
	if math.Abs(det) < 1e-18 {
		return AffineTransform{}, errors.New("singular affine transform")
	}

	return AffineTransform{
		A: t.E / det,
		B: -t.B / det,
		C: (t.B*t.F - t.C*t.E) / det,
		D: -t.D / det,
		E: t.A / det,
		F: (t.C*t.D - t.A*t.F) / det,
	}, nil
}

func SolveTransformation(pairs []MatchedPair) (AffineTransform, error) {
	n := len(pairs)
	if n < 3 {
		return AffineTransform{}, errors.New("at least 3 matched pairs are required for an affine transform")
	}
	aData := make([]float64, n*3)
	bxData := make([]float64, n)
	byData := make([]float64, n)
	for i, p := range pairs {
		aData[i*3+0] = p.RefX
		aData[i*3+1] = p.RefY
		aData[i*3+2] = 1.0
		bxData[i] = p.TargetX
		byData[i] = p.TargetY
	}
	A := mat.NewDense(n, 3, aData)
	bx := mat.NewVecDense(n, bxData)
	by := mat.NewVecDense(n, byData)
	var vecX, vecY mat.VecDense
	if err := vecX.SolveVec(A, bx); err != nil {
		return AffineTransform{}, err
	}
	if err := vecY.SolveVec(A, by); err != nil {
		return AffineTransform{}, err
	}
	return AffineTransform{A: vecX.AtVec(0), B: vecX.AtVec(1), C: vecX.AtVec(2), D: vecY.AtVec(0), E: vecY.AtVec(1), F: vecY.AtVec(2)}, nil
}

func WarpImage(targetPixels []float32, width, height int, t AffineTransform) []float32 {
	return WarpImageToSize(targetPixels, width, height, width, height, t)
}

func WarpImageToSize(targetPixels []float32, srcWidth, srcHeight, outWidth, outHeight int, t AffineTransform) []float32 {
	return WarpImageToSizeCtx(context.Background(), targetPixels, srcWidth, srcHeight, outWidth, outHeight, t)
}

// WarpImageToSizeCtx behaves like WarpImageToSize but checks ctx for
// cancellation once per output row, returning a partial (short) result if
// canceled. Callers that care about cancellation must check ctx.Err().
func WarpImageToSizeCtx(ctx context.Context, targetPixels []float32, srcWidth, srcHeight, outWidth, outHeight int, t AffineTransform) []float32 {
	n, ok := rasterSize(outWidth, outHeight)
	if !ok {
		return []float32{}
	}
	out := make([]float32, n)
	if srcWidth <= 0 || srcHeight <= 0 || len(targetPixels) == 0 {
		return out
	}
	maxHeight := len(targetPixels) / srcWidth
	if maxHeight <= 0 {
		return out
	}
	if srcHeight > maxHeight {
		srcHeight = maxHeight
	}
	for y := 0; y < outHeight; y++ {
		if ctx.Err() != nil {
			return out
		}
		for x := 0; x < outWidth; x++ {
			srcX := t.A*float64(x) + t.B*float64(y) + t.C
			srcY := t.D*float64(x) + t.E*float64(y) + t.F
			outIdx := y*outWidth + x
			if !isFinite64(srcX) || !isFinite64(srcY) || srcX < 0 || srcY < 0 || srcX > float64(srcWidth-1) || srcY > float64(srcHeight-1) {
				out[outIdx] = 0
				continue
			}
			x0 := int(math.Floor(srcX))
			y0 := int(math.Floor(srcY))
			x1 := x0 + 1
			y1 := y0 + 1
			if x1 >= srcWidth {
				x1 = srcWidth - 1
			}
			if y1 >= srcHeight {
				y1 = srcHeight - 1
			}
			wx := srcX - float64(x0)
			wy := srcY - float64(y0)
			p00 := float64(targetPixels[y0*srcWidth+x0])
			p10 := float64(targetPixels[y0*srcWidth+x1])
			p01 := float64(targetPixels[y1*srcWidth+x0])
			p11 := float64(targetPixels[y1*srcWidth+x1])
			if !isFinite64(p00) || !isFinite64(p10) || !isFinite64(p01) || !isFinite64(p11) {
				out[outIdx] = float32(math.NaN())
				continue
			}
			val := p00*(1-wx)*(1-wy) + p10*wx*(1-wy) + p01*(1-wx)*wy + p11*wx*wy
			out[outIdx] = float32(val)
		}
	}
	return out
}

func rasterSize(width, height int) (int, bool) {
	if width <= 0 || height <= 0 || width > int(^uint(0)>>1)/height {
		return 0, false
	}
	return width * height, true
}
