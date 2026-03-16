from pathlib import Path
path = Path("internal/ui/workspace_compose.go")
text = path.read_text()
needle = """\t\tif len(sci) == 1 {\n\t\t\thdu = sci[0]\n\t\t} else if len(sci) > 1 {\n\t\t\thdu = sci[0]\n\t\t}\n\t\tminV, maxV := autoLevels(hdu.Data.Pixels)
"""
insert = """\t\tif len(sci) == 1 {\n\t\t\thdu = sci[0]\n\t\t} else if len(sci) > 1 {\n\t\t\thdu = sci[0]\n\t\t}\n\t\tif flipCheck.Checked {\n\t\t\thdu.Data = flipImageData(hdu.Data)\n\t\t}\n\t\tminV, maxV := autoLevels(hdu.Data.Pixels)
"""
if needle not in text:
    raise SystemExit('pattern not found')
text = text.replace(needle, insert, 1)
path.write_text(text)
