from pathlib import Path
import textwrap
path = Path("internal/ui/workspace_compose.go")
text = path.read_text()
old_controls = """\tcontrols := container.NewVBox(
\t\twidget.NewSeparator(),
\t\twidget.NewLabel(\"Per-channel controls\"),
\t\tchannelControls(\"Channel 1\", 0, imgs, viewports, refresh),
\t\tchannelControls(\"Channel 2\", 1, imgs, viewports, refresh),
\t\tchannelControls(\"Channel 3\", 2, imgs, viewports, refresh),
\t\texportBtn,
\t)
"""
flip_block = """\tflipCheck := widget.NewCheck(\"Flip image horizontally\", func(bool) {})
\tflipCheck.SetChecked(true)

\tcontrols := container.NewVBox(
\t\twidget.NewSeparator(),
\t\tflipCheck,
\t\twidget.NewLabel(\"Per-channel controls\"),
\t\tchannelControls(\"Channel 1\", 0, imgs, viewports, refresh, flipCheck),
\t\tchannelControls(\"Channel 2\", 1, imgs, viewports, refresh, flipCheck),
\t\tchannelControls(\"Channel 3\", 2, imgs, viewports, refresh, flipCheck),
\t\texportBtn,
\t)
"""
if old_controls not in text:
    raise SystemExit("controls block not found")
text = text.replace(old_controls, flip_block, 1)
text = text.replace('channelControls("Channel 1", 0, imgs, viewports, refresh)', 'channelControls("Channel 1", 0, imgs, viewports, refresh, flipCheck)')
text = text.replace('channelControls("Channel 2", 1, imgs, viewports, refresh)', 'channelControls("Channel 2", 1, imgs, viewports, refresh, flipCheck)')
text = text.replace('channelControls("Channel 3", 2, imgs, viewports, refresh)', 'channelControls("Channel 3", 2, imgs, viewports, refresh, flipCheck)')
path.write_text(text)
