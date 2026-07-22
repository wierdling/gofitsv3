package ui

import (
	"fmt"
	"image/color"
	"path/filepath"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"
)

var composeMagicAssignments = []string{
	string(composeMagicBlue),
	string(composeMagicGreen),
	string(composeMagicRed),
	string(composeMagicCustom),
}

var composeMagicPresets = []string{"Balanced", "Nebula", "Galaxy"}

func showComposeMagicFolderPicker(app fyne.App, win fyne.Window, onConfirm func(string, []composeMagicRow) error) {
	folderDialog := dialog.NewFolderOpen(func(folder fyne.ListableURI, err error) {
		if err != nil {
			dialog.ShowError(err, win)
			return
		}
		if folder == nil {
			return
		}
		dir := folder.Path()
		app.Preferences().SetString("lastDir", dir)
		progress := dialog.NewCustomWithoutButtons(
			"Scanning Filter Files",
			container.NewVBox(widget.NewLabel("Finding named _drz.fits, _driz.fits, or _drizzle.fits filter files...")),
			win,
		)
		progress.Show()
		go func() {
			files, scanErr := discoverComposeMagicFiles(dir)
			fyne.Do(func() {
				progress.Hide()
				if scanErr != nil {
					dialog.ShowError(scanErr, win)
					return
				}
				if len(files) == 0 {
					dialog.ShowInformation(
						"No Filter Files",
						"No files matching F<filter>_<name>_drz.fits, _driz.fits, or _drizzle.fits were found.",
						win,
					)
					return
				}
				showComposeMagicDialog(win, defaultComposeMagicRows(files), onConfirm)
			})
		}()
	}, win)
	if lastDir := app.Preferences().String("lastDir"); lastDir != "" {
		uri := storage.NewFileURI(lastDir)
		if location, err := storage.ListerForURI(uri); err == nil {
			folderDialog.SetLocation(location)
		}
	}
	sizeFileDialog(folderDialog)
	folderDialog.Show()
}

func showComposeMagicDialog(win fyne.Window, rows []composeMagicRow, onConfirm func(string, []composeMagicRow) error) {
	magicSelect := widget.NewSelect(composeMagicPresets, nil)
	magicSelect.SetSelected(composeMagicPresets[0])

	rowList := container.NewVBox()
	for i := range rows {
		i := i
		assignmentSelect := widget.NewSelect(composeMagicAssignments, nil)
		assignmentSelect.SetSelected(string(rows[i].Assignment))

		customColor := rows[i].Color
		customColor.A = 255
		swatch := canvas.NewRectangle(customColor)
		swatch.StrokeColor = color.NRGBA{R: 100, G: 100, B: 100, A: 255}
		swatch.StrokeWidth = 1
		swatchBox := container.NewGridWrap(fyne.NewSize(30, 22), swatch)
		var openColorPicker func()
		chooseButton := widget.NewButton("Choose...", func() { openColorPicker() })
		customControls := container.NewHBox(swatchBox, chooseButton)
		openColorPicker = func() {
			picker := dialog.NewColorPicker("Custom Channel Color", rows[i].File.Name, func(chosen color.Color) {
				normalized := opaqueComposeMagicColor(chosen)
				customColor = normalized
				rows[i].Color = normalized
				swatch.FillColor = normalized
				swatch.Refresh()
			}, win)
			picker.Advanced = true
			picker.SetColor(customColor)
			picker.Show()
		}
		if rows[i].Assignment != composeMagicCustom {
			customControls.Hide()
		}
		assignmentSelect.OnChanged = func(selected string) {
			assignment := composeMagicAssignment(selected)
			rows[i].Assignment = assignment
			if assignment == composeMagicCustom {
				rows[i].Color = customColor
				customControls.Show()
				openColorPicker()
				return
			}
			rows[i].Color = composeMagicPresetColor(assignment)
			customControls.Hide()
		}

		rowList.Add(newComposeMagicDialogRow(
			widget.NewLabel(rows[i].File.Filter),
			widget.NewLabel(filepath.Base(rows[i].File.Path)),
			assignmentSelect,
			customControls,
		))
	}

	rowsScroll := newComposeMagicRowsScroll(rowList)
	var batchDialog *dialog.CustomDialog
	loadButton := widget.NewButton("Load & Compose", func() {
		if err := validateComposeMagicPlan(rows); err != nil {
			dialog.ShowError(err, win)
			return
		}
		confirmedRows := append([]composeMagicRow(nil), rows...)
		if onConfirm != nil {
			if err := onConfirm(magicSelect.Selected, confirmedRows); err != nil {
				dialog.ShowError(err, win)
				return
			}
		}
		batchDialog.Hide()
	})
	loadButton.Importance = widget.HighImportance
	cancelButton := widget.NewButton("Cancel", func() { batchDialog.Hide() })
	content := container.NewBorder(
		container.NewVBox(
			widget.NewForm(widget.NewFormItem("Magic", magicSelect)),
			widget.NewSeparator(),
			newComposeMagicDialogRow(
				widget.NewLabel(fmt.Sprintf("Filter (%d found)", len(rows))),
				widget.NewLabel("File"),
				widget.NewLabel("Channel"),
				widget.NewLabel("Custom Color"),
			),
		),
		container.NewGridWithColumns(2, cancelButton, loadButton),
		nil,
		nil,
		rowsScroll,
	)
	batchDialog = dialog.NewCustomWithoutButtons("Load Filter Set", content, win)
	batchDialog.Resize(fyne.NewSize(780, 500))
	batchDialog.Show()
}

func newComposeMagicDialogRow(objects ...fyne.CanvasObject) *fyne.Container {
	return container.NewGridWithColumns(4, objects...)
}

func newComposeMagicRowsScroll(rows fyne.CanvasObject) *container.Scroll {
	// A grid expands all of its rows equally when it receives extra height.
	// The VBox keeps the grid at its minimum height so the scroll viewport does
	// not turn a short filter list into a handful of oversized controls.
	rowsContent := container.NewVBox(rows)
	rowsScroll := container.NewVScroll(rowsContent)
	rowsScroll.SetMinSize(fyne.NewSize(720, 360))
	return rowsScroll
}

func opaqueComposeMagicColor(chosen color.Color) color.NRGBA {
	normalized := color.NRGBAModel.Convert(chosen).(color.NRGBA)
	normalized.A = 255
	return normalized
}
