import re, pathlib
path = pathlib.Path("internal/ui/workspace_compose.go")
txt = path.read_text()
pattern = r"func applyStretch\(img \*loadedImage\) \(fitsio.ImageData, \[]byte\) \{[\s\S]*?\n}\n\nfunc toGrayRGBA"
repl = '''func applyStretch(img *loadedImage) (fitsio.ImageData, []byte) {
	data := img.HDU.Data // raw pixels
	pixels := make([]float64, len(data.Pixels))
	mask := make([]byte, len(data.Pixels)) // 1=black,2=white,3=nan

	denom := img.Peak - img.Background
	if denom == 0 {
		denom = 1
	}
	if img.ScaledPeak <= 0 {
		img.ScaledPeak = 1
	}
	stretchMul := img.ScaledPeak / denom

	for i, v := range data.Pixels {
		if math.IsNaN(v) {
			mask[i] = 3
			pixels[i] = 0
			continue
		}
		if v < img.Black {
			v = img.Black
			mask[i] = 1
		}
		if v > img.White {
			v = img.White
			mask[i] = 2
		}

		val := (v - img.Background) * stretchMul
		if val < 0 {
			val = 0
		}
		switch img.Mode {
		case stretch.Log:
			val = math.Log1p(val) / math.Log1p(img.ScaledPeak)
		case stretch.Asinh:
			val = math.Asinh(val) / math.Asinh(img.ScaledPeak)
		case stretch.Sqrt:
			val = math.Sqrt(val) / math.Sqrt(img.ScaledPeak)
		case stretch.HistEq:
			val = clamp01(val / img.ScaledPeak)
		case stretch.Linear:
			val = val / img.ScaledPeak
		}

		pixels[i] = clamp01(val)
	}

	if img.Mode == stretch.HistEq {
		stretched := stretch.Apply(pixels, img.Mode)
		return fitsio.ImageData{Width: data.Width, Height: data.Height, Pixels: stretched}, mask
	}

	return fitsio.ImageData{Width: data.Width, Height: data.Height, Pixels: pixels}, mask
}

func toGrayRGBA'''
new, n = re.subn(pattern, repl, txt, count=1)
if n != 1:
    raise SystemExit(f"pattern replacements: {n}")
path.write_text(new)
