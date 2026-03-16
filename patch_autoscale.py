from pathlib import Path
path = Path("internal/ui/workspace_compose.go")
text = path.read_text()
old = """\tauto := widget.NewButton(\"Auto scaling\", func() {\n\t\tif imgs[idx] == nil {\n\t\t\treturn\n\t\t}\n\t\tminV, maxV := autoLevels(imgs[idx].HDU.Data.Pixels)\n\t\timgs[idx].Background = minV\n\t\timgs[idx].Peak = maxV\n\t\timgs[idx].ScaledPeak = 10\n\t\timgs[idx].White = maxV\n\t\timgs[idx].Black = 0\n\t\tviews[idx].blackBox.SetText(\"0\")\n\t\tviews[idx].whiteBox.SetText(fmt.Sprintf(\"%.2f\", maxV))\n\t\tbackgroundEntry.SetText(fmt.Sprintf(\"%.2f\", minV))\n\t\tpeakEntry.SetText(fmt.Sprintf(\"%.2f\", maxV))\n\t\tscaledPeakEntry.SetText(\"10\")\n\t\trefresh()\n\t})\n"""
new = """\tauto := widget.NewButton(\"Auto scaling\", func() {\n\t\tif imgs[idx] == nil {\n\t\t\treturn\n\t\t}\n\t\tblackVal := imgs[idx].Black\n\t\tif v, err := parseFloat(views[idx].blackBox.Text); err == nil {\n\t\t\tblackVal = v\n\t\t}\n\t\twhiteVal := imgs[idx].White\n\t\tif v, err := parseFloat(views[idx].whiteBox.Text); err == nil {\n\t\t\twhiteVal = v\n\t\t} else {\n\t\t\t_, whiteVal = autoLevels(imgs[idx].HDU.Data.Pixels)\n\t\t}\n\t\timgs[idx].Background = blackVal\n\t\timgs[idx].Peak = whiteVal\n\t\timgs[idx].ScaledPeak = 10\n\t\timgs[idx].White = whiteVal\n\t\timgs[idx].Black = 0\n\t\tviews[idx].blackBox.SetText(\"0\")\n\t\tviews[idx].whiteBox.SetText(fmt.Sprintf(\"%.2f\", whiteVal))\n\t\tbackgroundEntry.SetText(fmt.Sprintf(\"%.2f\", blackVal))\n\t\tpeakEntry.SetText(fmt.Sprintf(\"%.2f\", whiteVal))\n\t\tscaledPeakEntry.SetText(\"10\")\n\t\trefresh()\n\t})\n"""
if old not in text:
    raise SystemExit('auto block not found')
text = text.replace(old, new, 1)
path.write_text(text)
