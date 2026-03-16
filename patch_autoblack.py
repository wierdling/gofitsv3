import pathlib
import re
path = pathlib.Path("internal/ui/workspace_compose.go")
text = path.read_text()
old = """\tauto := widget.NewButton(\"Auto scaling\", func() {
\t\tif imgs[idx] == nil {
\t\t\treturn
\t\t}
\t\tminV, maxV := autoLevels(imgs[idx].HDU.Data.Pixels)
\t\timgs[idx].Background = minV
\t\timgs[idx].Peak = maxV
\t\timgs[idx].ScaledPeak = 10
\t\timgs[idx].White = maxV
\t\timgs[idx].Black = 0
\t\tviews[idx].blackBox.SetText(\"0\")
\t\tviews[idx].whiteBox.SetText(fmt.Sprintf(\"%.2f\", maxV))
\t\tbackgroundEntry.SetText(fmt.Sprintf(\"%.2f\", minV))
\t\tpeakEntry.SetText(fmt.Sprintf(\"%.2f\", maxV))
\t\tscaledPeakEntry.SetText(\"10\")
\t\trefresh()
\t})
"""
new = """\tauto := widget.NewButton(\"Auto scaling\", func() {
\t\tif imgs[idx] == nil {
\t\t\treturn
\t\t}
\t\tblackVal, errB := parseFloat(views[idx].blackBox.Text)
\t\tif errB != nil {
\t\t\tblackVal = imgs[idx].Black
\t\t}
\t\twhiteVal, errW := parseFloat(views[idx].whiteBox.Text)
\t\tif errW != nil {
\t\t\t_, whiteVal = autoLevels(imgs[idx].HDU.Data.Pixels)
\t\t}
\t\timgs[idx].Background = blackVal
\t\timgs[idx].Peak = whiteVal
\t\timgs[idx].ScaledPeak = 10
\t\timgs[idx].White = whiteVal
\t\timgs[idx].Black = 0
\t\tviews[idx].blackBox.SetText(\"0\")
\t\tviews[idx].whiteBox.SetText(fmt.Sprintf(\"%.2f\", whiteVal))
\t\tbackgroundEntry.SetText(fmt.Sprintf(\"%.2f\", blackVal))
\t\tpeakEntry.SetText(fmt.Sprintf(\"%.2f\", whiteVal))
\t\tscaledPeakEntry.SetText(\"10\")
\t\trefresh()
\t})
"""
if old not in text:
    raise SystemExit("auto block not found")
text = text.replace(old, new, 1)
path.write_text(text)
