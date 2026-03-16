from pathlib import Path
path = Path("internal/ui/workspace_compose.go")
text = path.read_text()
text = text.replace("\n\t}\n\treturn fitsio.ImageData{Width: w, Height: h, Pixels: out}\n}\n", "\n")
path.write_text(text)
