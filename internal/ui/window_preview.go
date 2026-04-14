package ui

import (
	"fmt"
	"path/filepath"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/histogram"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
	"gofitsv3/internal/utils"
)

// OpenPreviewDialog launches a file picker and opens a FITS file safely.
func OpenPreviewDialog(app fyne.App, parent fyne.Window) {
	fd := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
		if err != nil || r == nil {
			return
		}

		path := r.URI().Path()
		r.Close()
		app.Preferences().SetString("lastDir", filepath.Dir(path))

		// 1. Create and show the window immediately on the main thread
		w := app.NewWindow("FITS Preview: " + filepath.Base(path))
		w.Resize(fyne.NewSize(1000, 700))

		loadingLabel := widget.NewLabel("Reading FITS data... Please wait.")
		loadingLabel.Alignment = fyne.TextAlignCenter
		w.SetContent(container.NewCenter(loadingLabel, widget.NewProgressBarInfinite()))
		w.Show()

		// 2. Offload the heavy file I/O to the background
		go func() {
			img, loadErr := loadImageFromPath(path)

			if loadErr != nil {
				// Safely update the window to show the error
				w.SetContent(container.NewCenter(widget.NewLabel(fmt.Sprintf("Failed to load: %v", loadErr))))
				return
			}

			// 3. Build the UI components and inject them into the active window
			tabs := buildPreviewTabs(img)
			w.SetContent(tabs)
		}()

	}, parent)

	fd.SetFilter(storage.NewExtensionFileFilter([]string{".fits", ".fit", ".fts"}))
	if last := app.Preferences().String("lastDir"); last != "" {
		uri := storage.NewFileURI(last)
		if l, err := storage.ListerForURI(uri); err == nil {
			fd.SetLocation(l)
		}
	}
	fd.SetView(dialog.ListView)

	// Force the dialog to open large
	winSize := parent.Canvas().Size()
	fd.Resize(fyne.NewSize(winSize.Width*0.85, winSize.Height*0.85))

	fd.Show()
}

// buildPreviewTabs constructs the UI controls and returns the tab container
func buildPreviewTabs(img *models.LoadedImage) fyne.CanvasObject {
	vp := newViewport()

	flipCheck := widget.NewCheck("Flip image vertically", func(bool) {})
	flipCheck.SetChecked(true)

	refresh := func() {
		stretched, mask := processing.ApplyStretchParallel(img)
		if flipCheck.Checked {
			stretched = processing.FlipImageData(stretched)
			mask = processing.FlipMask(mask, stretched.Width, stretched.Height)
		}
		vp.image.Image = processing.ToGrayRGBA(stretched, mask)
		vp.origW, vp.origH = stretched.Width, stretched.Height

		// Execute the new mathematical compute
		stats := histogram.Compute(stretched.Pixels)
		vp.bins = stats.Hist

		// Render the true mean and standard deviation
		if vp.StatsLabel != nil {
			vp.StatsLabel.SetText(fmt.Sprintf("Mean: %.4f | Std: %.4f", stats.Mean, stats.Std))
		}

		vp.blackBox.SetText(fmt.Sprintf("%.3f", img.Black))
		vp.whiteBox.SetText(fmt.Sprintf("%.3f", img.White))
		vp.histogram.Refresh()

		if vp.zoomLabel.Selected == "fit in preview" {
			vp.zoom = vp.fitZoom()
		}
		vp.applyZoom()
		vp.image.Refresh()
	}

	flipCheck.OnChanged = func(b bool) { refresh() }

	modeSelect := widget.NewSelect([]string{"Linear", "Log", "Asinh", "Sqrt", "HistEq"}, func(value string) {
		img.Mode = labelToMode(value)
		refresh()
	})
	modeSelect.SetSelected(modeToLabel(img.Mode))

	bgEntry := widget.NewEntry()
	peakEntry := widget.NewEntry()
	sPeakEntry := widget.NewEntry()

	bgEntry.SetText(fmt.Sprintf("%.3f", img.Background))
	peakEntry.SetText(fmt.Sprintf("%.3f", img.Peak))
	sPeakEntry.SetText(fmt.Sprintf("%.3f", img.ScaledPeak))

	showClip := widget.NewCheck("Show clipped", func(v bool) {
		img.ShowClip = v
		refresh()
	})
	showClip.SetChecked(img.ShowClip)

	applyBtn := widget.NewButton("Apply values", func() {
		if v, err := utils.ParseFloat(bgEntry.Text); err == nil {
			img.Background = v
		}
		if v, err := utils.ParseFloat(peakEntry.Text); err == nil {
			img.Peak = v
		}
		if v, err := utils.ParseFloat(sPeakEntry.Text); err == nil {
			img.ScaledPeak = v
		}
		if v, err := utils.ParseFloat(vp.blackBox.Text); err == nil {
			img.Black = v
		}
		if v, err := utils.ParseFloat(vp.whiteBox.Text); err == nil {
			img.White = v
		}
		refresh()
	})

	autoBtn := widget.NewButton("Auto scaling", func() {
		blackVal := img.Black
		if v, err := utils.ParseFloat(vp.blackBox.Text); err == nil {
			blackVal = v
		}
		whiteVal := img.White
		if v, err := utils.ParseFloat(vp.whiteBox.Text); err == nil {
			whiteVal = v
		} else {
			_, whiteVal = processing.AutoLevels(img.HDU.Data.Pixels)
		}
		img.Background = blackVal
		img.Peak = whiteVal
		img.ScaledPeak = 10
		img.White = whiteVal
		img.Black = 0

		vp.blackBox.SetText("0")
		vp.whiteBox.SetText(fmt.Sprintf("%.2f", whiteVal))
		bgEntry.SetText(fmt.Sprintf("%.2f", blackVal))
		peakEntry.SetText(fmt.Sprintf("%.2f", whiteVal))
		sPeakEntry.SetText("10")
		refresh()
	})

	// Send to Compose channel
	channelSelect := widget.NewSelect([]string{"Channel 1", "Channel 2", "Channel 3"}, nil)
	channelSelect.SetSelectedIndex(0)
	sendToChannelBtn := widget.NewButton("Send to Channel", func() {
		if globalSendToChannel == nil {
			return
		}
		// Snapshot current UI values into the image before sending.
		imgCopy := *img
		if v, err := utils.ParseFloat(vp.blackBox.Text); err == nil {
			imgCopy.Black = v
		}
		if v, err := utils.ParseFloat(vp.whiteBox.Text); err == nil {
			imgCopy.White = v
		}
		if v, err := utils.ParseFloat(bgEntry.Text); err == nil {
			imgCopy.Background = v
		}
		if v, err := utils.ParseFloat(peakEntry.Text); err == nil {
			imgCopy.Peak = v
		}
		if v, err := utils.ParseFloat(sPeakEntry.Text); err == nil {
			imgCopy.ScaledPeak = v
		}
		idx := channelSelect.SelectedIndex()
		if idx < 0 {
			idx = 0
		}
		globalSendToChannel(idx, &imgCopy)
	})

	controlsBox := container.NewVBox(
		widget.NewLabel("Stretch Controls"),
		modeSelect,
		widget.NewForm(
			widget.NewFormItem("Background", bgEntry),
			widget.NewFormItem("Peak", peakEntry),
			widget.NewFormItem("Scaled Peak", sPeakEntry),
		),
		showClip,
		flipCheck,
		container.NewHBox(autoBtn, applyBtn),
		widget.NewSeparator(),
		widget.NewLabel("Send to Compose"),
		channelSelect,
		sendToChannelBtn,
	)

	previewSplit := container.NewHSplit(
		container.NewVScroll(controlsBox),
		vp.container,
	)
	previewSplit.SetOffset(0.25)

	lines := utils.FormatHeadersLines(img.Primary, img.HDU.Header)
	headerList := widget.NewList(
		func() int { return len(lines) },
		func() fyne.CanvasObject {
			lbl := widget.NewLabel("")
			lbl.Wrapping = fyne.TextWrapOff
			lbl.TextStyle = fyne.TextStyle{Monospace: true}
			return lbl
		},
		func(id widget.ListItemID, co fyne.CanvasObject) {
			lbl := co.(*widget.Label)
			lbl.SetText(lines[id])
		},
	)

	tabs := container.NewAppTabs(
		container.NewTabItem("Preview", previewSplit),
		container.NewTabItem("Headers", headerList),
	)

	refresh()
	return tabs
}
