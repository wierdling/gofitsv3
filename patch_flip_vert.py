from pathlib import Path
import re
path = Path("internal/ui/workspace_compose.go")
text = path.read_text()
text = text.replace("Flip image horizontally", "Flip image vertically")
text = re.sub(r"func flipImageData\(data fitsio.ImageData\) fitsio.ImageData \{[\s\S]*?\}", """func flipImageData(data fitsio.ImageData) fitsio.ImageData {
\tw, h := data.Width, data.Height
\tout := make([]float64, len(data.Pixels))
\tfor y := 0; y < h; y++ {
\t\tfor x := 0; x < w; x++ {
\t\t\tsrcIdx := (h-1-y)*w + x
\t\t\tdstIdx := y*w + x
\t\t\tout[dstIdx] = data.Pixels[srcIdx]
\t\t}
\t}
\treturn fitsio.ImageData{Width: w, Height: h, Pixels: out}
}
""", text, count=1)
path.write_text(text)
