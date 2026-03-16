from pathlib import Path
path = Path("internal/ui/workspace_compose.go")
text = path.read_text()
old_controls = """\tcontrols := container.NewVBox(\n\t\twidget.NewSeparator(),\n\t\twidget.NewLabel(\"Per-channel controls\"),\n\t\tchannelControls(\"Channel 1\", 0, imgs, refresh),\n\t\tchannelControls(\"Channel 2\", 1, imgs, refresh),\n\t\tchannelControls(\"Channel 3\", 2, imgs, refresh),\n\t\texportBtn,\n\t)\n"""
if old_controls in text:
    pass
else:
    # already modified in this HEAD; do nothing
    quit()
flip_block = """\tflipCheck := widget.NewCheck(\"Flip image vertically\", func(bool) {})
\tflipCheck.SetChecked(true)\n\n\tcontrols := container.NewVBox(\n\t\twidget.NewLabel(\"Options\"),\n\t\tflipCheck,\n\t\twidget.NewSeparator(),\n\t\twidget.NewLabel(\"Per-channel controls\"),\n\t\tchannelControls(\"Channel 1\", 0, imgs, refresh, flipCheck),\n\t\tchannelControls(\"Channel 2\", 1, imgs, refresh, flipCheck),\n\t\tchannelControls(\"Channel 3\", 2, imgs, refresh, flipCheck),\n\t\texportBtn,\n\t)\n"""
text = text.replace(old_controls, flip_block, 1)
text = text.replace('channelControls("Channel 1", 0, imgs, refresh)', 'channelControls("Channel 1", 0, imgs, refresh, flipCheck)')
text = text.replace('channelControls("Channel 2", 1, imgs, refresh)', 'channelControls("Channel 2", 1, imgs, refresh, flipCheck)')
text = text.replace('channelControls("Channel 3", 2, imgs, refresh)', 'channelControls("Channel 3", 2, imgs, refresh, flipCheck)')
path.write_text(text)
