from pathlib import Path
path = Path("internal/ui/workspace_compose.go")
text = path.read_text()
# Remove existing flipCheck declaration in controls block if any
text = text.replace('\tcontrols := container.NewVBox(\n\t\twidget.NewLabel("Options"),\n\t\tflipCheck,', '\tcontrols := container.NewVBox(\n\t\twidget.NewLabel("Options"),\n\t\tflipCheck,', 1)
# Insert flipCheck before loadChannel
needle = 'refresh := func() { updatePreviews(imgs, viewports) }\n\n\tloadChannel := func(idx int) {'
insert = 'refresh := func() { updatePreviews(imgs, viewports) }\n\n\tflipCheck := widget.NewCheck("Flip image vertically", func(bool) {})\n\tflipCheck.SetChecked(true)\n\n\tloadChannel := func(idx int) {'
if needle not in text:
    raise SystemExit('needle not found')
text = text.replace(needle, insert, 1)
# Add flip inside loadChannel
text = text.replace('if len(sci) > 1 {\n\t\t\thdu = sci[0]\n\t\t}\n\t\tminV, maxV := autoLevels(hdu.Data.Pixels)', 'if len(sci) > 1 {\n\t\t\thdu = sci[0]\n\t\t}\n\t\tif flipCheck.Checked {\n\t\t\thdu.Data = flipImageData(hdu.Data)\n\t\t}\n\t\tminV, maxV := autoLevels(hdu.Data.Pixels)', 1)
path.write_text(text)
