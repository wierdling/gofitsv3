from pathlib import Path
path = Path("internal/ui/workspace_compose.go")
text = path.read_text()
text = text.replace(
    "func channelControls(label string, idx int, imgs []*loadedImage, views []*viewport, refresh func()) fyne.CanvasObject {",
    "func channelControls(label string, idx int, imgs []*loadedImage, views []*viewport, refresh func(), flipCheck *widget.Check) fyne.CanvasObject {",
    1,
)
path.write_text(text)
