from pathlib import Path
path = Path("internal/ui/workspace_compose.go")
text = path.read_text()
text = text.replace('vp.blackBox = widget.NewEntry()\n\tvp.blackBox.SetPlaceHolder("000000")\n\tvp.blackBox.SetText("--")',
                    'vp.blackBox = widget.NewEntry()\n\tvp.blackBox.SetPlaceHolder("000000")\n\tvp.blackBox.SetText("--")\n\tvp.blackBox.OnFocusGained = func() { vp.blackBox.SelectAll() }')
text = text.replace('vp.whiteBox = widget.NewEntry()\n\tvp.whiteBox.SetPlaceHolder("000000")\n\tvp.whiteBox.SetText("--")',
                    'vp.whiteBox = widget.NewEntry()\n\tvp.whiteBox.SetPlaceHolder("000000")\n\tvp.whiteBox.SetText("--")\n\tvp.whiteBox.OnFocusGained = func() { vp.whiteBox.SelectAll() }')
path.write_text(text)
