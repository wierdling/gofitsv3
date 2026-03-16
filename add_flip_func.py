from pathlib import Path
path = Path("internal/ui/workspace_compose.go")
text = path.read_text()
add = """
func flipImageData(data fitsio.ImageData) fitsio.ImageData {
	w, h := data.Width, data.Height
	out := make([]float64, len(data.Pixels))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			srcIdx := y*w + (w - 1 - x)
			dstIdx := y*w + x
			out[dstIdx] = data.Pixels[srcIdx]
		}
	}
	return fitsio.ImageData{Width: w, Height: h, Pixels: out}
}
"""
if "func flipImageData" in text:
    raise SystemExit("already present")
text += "\n" + add
path.write_text(text)
