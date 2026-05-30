package ui

import (
	"fmt"
	"image"
	"math"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/processing"
)

var starlessDebugWin fyne.Window

func showStarlessDebugViewer(app fyne.App, result *processing.StarlessResult) {
	debugImages, err := processing.BuildStarlessDebugImages(result)
	if err != nil || len(debugImages) == 0 {
		return
	}

	if starlessDebugWin == nil {
		starlessDebugWin = app.NewWindow("Starless Diagnostic Images")
		starlessDebugWin.SetCloseIntercept(func() {
			starlessDebugWin.SetCloseIntercept(nil)
			starlessDebugWin.Close()
			starlessDebugWin = nil
		})
	}
	starlessDebugWin.SetContent(newScrollableDebugImageTabs(debugImages))
	starlessDebugWin.Resize(fyne.NewSize(900, 700))
	starlessDebugWin.Show()
	starlessDebugWin.RequestFocus()
}

func newScrollableDebugImageTabs(debugImages []processing.DebugImage) fyne.CanvasObject {
	content := container.NewMax()
	tabButtons := make([]*widget.Button, len(debugImages))

	selectTab := func(active int) {
		if active < 0 || active >= len(debugImages) {
			return
		}
		for i, btn := range tabButtons {
			if i == active {
				btn.Importance = widget.HighImportance
			} else {
				btn.Importance = widget.LowImportance
			}
			btn.Refresh()
		}
		content.Objects = []fyne.CanvasObject{newDebugImageTabContent(debugImages[active].Image)}
		content.Refresh()
	}

	tabRow := container.NewHBox()
	for i, di := range debugImages {
		idx := i
		btn := widget.NewButton(di.Name, func() {
			selectTab(idx)
		})
		btn.Importance = widget.LowImportance
		tabButtons[i] = btn
		tabRow.Add(btn)
	}
	tabScroll := container.NewHScroll(tabRow)
	tabScroll.SetMinSize(fyne.NewSize(0, tabRow.MinSize().Height+themeScrollBarReserve()))
	selectTab(0)

	return container.NewBorder(tabScroll, nil, nil, nil, content)
}

func themeScrollBarReserve() float32 {
	return 16
}

func newDebugImageTabContent(img image.Image) fyne.CanvasObject {
	cimg := canvas.NewImageFromImage(img)
	cimg.FillMode = canvas.ImageFillContain

	var origW, origH int
	if img != nil {
		b := img.Bounds()
		origW = b.Dx()
		origH = b.Dy()
	}

	scroll := container.NewScroll(cimg)
	scroll.SetMinSize(fyne.NewSize(400, 400))

	zoom := 1.0
	fitMode := true
	zoomLabel := widget.NewLabel("Fit")

	applyZoom := func() {
		if fitMode || origW == 0 || origH == 0 {
			cimg.SetMinSize(fyne.NewSize(0, 0))
		} else {
			w := float32(math.Round(float64(origW) * zoom))
			h := float32(math.Round(float64(origH) * zoom))
			cimg.SetMinSize(fyne.NewSize(w, h))
		}
		cimg.Refresh()
		scroll.Refresh()
	}

	startZoomFromFit := func() float64 {
		sz := scroll.Size()
		if origW > 0 && origH > 0 && sz.Width > 1 && sz.Height > 1 {
			return math.Min(float64(sz.Width)/float64(origW), float64(sz.Height)/float64(origH))
		}
		return 1.0
	}

	zoomOut := widget.NewButton("-", func() {
		if fitMode {
			zoom = startZoomFromFit()
		}
		fitMode = false
		zoom = math.Max(0.05, zoom*0.85)
		zoomLabel.SetText(fmt.Sprintf("%d%%", int(math.Round(zoom*100))))
		applyZoom()
	})
	zoomIn := widget.NewButton("+", func() {
		if fitMode {
			zoom = startZoomFromFit()
		}
		fitMode = false
		zoom = math.Min(20.0, zoom/0.85)
		zoomLabel.SetText(fmt.Sprintf("%d%%", int(math.Round(zoom*100))))
		applyZoom()
	})
	fitBtn := widget.NewButton("Fit", func() {
		fitMode = true
		zoom = 1.0
		zoomLabel.SetText("Fit")
		applyZoom()
	})

	toolbar := container.NewHBox(zoomOut, zoomLabel, zoomIn, hpad(8), fitBtn, layout.NewSpacer())
	return container.NewBorder(toolbar, nil, nil, nil, scroll)
}
