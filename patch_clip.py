import re, pathlib
path = pathlib.Path("internal/ui/workspace_compose.go")
txt = path.read_text()
old = """\tshowClip := widget.NewCheck(\"Show clipped (blue/green/red)\", func(v bool) {\n\t\tif imgs[idx] == nil {\n\t\t\treturn\n\t\t}\n\t\timgs[idx].ShowClip = v\n\t\trefresh()\n\t})\n\n\tapply := widget.NewButton(\"Apply values\", func() {"""
new = """\tshowClip := widget.NewCheck(\"Show clipped (blue/green/red)\", func(v bool) {\n\t\tif imgs[idx] == nil {\n\t\t\treturn\n\t\t}\n\t\timgs[idx].ShowClip = v\n\t\trefresh()\n\t})\n\tshowClip.SetChecked(true)\n\n\tapply := widget.NewButton(\"Apply values\", func() {"""
if old not in txt:
    raise SystemExit("showClip block not found")
path.write_text(txt.replace(old, new, 1))
