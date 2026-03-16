from pathlib import Path
path = Path("internal/ui/workspace_compose.go")
text = path.read_text()
text = text.replace('\n\tflipCheck := widget.NewCheck("Flip image horizontally", func(bool) {})\n\tflipCheck.SetChecked(true)\n\n\tcontrols :=', '\n\tcontrols :=', 1)
text = text.replace('\tcontrols := container.NewVBox(\n\t\twidget.NewSeparator(),\n\t\tflipCheck,', '\tcontrols := container.NewVBox(\n\t\twidget.NewSeparator(),', 1)
path.write_text(text)
