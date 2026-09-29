package ui

import (
	"fmt"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/processing"
)

func (ws *composeWorkspace) crossChannelClean() {
	if ws.imgs[0] == nil || ws.imgs[1] == nil || ws.imgs[2] == nil {
		dialog.ShowInformation("Missing Channels", "Load all three channels before cleaning.", ws.win)
		return
	}

	savedStates := ws.captureViewportStates()
	if ws.largeMode && ws.largeStore != nil {
		ws.crossChannelCleanLarge(savedStates)
		return
	}

	sharedWidth := 0
	sharedHeight := 0
	for i := 0; i < 3; i++ {
		data := ws.imgs[i].HDU.Data
		if data.Width <= 0 || data.Height <= 0 {
			dialog.ShowInformation(
				"Invalid Channel Data",
				fmt.Sprintf("Channel %d has invalid dimensions %dx%d.", i+1, data.Width, data.Height),
				ws.win,
			)
			return
		}
		usableHeight := len(data.Pixels) / data.Width
		if usableHeight <= 0 {
			dialog.ShowInformation(
				"Invalid Channel Data",
				fmt.Sprintf("Channel %d does not have enough pixels for its declared width %d.", i+1, data.Width),
				ws.win,
			)
			return
		}
		if usableHeight > data.Height {
			usableHeight = data.Height
		}
		if i == 0 || data.Width < sharedWidth {
			sharedWidth = data.Width
		}
		if i == 0 || usableHeight < sharedHeight {
			sharedHeight = usableHeight
		}
	}
	if sharedWidth <= 0 || sharedHeight <= 0 {
		dialog.ShowInformation("Invalid Channel Data", "Could not determine a shared image region to clean.", ws.win)
		return
	}

	progressDialog := dialog.NewCustom("Cleaning", "Building star mask and removing artifacts...", widget.NewProgressBarInfinite(), ws.win)
	progressDialog.Show()

	go func() {
		cropTopLeft := func(data fitsio.ImageData, width, height int) []float32 {
			cropped := make([]float32, width*height)
			for y := 0; y < height; y++ {
				srcStart := y * data.Width
				dstStart := y * width
				copy(cropped[dstStart:dstStart+width], data.Pixels[srcStart:srcStart+width])
			}
			return cropped
		}
		pasteTopLeft := func(dst []float32, dstWidth int, src []float32, width, height int) {
			for y := 0; y < height; y++ {
				dstStart := y * dstWidth
				srcStart := y * width
				copy(dst[dstStart:dstStart+width], src[srcStart:srcStart+width])
			}
		}

		channels := make([][]float32, 0, 3)
		sigmas := make([]float64, 0, 3)
		for i := 0; i < 3; i++ {
			cropped := cropTopLeft(ws.imgs[i].HDU.Data, sharedWidth, sharedHeight)
			channels = append(channels, cropped)
			_, sig := processing.EstimateBackground(cropped)
			sigmas = append(sigmas, sig)
		}

		starMasks := processing.BuildLayerStarMasks(channels, sharedWidth, sharedHeight, sigmas)
		passes := 2

		cleaned := make([][]float32, 3)
		var wg sync.WaitGroup
		for i := 0; i < 3; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				cleaned[idx] = processing.RemoveCosmicRays(channels[idx], sharedWidth, sharedHeight, sigmas[idx], passes, starMasks[idx])
			}(i)
		}
		wg.Wait()

		for i := 0; i < 3; i++ {
			out := make([]float32, len(ws.imgs[i].HDU.Data.Pixels))
			copy(out, ws.imgs[i].HDU.Data.Pixels)
			pasteTopLeft(out, ws.imgs[i].HDU.Data.Width, cleaned[i], sharedWidth, sharedHeight)
			ws.imgs[i].HDU.Data.Pixels = out
			clearComposeOrigPixels(&ws.origPixels, i)
		}

		fyne.Do(func() {
			ws.win.Canvas().Refresh(ws.win.Content())
			progressDialog.Hide()
			ws.refresh()
			ws.restoreViewportStates(savedStates)
			dialog.ShowInformation("Complete", fmt.Sprintf("Star masks generated and cosmic rays eradicated in the shared %dx%d region.", sharedWidth, sharedHeight), ws.win)
		})
	}()
}
