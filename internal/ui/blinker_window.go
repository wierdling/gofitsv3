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
	Path    string
	Image   *image.RGBA
	ci      *canvas.Image
	Checked bool
}

func blinkLoadIsCurrent(closed bool, windowGen, currentWindowGen, frameGen, currentFrameGen uint64, checked bool) bool {
	return !closed && windowGen == currentWindowGen && frameGen == currentFrameGen && checked
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

	// Enumerate candidate frames by filename only. Nothing is read into memory
	// until the user selects (checks) a frame.
	var frames []*blinkFrame
	for _, f := range files {
		if f.IsDir() || !strings.HasPrefix(f.Name(), "debug_") || filepath.Ext(f.Name()) != ".fits" {
			continue
		}
		name := f.Name()
		if len(name) > 11 {
			name = name[6 : len(name)-5] // trim "debug_" and ".fits"
		}
		frames = append(frames, &blinkFrame{
			Name:    name,
			Path:    filepath.Join(debugDir, f.Name()),
			Checked: false,
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
	loadGen := make([]uint64, len(frames))
	windowGen := uint64(0)
	closed := false

	// cimgs[i] mirrors frames[i].ci so the blink/zoom loops can iterate cheaply.
	// Entries are nil until the corresponding frame is loaded.
	cimgs := make([]*canvas.Image, len(frames))

	// Image sizing is discovered from the first loaded frame.
	var origW, origH float32
	haveSize := false
	zoomFactor := float32(1)

	blinkerWin.SetCloseIntercept(func() {
		mu.Lock()
		if closed {
			mu.Unlock()
			return
		}
		closed = true
		windowGen++
		mu.Unlock()
		close(stopCh)
		blinkerWin.SetCloseIntercept(nil)
		blinkerWin.Close()
		blinkerWin = nil
	})

	// imgLayer holds the (lazily added) frame images; overlay stays on top.
	imgLayer := container.NewMax()
	overlay := newViewerInteractionLayer()
	imgStack := container.NewMax(imgLayer, overlay)
	imgContainer := container.NewScroll(imgStack)
	overlay.scroll = imgContainer

	// advanceFrame shows the next selected+loaded frame, hiding the rest.
	advanceFrame := func() {
		mu.Lock()
		if closed {
			mu.Unlock()
			return
		}
		var disp []int
		for i, fr := range frames {
			if fr.Checked && fr.ci != nil {
				disp = append(disp, i)
			}
		}
		targetIdx := -1
		if len(disp) > 0 {
			targetIdx = disp[0]
			for _, idx := range disp {
				if idx > currentIndex {
					targetIdx = idx
					break
				}
			}
			currentIndex = targetIdx
		}
		mu.Unlock()

		if targetIdx < 0 {
			return
		}
		fyne.Do(func() {
			mu.Lock()
			if closed {
				mu.Unlock()
				return
			}
			defer mu.Unlock()
			for i, ci := range cimgs {
				if ci == nil {
					continue
				}
				if i == targetIdx {
					if ci.Hidden {
						ci.Hidden = false
						ci.Refresh()
					}
				} else if !ci.Hidden {
					ci.Hidden = true
					ci.Refresh()
				}
			}
		})
	}

	// loadFrame reads + stretches a frame's FITS and installs its canvas image.
	// Runs on a background goroutine; UI mutations are marshalled onto the main thread.
	loadFrame := func(i int) {
		mu.Lock()
		if closed || !frames[i].Checked {
			mu.Unlock()
			return
		}
		gen := loadGen[i]
		window := windowGen
		mu.Unlock()
		fr := frames[i]
		fitsFile, err := fitsio.LoadFile(fr.Path)
		if err != nil || len(fitsFile.HDUs) == 0 {
			return
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

		fyne.Do(func() {
			mu.Lock()
			if !blinkLoadIsCurrent(closed, window, windowGen, gen, loadGen[i], fr.Checked) {
				// Deselected while loading; discard.
				mu.Unlock()
				return
			}
			if !haveSize {
				b := rgba.Bounds()
				origW = float32(b.Dx())
				origH = float32(b.Dy())
				haveSize = true
			}
			ci := canvas.NewImageFromImage(rgba)
			ci.FillMode = canvas.ImageFillContain
			ci.SetMinSize(fyne.NewSize(origW*zoomFactor, origH*zoomFactor))
			ci.Hidden = true
			fr.Image = rgba
			fr.ci = ci
			cimgs[i] = ci
			imgLayer.Add(ci)
			mu.Unlock()
			imgLayer.Refresh()
			advanceFrame()
		})
	}

	// unloadFrame drops a frame's image and removes its canvas image, freeing memory.
	unloadFrame := func(i int) {
		mu.Lock()
		fr := frames[i]
		ci := fr.ci
		fr.ci = nil
		fr.Image = nil
		cimgs[i] = nil
		mu.Unlock()
		if ci != nil {
			fyne.Do(func() {
				mu.Lock()
				if closed {
					mu.Unlock()
					return
				}
				mu.Unlock()
				imgLayer.Remove(ci)
				imgLayer.Refresh()
			})
		}
	}

	// Sidebar with checks (all unselected by default).
	checkList := container.NewVBox()
	for i, fr := range frames {
		i, fr := i, fr
		check := widget.NewCheck(fr.Name, func(v bool) {
			mu.Lock()
			fr.Checked = v
			loadGen[i]++
			mu.Unlock()
			if v {
				go loadFrame(i)
			} else {
				unloadFrame(i)
			}
		})
		check.SetChecked(false)
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
		mu.Lock()
		zoomFactor = float32(v / 100.0)
		newSize := fyne.NewSize(origW*zoomFactor, origH*zoomFactor)
		for _, ci := range cimgs {
			if ci != nil {
				ci.SetMinSize(newSize)
			}
		}
		mu.Unlock()
		imgContainer.Refresh()
		zoomValLabel.SetText(fmt.Sprintf("%d %%", int(v)))
	}

	pauseCheck := widget.NewCheck("Pause", func(v bool) {
		mu.Lock()
		isPaused = v
		mu.Unlock()
	})

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
