import pathlib, re
path = pathlib.Path("internal/ui/workspace_compose.go")
text = path.read_text()
# replace menu insertion
needle = "\t\tfd.Show()\n\t}\n\n\texportBtn := widget.NewButton(\"Export RGB\", func() {"
menu_block = """\t\tfd.Show()\n\t}\n\n\tfileMenu := fyne.NewMenu(\"File\",\n\t\tfyne.NewMenuItem(\"Load Channel 1\", func() { loadChannel(0) }),\n\t\tfyne.NewMenuItem(\"Load Channel 2\", func() { loadChannel(1) }),\n\t\tfyne.NewMenuItem(\"Load Channel 3\", func() { loadChannel(2) }),\n\t)\n\twin.SetMainMenu(fyne.NewMainMenu(fileMenu))\n\n\texportBtn := widget.NewButton(\"Export RGB\", func() {"""
if needle not in text:
    raise SystemExit("needle not found")
text = text.replace(needle, menu_block, 1)
# replace controls block with regex
controls_re = re.compile(r"\tcontrols := container.NewVBox\([\s\S]*?\t\)\n", re.MULTILINE)
new_controls = """\tcontrols := container.NewVBox(\n\t\twidget.NewSeparator(),\n\t\twidget.NewLabel(\"Per-channel controls\"),\n\t\tchannelControls(\"Channel 1\", 0, imgs, refresh),\n\t\tchannelControls(\"Channel 2\", 1, imgs, refresh),\n\t\tchannelControls(\"Channel 3\", 2, imgs, refresh),\n\t\texportBtn,\n\t)\n"""
text, n = controls_re.subn(new_controls, text, count=1)
if n != 1:
    raise SystemExit(f"controls replace count {n}")
path.write_text(text)
