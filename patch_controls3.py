from pathlib import Path
path = Path("internal/ui/workspace_compose.go")
text = path.read_text()
old = """\tcontrols := container.NewVBox(\n\t\twidget.NewButton(\"Load Channel 1\", func() { loadChannel(0) }),\n\t\twidget.NewButton(\"Load Channel 2\", func() { loadChannel(1) }),\n\t\twidget.NewButton(\"Load Channel 3\", func() { loadChannel(2) }),\n\t\twidget.NewSeparator(),\n\t\twidget.NewLabel(\"Per-channel controls\"),\n\t\tchannelControls(\"Channel 1\", 0, imgs, viewports, refresh),\n\t\tchannelControls(\"Channel 2\", 1, imgs, viewports, refresh),\n\t\tchannelControls(\"Channel 3\", 2, imgs, viewports, refresh),\n\t\texportBtn,\n\t)\n"""
new = """\tflipCheck := widget.NewCheck(\"Flip image vertically\", func(bool) {})\n\tflipCheck.SetChecked(true)\n\n\tcontrols := container.NewVBox(\n\t\twidget.NewButton(\"Load Channel 1\", func() { loadChannel(0) }),\n\t\twidget.NewButton(\"Load Channel 2\", func() { loadChannel(1) }),\n\t\twidget.NewButton(\"Load Channel 3\", func() { loadChannel(2) }),\n\t\twidget.NewLabel(\"Options\"),\n\t\tflipCheck,\n\t\twidget.NewSeparator(),\n\t\twidget.NewLabel(\"Per-channel controls\"),\n\t\tchannelControls(\"Channel 1\", 0, imgs, viewports, refresh, flipCheck),\n\t\tchannelControls(\"Channel 2\", 1, imgs, viewports, refresh, flipCheck),\n\t\tchannelControls(\"Channel 3\", 2, imgs, viewports, refresh, flipCheck),\n\t\texportBtn,\n\t)\n"""
if old not in text:
    raise SystemExit('controls block not found')
text = text.replace(old, new, 1)
path.write_text(text)
