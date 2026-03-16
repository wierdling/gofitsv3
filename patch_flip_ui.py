from pathlib import Path
path = Path("internal/ui/workspace_compose.go")
text = path.read_text()
old = """\tcontrols := container.NewVBox(
\t\twidget.NewSeparator(),
\t\tflipCheck,
\t\twidget.NewLabel(\"Per-channel controls\"),
\t\tchannelControls(\"Channel 1\", 0, imgs, viewports, refresh, flipCheck),
\t\tchannelControls(\"Channel 2\", 1, imgs, viewports, refresh, flipCheck),
\t\tchannelControls(\"Channel 3\", 2, imgs, viewports, refresh, flipCheck),
\t\texportBtn,
\t)
"""
new = """\tcontrols := container.NewVBox(
\t\twidget.NewLabel(\"Options\"),
\t\tflipCheck,
\t\twidget.NewSeparator(),
\t\twidget.NewLabel(\"Per-channel controls\"),
\t\tchannelControls(\"Channel 1\", 0, imgs, viewports, refresh, flipCheck),
\t\tchannelControls(\"Channel 2\", 1, imgs, viewports, refresh, flipCheck),
\t\tchannelControls(\"Channel 3\", 2, imgs, viewports, refresh, flipCheck),
\t\texportBtn,
\t)
"""
if old not in text:
    raise SystemExit('controls block not found')
text = text.replace(old, new, 1)
path.write_text(text)
