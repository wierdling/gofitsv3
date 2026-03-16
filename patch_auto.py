import re, pathlib
path = pathlib.Path("internal/ui/workspace_compose.go")
txt = path.read_text()
old = """	auto := widget.NewButton(\"Auto scaling\", func() {
\t\tif imgs[idx] == nil {
\t\t\treturn
\t\t}
\t\tminV, maxV := autoLevels(imgs[idx].HDU.Data.Pixels)
\t\timgs[idx].Black = minV
\t\timgs[idx].White = maxV
\t\timgs[idx].Background = minV
\t\timgs[idx].Peak = maxV
\t\timgs[idx].ScaledPeak = maxV
\t\tviews[idx].blackBox.SetText(fmt.Sprintf(\"%.2f\", minV))
\t\tviews[idx].whiteBox.SetText(fmt.Sprintf(\"%.2f\", maxV))
\t\tbackgroundEntry.SetText(fmt.Sprintf(\"%.2f\", minV))
\t\tpeakEntry.SetText(fmt.Sprintf(\"%.2f\", maxV))
\t\tscaledPeakEntry.SetText(fmt.Sprintf(\"%.2f\", maxV))
\t\trefresh()
\t})
"""
new = """	auto := widget.NewButton(\"Auto scaling\", func() {
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
if old not in txt:
    raise SystemExit("auto block not found")
path.write_text(txt.replace(old, new, 1))
