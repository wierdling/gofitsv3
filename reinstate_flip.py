from pathlib import Path
path = Path("internal/ui/workspace_compose.go")
text = path.read_text()
if 'flipCheck := widget.NewCheck("Flip image vertically"' not in text:
    text = text.replace('refresh := func() { updatePreviews(imgs, viewports) }\n\n\tloadChannel := func(idx int) {', 'refresh := func() { updatePreviews(imgs, viewports) }\n\n\tflipCheck := widget.NewCheck("Flip image vertically", func(bool) {})\n\tflipCheck.SetChecked(true)\n\n\tloadChannel := func(idx int) {', 1)
text = text.replace('if len(sci) > 1 {\n\t\t\thdu = sci[0]\n\t\t}\n\t\tminV, maxV := autoLevels(hdu.Data.Pixels)', 'if len(sci) > 1 {\n\t\t\thdu = sci[0]\n\t\t}\n\t\tif flipCheck.Checked {\n\t\t\thdu.Data = flipImageData(hdu.Data)\n\t\t}\n\t\tminV, maxV := autoLevels(hdu.Data.Pixels)', 1)
path.write_text(text)
