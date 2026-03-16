from pathlib import Path
path = Path("internal/ui/workspace_compose.go")
text = path.read_text()
old = "\trefresh := func() { updatePreviews(imgs, viewports) }\n\n\tloadChannel := func(idx int) {"
insert = "\trefresh := func() { updatePreviews(imgs, viewports) }\n\n\tflipCheck := widget.NewCheck(\"Flip image horizontally\", func(bool) {})\n\tflipCheck.SetChecked(true)\n\n\tloadChannel := func(idx int) {"
if old not in text:
    raise SystemExit('block not found')
text = text.replace(old, insert, 1)
path.write_text(text)
