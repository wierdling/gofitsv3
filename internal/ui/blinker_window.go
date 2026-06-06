package ui

import (
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
	"gofitsv3/internal/stretch"
)

var blinkerWin fyne.Window

type blinkFrame struct {
	Name    string
	Image   *image.RGBA
	Checked bool
}

func showBlinkerWindow(app fyne.App, debugDir string, black, white, bg, peak, scaledPeak float64, stretchMode stretch.Mode) {
	if blinkerWin != nil {
		blinkerWin.RequestFocus()
		return
	}

	files, err := os.ReadDir(debugDir)
	if err != nil {
		return
	}

	var frames []*blinkFrame
	for _, f := range files {
		if f.IsDir() || !strings.HasPrefix(f.Name(), "debug_") || filepath.Ext(f.Name()) != ".fits" {
			continue
		}
		path := filepath.Join(debugDir, f.Name())
		// Read FITS
		fitsFile, err := fitsio.LoadFile(path)
		if err != nil || len(fitsFile.HDUs) == 0 {
			continue
		}
		hdu := fitsFile.HDUs[0]

		img := &models.LoadedImage{
			HDU:        hdu,
			Mode:       stretchMode,
			Black:      black,
			White:      white,
			Background: bg,
			Peak:       peak,
			ScaledPeak: scaledPeak,
		}
		stretched, mask := processing.ApplyStretchParallel(img)
		if mask == nil {
			mask = make([]byte, len(stretched.Pixels))
		}
		rgba := processing.ToGrayRGBA(stretched, mask)

		name := f.Name()
		if len(name) > 11 {
			name = name[6 : len(name)-5] // trim "debug_" and ".fits"
		}
		frames = append(frames, &blinkFrame{
			Name:    name,
			Image:   rgba,
			Checked: true,
		})
	}

	if len(frames) == 0 {
		return
	}

	blinkerWin = app.NewWindow("Debug Blinker")

	// State for blinker
	var mu sync.Mutex
	currentIndex := 0
	blinkIntervalMs := int64(500)
	isPaused := false
	stopCh := make(chan struct{})

	blinkerWin.SetCloseIntercept(func() {
		close(stopCh)
		blinkerWin.SetCloseIntercept(nil)
		blinkerWin.Close()
		blinkerWin = nil
	})

	var cimgs []*canvas.Image
	imgStack := container.NewMax()

	bounds := frames[0].Image.Bounds()
	origW := float32(bounds.Dx())
	origH := float32(bounds.Dy())

	for i, fr := range frames {
		ci := canvas.NewImageFromImage(fr.Image)
		ci.FillMode = canvas.ImageFillContain
		ci.SetMinSize(fyne.NewSize(origW, origH))
		if i != 0 {
			ci.Hidden = true
		}
		cimgs = append(cimgs, ci)
		imgStack.Add(ci)
	}

	overlay := newViewerInteractionLayer()
	imgStack.Add(overlay)
	imgContainer := container.NewScroll(imgStack)
	overlay.scroll = imgContainer

	// Sidebar with checks
	checkList := container.NewVBox()
	for _, fr := range frames {
		fr := fr
		check := widget.NewCheck(fr.Name, func(v bool) {
			mu.Lock()
			fr.Checked = v
			mu.Unlock()
		})
		check.SetChecked(true)
		checkList.Add(check)
	}
	checkScroll := container.NewVScroll(checkList)

	// Slider for interval
	slider := widget.NewSlider(50, 2000)
	slider.SetValue(500)
	sliderValLabel := widget.NewLabel("500 ms")
	slider.OnChanged = func(v float64) {
		mu.Lock()
		blinkIntervalMs = int64(v)
		sliderValLabel.SetText(fmt.Sprintf("%d ms", int(v)))
		mu.Unlock()
	}

	// Slider for Zoom
	zoomSlider := widget.NewSlider(10, 400)
	zoomSlider.SetValue(100)
	zoomValLabel := widget.NewLabel("100 %")
	zoomSlider.OnChanged = func(v float64) {
		zoom := v / 100.0
		newSize := fyne.NewSize(origW*float32(zoom), origH*float32(zoom))
		for _, ci := range cimgs {
			ci.SetMinSize(newSize)
		}
		imgContainer.Refresh()
		zoomValLabel.SetText(fmt.Sprintf("%d %%", int(v)))
	}

	pauseCheck := widget.NewCheck("Pause", func(v bool) {
		mu.Lock()
		isPaused = v
		mu.Unlock()
	})

	advanceFrame := func() {
		mu.Lock()
		checkedCount := 0
		for _, fr := range frames {
			if fr.Checked {
				checkedCount++
			}
		}

		targetIdx := -1
		if checkedCount > 0 {
			startIdx := currentIndex
			for {
				currentIndex = (currentIndex + 1) % len(frames)
				if frames[currentIndex].Checked {
					targetIdx = currentIndex
					break
				}
				if currentIndex == startIdx {
					break
				}
			}
		}
		mu.Unlock()

		if targetIdx >= 0 {
			fyne.Do(func() {
				for i, ci := range cimgs {
					if i == targetIdx {
						if ci.Hidden {
							ci.Hidden = false
							ci.Refresh()
						}
					} else {
						if !ci.Hidden {
							ci.Hidden = true
							ci.Refresh()
						}
					}
				}
			})
		}
	}

	nextBtn := widget.NewButton("Next >", func() {
		pauseCheck.SetChecked(true)
		advanceFrame()
	})

	speedControls := container.NewHBox(pauseCheck, nextBtn, widget.NewLabel("Blink Speed:"))
	speedRow := container.NewBorder(nil, nil, speedControls, sliderValLabel, slider)
	zoomRow := container.NewBorder(nil, nil, widget.NewLabel("Zoom:"), zoomValLabel, zoomSlider)

	bottomBar := container.NewVBox(speedRow, zoomRow)

	mainLayout := container.NewBorder(nil, bottomBar, checkScroll, nil, imgContainer)
	blinkerWin.SetContent(mainLayout)
	blinkerWin.Resize(fyne.NewSize(1000, 800))
	blinkerWin.Show()

	go func() {
		for {
			select {
			case <-stopCh:
				return
			default:
				mu.Lock()
				interval := time.Duration(blinkIntervalMs) * time.Millisecond
				paused := isPaused
				mu.Unlock()

				if !paused {
					advanceFrame()
				}

				time.Sleep(interval)
			}
		}
	}()
}
