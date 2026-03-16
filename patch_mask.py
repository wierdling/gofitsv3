import re, pathlib
path = pathlib.Path("internal/ui/workspace_compose.go")
txt = path.read_text()
old = """\tfor i, v := range data.Pixels {
\t\tif math.IsNaN(v) {
\t\t\tmask[i] = 3
\t\t\tpixels[i] = 0
\t\t\tcontinue
\t\t}
\t\tif v < img.Black {
\t\t\tv = img.Black
\t\t\tmask[i] = 1
\t\t}
\t\tif v > img.White {
\t\t\tv = img.White
\t\t\tmask[i] = 2
\t\t}
"""
new = """\tfor i, v := range data.Pixels {
\t\tif math.IsNaN(v) {
\t\t\tif img.ShowClip {
\t\t\t\tmask[i] = 3
\t\t\t}
\t\t\tpixels[i] = 0
\t\t\tcontinue
\t\t}
\t\tif v < img.Black {
\t\t\tif img.ShowClip {
\t\t\t\tmask[i] = 1
\t\t\t}
\t\t\tv = img.Black
\t\t}
\t\tif v > img.White {
\t\t\tif img.ShowClip {
\t\t\t\tmask[i] = 2
\t\t\t}
\t\t\tv = img.White
\t\t}
"""
if old not in txt:
    raise SystemExit("pattern not found")
txt = txt.replace(old, new, 1)
path.write_text(txt)
