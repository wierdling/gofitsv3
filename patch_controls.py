import pathlib, re
path = pathlib.Path("internal/ui/workspace_compose.go")
text = path.read_text()
old = """\tcontrols := container.NewVBox(\n\t\twidget.NewSeparator(),\n\t\twidget.NewLabel(\"Per-channel controls\"),\n\t\tchannelControls(\"Channel 1\", 0, imgs, refresh),\n\t\tchannelControls(\"Channel 2\", 1, imgs, refresh),\n\t\tchannelControls(\"Channel 3\", 2, imgs, refresh),\n\t\texportBtn,\n\t)\n"""
new = """\tcontrols := container.NewVBox(\n\t\twidget.NewSeparator(),\n\t\twidget.NewLabel(\"Per-channel controls\"),\n\t\tchannelControls(\"Channel 1\", 0, imgs, viewports, refresh),\n\t\tchannelControls(\"Channel 2\", 1, imgs, viewports, refresh),\n\t\tchannelControls(\"Channel 3\", 2, imgs, viewports, refresh),\n\t\texportBtn,\n\t)\n"""
if old not in text:
    raise SystemExit("controls block not found")
text = text.replace(old, new, 1)
path.write_text(text)
