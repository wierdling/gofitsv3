from pathlib import Path
import re
path = Path("internal/ui/workspace_compose.go")
text = path.read_text()
# Insert menu after loadChannel definition
needle = "\tloadChannel := func(idx int) {\n\t\tfd := dialog.NewFileOpen"
if needle not in text:
    raise SystemExit("loadChannel marker not found")
# ensure main menu present
menu_block = """	fileMenu := fyne.NewMenu("File",
		fyne.NewMenuItem("Load Channel 1", func() { loadChannel(0) }),
		fyne.NewMenuItem("Load Channel 2", func() { loadChannel(1) }),
		fyne.NewMenuItem("Load Channel 3", func() { loadChannel(2) }),
	)
	win.SetMainMenu(fyne.NewMainMenu(fileMenu))

	exportBtn := widget.NewButton("Export RGB", func() {
"""
# replace first occurrence of exportBtn assignment
text = re.sub(r"\texportBtn := widget.NewButton\(\"Export RGB\", func\(\) \{", menu_block, text, count=1)
# remove load buttons from controls
text = re.sub(r"\tcontrols := container.NewVBox\(\n\t\twidget.NewButton\(\"Load Channel 1\"[\s\S]*?widget.NewButton\(\"Load Channel 3\"[\s\S]*?\n\t\twidget.NewLabel\(\"Options\"\),\n\t\tflipCheck,",
               '\tcontrols := container.NewVBox(\n\t\twidget.NewLabel("Options"),\n\t\tflipCheck,',
               text,
               count=1,
               flags=re.MULTILINE)
path.write_text(text)
