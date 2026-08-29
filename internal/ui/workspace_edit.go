package ui

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/export"
	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
)

// globalExportToEdit is set by app.go and called by the compose workspace.
type editDiskSource struct {
	planes        [3]string
	width, height int
	levels        models.RgbLevels
	root          string
	cleanupOnce   sync.Once
	store         *editDiskStore
}

type editImageHandoff struct {
	memory image.Image
	disk   *editDiskSource
}

func (h editImageHandoff) valid() bool { return (h.memory != nil) != (h.disk != nil) }

var globalExportToEdit func(editImageHandoff) error
var globalEditCleanup func()

var editZoomPresets = []string{"fit", "10%", "25%", "50%", "75%", "100%", "150%", "200%", "300%", "400%"}

// editWorkspaceState holds all mutable state for the edit tab.
type editWorkspaceState struct {
	win fyne.Window
	// base is the last loaded or saved image. working is the mutable image
	// displayed in Edit. Keeping them separate makes Reset a memory copy rather
	// than a reload, and prevents adjustments from being applied cumulatively.
	base       *image.RGBA
	working    *image.RGBA
	diskSource *editDiskSource
	origW      int
	origH      int
	zoom       float64

	canvasImg *canvas.Image
	imgScroll *container.Scroll

	// per-channel histograms (drawn with min/max marker lines)
	rgbHists [3]*canvas.Raster
	rgbBins  [3][256]int

	// zoom controls
	zoomSelect *SafeSelect
	customZoom string

	// level sliders (min/max per channel, 0-255)
	rMinSlider *widget.Slider
	rMaxSlider *widget.Slider
	gMinSlider *widget.Slider
	gMaxSlider *widget.Slider
	bMinSlider *widget.Slider
	bMaxSlider *widget.Slider

	// curves widget and channel selector
	curves        *curvesWidget
	curvesChannel *SafeSelect

	// sharpening sliders
	sharpSlider       *widget.Slider // strength 0-3
	sharpRadiusSlider *widget.Slider // radius 0.5-10

	loadedName string // base filename of the last loaded image

	// heal tool
	healOverlay     *healLayer
	healActive      bool
	healBrushSlider *widget.Slider
	healStatusLabel *widget.Label
	healUndo        *image.RGBA // single-level undo buffer

	editTabs             *container.AppTabs
	applyButton          *widget.Button
	resetButton          *widget.Button
	cleanTab             *container.TabItem
	cleanStatusLabel     *widget.Label
	cleanBlobSlider      *widget.Slider
	cleanIntensitySlider *widget.Slider

	// crop tool
	cropOverlay     *cropLayer
	cropActive      bool
	cropStatusLabel *widget.Label
	cropMin         image.Point // selection in image pixels (top-left)
	cropMax         image.Point // selection in image pixels (bottom-right)
	cropHasSel      bool
	jobGeneration   uint64
}

func (es *editWorkspaceState) applyEdits() {
	if es.diskSource != nil {
		if es.diskSource.store == nil {
			return
		}
		recipe := es.diskEditRecipe()
		generation := es.jobGeneration + 1
		es.jobGeneration = generation
		go func() {
			result, err := es.diskSource.store.Render(context.Background(), recipe, 1600)
			if err != nil {
				return
			}
			fyne.Do(func() {
				if es.diskSource == nil || es.jobGeneration != generation {
					return
				}
				planes, _, _, _ := es.diskSource.store.Source()
				es.diskSource.planes = planes
				es.diskSource.levels = models.RgbLevels{Max: [3]float64{255, 255, 255}}
				es.canvasImg.Image = result.Preview
				es.canvasImg.Refresh()
				es.refreshHistograms(result.Preview)
			})
		}()
		return
	}
	if es.base == nil {
		return
	}
	es.jobGeneration++
	generation := es.jobGeneration
	base := es.base
	prog := dialog.NewCustom("Applying", "Please wait…", widget.NewProgressBarInfinite(), es.win)
	prog.Show()

	// Snapshot LUTs and slider values on the UI goroutine before handing off.
	rMin, rMax := es.rMinSlider.Value, es.rMaxSlider.Value
	gMin, gMax := es.gMinSlider.Value, es.gMaxSlider.Value
	bMin, bMax := es.bMinSlider.Value, es.bMaxSlider.Value
	rLUT, gLUT, bLUT := es.curves.ToLUT(0), es.curves.ToLUT(1), es.curves.ToLUT(2)
	strength, radius := es.sharpSlider.Value, es.sharpRadiusSlider.Value

	go func() {
		img := processing.ApplyEditLevels(base, rMin, rMax, gMin, gMax, bMin, bMax)
		img = processing.ApplyCurvesRGBA(img, rLUT, gLUT, bLUT)
		img = processing.SharpenRGBA(img, strength, radius)

		fyne.Do(func() {
			prog.Hide()
			if es.jobGeneration != generation || es.base != base || es.diskSource != nil {
				return
			}
			es.working = img
			es.healUndo = nil
			es.canvasImg.Image = img
			es.canvasImg.Refresh()
		})
	}()
}

func (es *editWorkspaceState) diskEditRecipe() processing.DiskEditRecipe {
	recipe := processing.DiskEditRecipe{Min: [3]float64{es.rMinSlider.Value, es.gMinSlider.Value, es.bMinSlider.Value}, Max: [3]float64{es.rMaxSlider.Value, es.gMaxSlider.Value, es.bMaxSlider.Value}, Strength: es.sharpSlider.Value, Radius: es.sharpRadiusSlider.Value}
	for c := range recipe.Curves {
		recipe.Curves[c] = es.curves.ToLUT(c)
	}
	return recipe
}

// updateHealBrushScreenRadius syncs the overlay's screen-space circle to the current brush size and zoom.
// The slider value is the brush diameter in image pixels, so screen radius = (diameter/2) * zoom.
func (es *editWorkspaceState) updateHealBrushScreenRadius() {
	if es.healOverlay == nil || es.healBrushSlider == nil {
		return
	}
	screenRadius := float32((es.healBrushSlider.Value / 2.0) * es.zoom)
	if screenRadius < 1 {
		screenRadius = 1
	}
	es.healOverlay.SetBrushRadius(screenRadius)
}

// screenToImagePt converts an overlay widget position to an image pixel coordinate.
func (es *editWorkspaceState) screenToImagePt(pos fyne.Position) (image.Point, bool) {
	if es.zoom <= 0 || es.origW == 0 || es.origH == 0 {
		return image.Point{}, false
	}
	// canvasImg uses ImageFillContain inside a NewMax container, so when the
	// displayed image (origW*zoom x origH*zoom) is smaller than the overlay in
	// either axis it is centered with a letterbox margin. Back that offset out
	// before mapping, otherwise the selection maps above/left of the real pixels.
	dispW := float32(es.origW) * float32(es.zoom)
	dispH := float32(es.origH) * float32(es.zoom)
	sz := es.canvasImg.Size()
	var offX, offY float32
	if sz.Width > dispW {
		offX = (sz.Width - dispW) / 2
	}
	if sz.Height > dispH {
		offY = (sz.Height - dispH) / 2
	}
	adj := fyne.NewPos(pos.X-offX, pos.Y-offY)
	pt, ok := mapViewportPositionToImage(adj, fyne.NewPos(0, 0), es.zoom, es.origW, es.origH, false)
	return image.Pt(pt.X, pt.Y), ok
}

// toRGBA returns the image as *image.RGBA, converting if necessary.
func toRGBA(img image.Image) *image.RGBA {
	if img == nil {
		return nil
	}
	if r, ok := img.(*image.RGBA); ok {
		return r
	}
	b := img.Bounds()
	r := image.NewRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r.Set(x, y, img.At(x, y))
		}
	}
	return r
}

func cloneEditRGBA(src *image.RGBA) *image.RGBA {
	if src == nil {
		return nil
	}
	dst := image.NewRGBA(src.Bounds())
	copy(dst.Pix, src.Pix)
	return dst
}

func (es *editWorkspaceState) resetWorkingToBase() bool {
	if es.base == nil {
		return false
	}
	es.working = cloneEditRGBA(es.base)
	es.healUndo = nil
	es.jobGeneration++
	return true
}

func (es *editWorkspaceState) commitWorkingToBase() {
	if es.working != nil {
		es.base = cloneEditRGBA(es.working)
	}
}

func (es *editWorkspaceState) resetAdjustmentControls() {
	for _, slider := range []*widget.Slider{es.rMinSlider, es.rMaxSlider, es.gMinSlider, es.gMaxSlider, es.bMinSlider, es.bMaxSlider} {
		if slider != nil {
			slider.SetValue(0)
		}
	}
	if es.rMaxSlider != nil {
		es.rMaxSlider.SetValue(255)
	}
	if es.gMaxSlider != nil {
		es.gMaxSlider.SetValue(255)
	}
	if es.bMaxSlider != nil {
		es.bMaxSlider.SetValue(255)
	}
	if es.curves != nil {
		es.curves.Reset()
	}
	if es.curvesChannel != nil {
		es.curvesChannel.SetSelected("All")
	}
	if es.sharpSlider != nil {
		es.sharpSlider.SetValue(0)
	}
	if es.sharpRadiusSlider != nil {
		es.sharpRadiusSlider.SetValue(1.0)
	}
}

// doHealStroke performs the heal for the full destination stroke.
func (es *editWorkspaceState) doHealStroke(srcScreen fyne.Position, dstScreens []fyne.Position) {
	if es.diskSource != nil {
		disk := es.diskSource
		if disk.store == nil {
			return
		}
		srcPt, ok := es.screenToImagePt(srcScreen)
		if !ok {
			return
		}
		dsts := make([]image.Point, 0, len(dstScreens))
		for _, p := range dstScreens {
			if q, valid := es.screenToImagePt(p); valid {
				dsts = append(dsts, q)
			}
		}
		if len(dsts) == 0 {
			return
		}
		radius := int(math.Round(es.healBrushSlider.Value / 2))
		if radius < 1 {
			radius = 1
		}
		es.jobGeneration++
		generation := es.jobGeneration
		prog := dialog.NewCustom("Healing", "Healing full-resolution disk source...", widget.NewProgressBarInfinite(), es.win)
		prog.Show()
		go func() {
			result, err := disk.store.Heal(context.Background(), processing.DiskHealStroke{Source: srcPt, Destinations: dsts, Radius: radius}, 1600)
			fyne.Do(func() {
				prog.Hide()
				if err != nil || es.diskSource != disk || es.jobGeneration != generation {
					return
				}
				planes, w, h, _ := disk.store.Source()
				disk.planes, disk.width, disk.height = planes, w, h
				disk.levels = models.RgbLevels{Max: [3]float64{255, 255, 255}}
				es.origW, es.origH = w, h
				es.canvasImg.Image = result.Preview
				es.canvasImg.Refresh()
				es.refreshHistograms(result.Preview)
				es.cleanStatusLabel.SetText("Heal applied at full resolution. One Heal undo is available.")
			})
		}()
		return
	}
	rgba := es.working
	if rgba == nil {
		return
	}
	srcPt, srcOK := es.screenToImagePt(srcScreen)
	if !srcOK {
		return
	}
	dstPts := make([]image.Point, 0, len(dstScreens))
	for _, ds := range dstScreens {
		pt, ok := es.screenToImagePt(ds)
		if ok {
			dstPts = append(dstPts, pt)
		}
	}
	if len(dstPts) == 0 {
		return
	}
	radius := int(math.Round(es.healBrushSlider.Value / 2.0))
	if radius < 1 {
		radius = 1
	}

	// Save undo snapshot before modifying.
	undo := image.NewRGBA(rgba.Bounds())
	copy(undo.Pix, rgba.Pix)
	es.healUndo = undo

	healed := applyHealStroke(rgba, srcPt, dstPts, radius)
	es.working = healed
	es.commitWorkingToBase()
	es.resetAdjustmentControls()
	es.jobGeneration++
	es.canvasImg.Image = healed
	es.canvasImg.Refresh()
}

// undoHeal rolls back the last heal operation.
func (es *editWorkspaceState) undoHeal() {
	if es.diskSource != nil {
		disk := es.diskSource
		if disk.store == nil {
			return
		}
		es.jobGeneration++
		generation := es.jobGeneration
		go func() {
			result, ok, err := disk.store.UndoHeal(context.Background(), 1600)
			fyne.Do(func() {
				if err != nil || !ok || es.diskSource != disk || es.jobGeneration != generation {
					return
				}
				planes, w, h, _ := disk.store.Source()
				disk.planes, disk.width, disk.height = planes, w, h
				es.origW, es.origH = w, h
				es.canvasImg.Image = result.Preview
				es.canvasImg.Refresh()
				es.refreshHistograms(result.Preview)
				es.cleanStatusLabel.SetText("Heal undone.")
			})
		}()
		return
	}
	if es.healUndo == nil {
		return
	}
	es.canvasImg.Image = es.healUndo
	es.working = es.healUndo
	es.commitWorkingToBase()
	es.resetAdjustmentControls()
	es.healUndo = nil
	es.jobGeneration++
	es.canvasImg.Refresh()
}

// onCropChange converts the overlay's screen-space selection to image pixels
// and updates the status readout.
func (es *editWorkspaceState) onCropChange(min, max fyne.Position, active bool) {
	if !active {
		es.cropHasSel = false
		if es.cropStatusLabel != nil {
			es.cropStatusLabel.SetText("Drag a rectangle over the image, then Apply Crop.")
		}
		return
	}
	minPt, okMin := es.screenToImagePt(min)
	maxPt, okMax := es.screenToImagePt(max)
	if !okMin || !okMax {
		return
	}
	es.cropMin = minPt
	es.cropMax = image.Pt(maxPt.X+1, maxPt.Y+1) // inclusive pixel -> exclusive bound
	es.cropHasSel = true
	if es.cropStatusLabel != nil {
		es.cropStatusLabel.SetText(fmt.Sprintf("Selection: %d x %d px", es.cropMax.X-es.cropMin.X, es.cropMax.Y-es.cropMin.Y))
	}
}

// applyCrop replaces the current image with the selected rectangle.
func (es *editWorkspaceState) applyCrop() {
	if es.diskSource != nil {
		if !es.cropHasSel || es.diskSource.store == nil {
			return
		}
		disk := es.diskSource
		rect := image.Rectangle{Min: es.cropMin, Max: es.cropMax}.Canon()
		es.jobGeneration++
		generation := es.jobGeneration
		prog := dialog.NewCustom("Cropping", "Cropping full-resolution disk source...", widget.NewProgressBarInfinite(), es.win)
		prog.Show()
		go func() {
			result, w, h, err := disk.store.Crop(context.Background(), rect, 1600)
			fyne.Do(func() {
				prog.Hide()
				if err != nil || es.diskSource != disk || es.jobGeneration != generation {
					return
				}
				planes, _, _, _ := disk.store.Source()
				disk.planes, disk.width, disk.height = planes, w, h
				disk.levels = models.RgbLevels{Max: [3]float64{255, 255, 255}}
				es.origW, es.origH = w, h
				es.resetInteractionStateAfterCrop()
				es.canvasImg.Image = result.Preview
				es.applyZoom()
				es.canvasImg.Refresh()
				es.refreshHistograms(result.Preview)
				es.cleanStatusLabel.SetText(fmt.Sprintf("Cropped full-resolution source to %d × %d.", w, h))
			})
		}()
		return
	}
	if !es.cropHasSel {
		dialog.ShowInformation("No selection", "Drag a rectangle on the image first.", es.win)
		return
	}
	rgba := es.working
	if rgba == nil {
		return
	}
	rect := image.Rectangle{Min: es.cropMin, Max: es.cropMax}.Canon().Intersect(rgba.Bounds())
	if rect.Dx() < 1 || rect.Dy() < 1 {
		dialog.ShowInformation("Selection too small", "The selected area is empty. Try again.", es.win)
		return
	}
	cropped := image.NewRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	draw.Draw(cropped, cropped.Bounds(), rgba, rect.Min, draw.Src)

	es.cropHasSel = false
	if es.cropOverlay != nil {
		es.cropOverlay.Reset()
	}
	// Crop works on the current working image, then bakes that operation into
	// the base before clearing the non-destructive adjustment controls.
	es.setImageOpts(cropped, false, false)
	es.commitWorkingToBase()
}

func (es *editWorkspaceState) refreshHistograms(img *image.RGBA) {
	for c := 0; c < 3; c++ {
		es.rgbBins[c] = [256]int{}
	}
	if img != nil {
		pix := img.Pix
		for i := 0; i+3 < len(pix); i += 4 {
			es.rgbBins[0][pix[i]]++
			es.rgbBins[1][pix[i+1]]++
			es.rgbBins[2][pix[i+2]]++
		}
	}
	for _, h := range es.rgbHists {
		if h != nil {
			h.Refresh()
		}
	}
}

func (es *editWorkspaceState) setImage(img image.Image) {
	es.setImageOpts(img, false, true)
}

func (d *editDiskSource) cleanup() {
	if d == nil {
		return
	}
	d.cleanupOnce.Do(func() {
		if d.store != nil {
			_ = d.store.Close()
		}
		if d.root != "" {
			_ = os.RemoveAll(d.root)
		}
	})
}

func (es *editWorkspaceState) installHandoff(h editImageHandoff) error {
	if !h.valid() {
		return fmt.Errorf("invalid Edit image handoff")
	}
	if h.disk != nil {
		return es.setDiskSource(h.disk)
	}
	if h.memory != nil {
		es.setImage(h.memory)
	}
	return nil
}

func (es *editWorkspaceState) setDiskSource(d *editDiskSource) error {
	if d == nil || d.width <= 0 || d.height <= 0 {
		if d != nil && d != es.diskSource {
			d.cleanup()
		}
		return fmt.Errorf("invalid disk-backed Edit dimensions")
	}
	// Prepare and validate the incoming source completely before touching the
	// currently installed source. This keeps replacement atomic on malformed or
	// truncated artifacts.
	cleanupIncoming := func() {
		if d != es.diskSource {
			d.cleanup()
		}
	}
	if d.store == nil {
		store, err := newEditDiskStore(d.root, d.planes, d.width, d.height, d.levels)
		if err != nil {
			cleanupIncoming()
			return fmt.Errorf("prepare disk Edit store: %w", err)
		}
		d.store = store
	}
	maxDim := 1600
	scale := math.Min(1, math.Min(float64(maxDim)/float64(d.width), float64(maxDim)/float64(d.height)))
	pw, ph := maxInt(1, int(math.Round(float64(d.width)*scale))), maxInt(1, int(math.Round(float64(d.height)*scale)))
	preview := image.NewRGBA(image.Rect(0, 0, pw, ph))
	arts := [3]*fitsio.Float32Artifact{}
	for i, p := range d.planes {
		a, err := fitsio.OpenFloat32ArtifactReadOnly(p)
		if err != nil {
			for _, opened := range arts {
				if opened != nil {
					_ = opened.Close()
				}
			}
			cleanupIncoming()
			return fmt.Errorf("open Edit plane %d: %w", i+1, err)
		}
		if a.Width != d.width || a.Height != d.height {
			_ = a.Close()
			for _, opened := range arts {
				if opened != nil {
					_ = opened.Close()
				}
			}
			cleanupIncoming()
			return fmt.Errorf("Edit plane %d dimensions do not match source", i+1)
		}
		arts[i] = a
	}
	rows := [3][]float32{}
	for i := range arts {
		rows[i] = make([]float32, d.width)
	}
	for y := 0; y < ph; y++ {
		sy := minEditInt(d.height-1, int(float64(y)/scale))
		for c := range arts {
			if err := arts[c].ReadRow(sy, rows[c]); err != nil {
				for _, a := range arts {
					if a != nil {
						_ = a.Close()
					}
				}
				cleanupIncoming()
				return fmt.Errorf("read Edit plane %d row %d: %w", c+1, sy, err)
			}
		}
		for x := 0; x < pw; x++ {
			sx := minEditInt(d.width-1, int(float64(x)/scale))
			preview.SetRGBA(x, y, color.RGBA{levelByte(rows[0][sx], d.levels.Min[0], d.levels.Max[0]), levelByte(rows[1][sx], d.levels.Min[1], d.levels.Max[1]), levelByte(rows[2][sx], d.levels.Min[2], d.levels.Max[2]), 255})
		}
	}
	for _, a := range arts {
		_ = a.Close()
	}
	old := es.diskSource
	es.diskSource = d
	es.base = nil
	es.working = nil
	es.jobGeneration++
	if old != nil && old != d {
		old.cleanup()
	}
	es.origW, es.origH = d.width, d.height
	// A disk handoff has no in-memory image setter to establish the initial
	// viewport. Mirror setImageOpts so coordinate mapping and the canvas size
	// start from the current fit zoom.
	es.zoom = 1.0
	if es.imgScroll != nil {
		sz := es.imgScroll.Size()
		if sz.Width > 1 && sz.Height > 1 && es.origW > 0 && es.origH > 0 {
			zw := float64(sz.Width) / float64(es.origW)
			zh := float64(sz.Height) / float64(es.origH)
			es.zoom = math.Min(zw, zh)
		}
	}
	es.setZoomSelectLabel("fit")
	es.applyZoom()
	// Compose RGB levels are the immutable baseline used by the disk renderer;
	// Edit's Levels controls begin at identity so they are not applied twice.
	es.rMinSlider.SetValue(0)
	es.rMaxSlider.SetValue(255)
	es.gMinSlider.SetValue(0)
	es.gMaxSlider.SetValue(255)
	es.bMinSlider.SetValue(0)
	es.bMaxSlider.SetValue(255)
	es.refreshHistograms(preview)
	es.canvasImg.Image = preview
	es.canvasImg.Refresh()
	if es.editTabs != nil {
		setDiskEditTabAvailability(es.editTabs)
	}
	for _, s := range []*widget.Slider{es.rMinSlider, es.rMaxSlider, es.gMinSlider, es.gMaxSlider, es.bMinSlider, es.bMaxSlider} {
		if s != nil {
			s.Enable()
		}
	}
	if es.applyButton != nil {
		es.applyButton.Enable()
	}
	if es.resetButton != nil {
		es.resetButton.Enable()
	}
	if es.cleanStatusLabel != nil {
		es.cleanStatusLabel.SetText("Disk-backed preview (1600×1600 maximum). Adjustments, Clean, Heal, and Crop apply at full resolution. Save streams from the full-resolution source.")
	}
	if es.editTabs != nil {
		es.editTabs.SelectIndex(0)
	}
	return nil
}

func (es *editWorkspaceState) resetInteractionStateAfterCrop() {
	es.cropHasSel = false
	if es.cropOverlay != nil {
		es.cropOverlay.Reset()
	}
	es.healUndo = nil
	if es.healOverlay != nil {
		es.healOverlay.Reset()
	}
	if es.cropStatusLabel != nil {
		if es.cropActive {
			es.cropStatusLabel.SetText("Drag a rectangle over the image, then Apply Crop.")
		} else {
			es.cropStatusLabel.SetText("")
		}
	}
	if es.healStatusLabel != nil {
		if es.healActive {
			es.healStatusLabel.SetText("Step 1: click source (sample area)")
		} else {
			es.healStatusLabel.SetText("")
		}
	}
}

func setDiskEditTabAvailability(tabs *container.AppTabs) {
	if tabs == nil {
		return
	}
	for i := range tabs.Items {
		tabs.EnableIndex(i)
	}
}

func toByte(v float32) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 255
	}
	return uint8(v*255 + 0.5)
}

func minEditInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func levelByte(v float32, min, max float64) uint8 {
	if max <= min {
		return 0
	}
	x := (float64(v)*255 - min) * 255 / (max - min)
	if x <= 0 {
		return 0
	}
	if x >= 255 {
		return 255
	}
	return uint8(x + 0.5)
}

// setImageOpts installs img as the working image. When replaceBase is true,
// such as for a new load or Compose handoff, it also becomes the reset target.
func (es *editWorkspaceState) setImageOpts(img image.Image, keepZoomAndScroll, replaceBase bool) {
	if img == nil {
		return
	}
	rgba, ok := img.(*image.RGBA)
	if !ok {
		bounds := img.Bounds()
		rgba = image.NewRGBA(bounds)
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				rgba.Set(x, y, img.At(x, y))
			}
		}
	}
	if es.diskSource != nil {
		es.diskSource.cleanup()
		es.diskSource = nil
	}
	if replaceBase {
		es.base = cloneEditRGBA(rgba)
	}
	es.working = rgba
	es.jobGeneration++
	if es.editTabs != nil {
		for i := range es.editTabs.Items {
			es.editTabs.EnableIndex(i)
		}
	}
	for _, s := range []*widget.Slider{es.rMinSlider, es.rMaxSlider, es.gMinSlider, es.gMaxSlider, es.bMinSlider, es.bMaxSlider} {
		if s != nil {
			s.Enable()
		}
	}
	if es.applyButton != nil {
		es.applyButton.Enable()
	}
	if es.resetButton != nil {
		es.resetButton.Enable()
	}
	es.origW = rgba.Bounds().Dx()
	es.origH = rgba.Bounds().Dy()

	es.rMinSlider.SetValue(0)
	es.rMaxSlider.SetValue(255)
	es.gMinSlider.SetValue(0)
	es.gMaxSlider.SetValue(255)
	es.bMinSlider.SetValue(0)
	es.bMaxSlider.SetValue(255)
	es.curves.Reset()
	es.curvesChannel.SetSelected("All")
	es.sharpSlider.SetValue(0)
	es.sharpRadiusSlider.SetValue(1.0)

	es.refreshHistograms(rgba)
	es.canvasImg.Image = rgba

	if !keepZoomAndScroll {
		es.zoom = 1.0
		if es.imgScroll != nil {
			sz := es.imgScroll.Size()
			if sz.Width > 1 && sz.Height > 1 && es.origW > 0 && es.origH > 0 {
				zw := float64(sz.Width) / float64(es.origW)
				zh := float64(sz.Height) / float64(es.origH)
				es.zoom = math.Min(zw, zh)
			}
		}
		es.setZoomSelectLabel("fit")
		es.applyZoom()
	}

	es.canvasImg.Refresh()
	if es.cropOverlay != nil {
		es.cropOverlay.Reset()
	}
	es.cropHasSel = false
	if es.cleanStatusLabel != nil {
		es.cleanStatusLabel.SetText("Run this after cross-channel clean to remove tiny color specks and black dropout dots.")
	}
	if es.editTabs != nil && es.cleanTab != nil {
		es.editTabs.Select(es.cleanTab)
	}
}

func (es *editWorkspaceState) runColorSpeckClean() {
	if es.diskSource != nil {
		disk := es.diskSource
		if disk.store == nil {
			return
		}
		es.jobGeneration++
		generation := es.jobGeneration
		cfg := processing.ColorSpeckCleanConfigFromSettings(int(math.Round(es.cleanBlobSlider.Value)), es.cleanIntensitySlider.Value)
		prog := dialog.NewCustom("Cleaning", "Cleaning full-resolution disk source...", widget.NewProgressBarInfinite(), es.win)
		prog.Show()
		go func() {
			result, err := disk.store.Clean(context.Background(), cfg, 1600)
			fyne.Do(func() {
				prog.Hide()
				if err != nil || es.diskSource != disk || es.jobGeneration != generation {
					return
				}
				planes, _, _, _ := disk.store.Source()
				disk.planes = planes
				disk.levels = models.RgbLevels{Max: [3]float64{255, 255, 255}}
				es.canvasImg.Image = result.Preview
				es.canvasImg.Refresh()
				es.refreshHistograms(result.Preview)
				if result.Repaired == 0 {
					es.cleanStatusLabel.SetText("No tiny pure-color specks were detected.")
				} else {
					es.cleanStatusLabel.SetText(fmt.Sprintf("Removed %d speck pixels; the result is now the disk Edit base.", result.Repaired))
				}
			})
		}()
		return
	}
	rgba := es.working
	if rgba == nil {
		dialog.ShowInformation("Nothing to clean", "Load or compose an image first.", es.win)
		return
	}
	es.jobGeneration++
	generation := es.jobGeneration
	source := rgba
	blobSize := es.cleanBlobSlider.Value
	intensity := es.cleanIntensitySlider.Value

	prog := dialog.NewCustom("Cleaning", "Removing tiny RGB specks...", widget.NewProgressBarInfinite(), es.win)
	prog.Show()

	go func() {
		cfg := processing.ColorSpeckCleanConfigFromSettings(int(math.Round(blobSize)), intensity)
		cleaned, repaired := processing.CleanColorSpecksRGBA(source, cfg)
		fyne.Do(func() {
			prog.Hide()
			if cleaned == nil || es.jobGeneration != generation || es.working != source || es.diskSource != nil {
				return
			}

			// Capture current zoom and scroll position before resetting the image.
			savedZoom := es.zoom
			var savedZoomSel string
			if es.zoomSelect != nil {
				savedZoomSel = es.zoomSelect.Selected
			}
			var savedOffset fyne.Position
			if es.imgScroll != nil {
				savedOffset = es.imgScroll.Offset
			}

			es.setImageOpts(cleaned, true, false)
			es.commitWorkingToBase()

			// Restore the captured zoom and scroll position. setImageOpts(...,true)
			// leaves them untouched, but the image swap + SetMinSize queues a layout
			// pass that runs after this callback and can re-fit the view, so re-assert
			// the saved values now and again on the next event-loop tick.
			restoreView := func() {
				// Set the label without firing OnChanged: setZoomFromSelect would
				// recompute zoom from the label and snap "fit" back in.
				if es.zoomSelect != nil && savedZoomSel != "" {
					prev := es.zoomSelect.OnChanged
					es.zoomSelect.OnChanged = nil
					es.setZoomSelectLabel(savedZoomSel)
					es.zoomSelect.OnChanged = prev
				}
				es.zoom = savedZoom
				es.applyZoom()
				if es.imgScroll != nil {
					es.imgScroll.Offset = savedOffset
					es.imgScroll.Refresh()
				}
			}
			restoreView()
			// Defeat any deferred re-fit triggered by the relayout above.
			go fyne.Do(restoreView)

			if repaired == 0 {
				es.cleanStatusLabel.SetText("No tiny pure-color specks were detected.")
				return
			}
			es.cleanStatusLabel.SetText(fmt.Sprintf("Removed %d speck pixels; the result is now the edit base.", repaired))
		})
	}()
}
func (es *editWorkspaceState) applyZoom() {
	if es.origW == 0 || es.zoom <= 0 {
		return
	}
	w := float32(es.origW) * float32(es.zoom)
	h := float32(es.origH) * float32(es.zoom)
	es.canvasImg.SetMinSize(fyne.NewSize(w, h))
	es.canvasImg.Refresh()
	es.updateHealBrushScreenRadius()
	// A zoom change invalidates the screen-space crop rectangle.
	if es.cropOverlay != nil && es.cropHasSel {
		es.cropOverlay.Reset()
		es.cropHasSel = false
		if es.cropStatusLabel != nil {
			es.cropStatusLabel.SetText("Drag a rectangle over the image, then Apply Crop.")
		}
	}
}

func (es *editWorkspaceState) stepZoom(factor float64) {
	if es.zoomSelect != nil && es.zoomSelect.Selected == "fit" {
		es.zoom = es.fitZoom()
	}
	es.zoom *= factor
	if es.zoom < 0.01 {
		es.zoom = 0.01
	}
	if es.zoom > 32 {
		es.zoom = 32
	}
	label := fmt.Sprintf("%d%%", int(math.Round(es.zoom*100)))
	es.setZoomSelectLabel(label)
	es.applyZoom()
}

func (es *editWorkspaceState) fitZoom() float64 {
	if es.origW == 0 || es.origH == 0 || es.imgScroll == nil {
		return 1
	}
	sz := es.imgScroll.Size()
	if sz.Width <= 1 || sz.Height <= 1 {
		return 1
	}
	return math.Min(float64(sz.Width)/float64(es.origW), float64(sz.Height)/float64(es.origH))
}

func (es *editWorkspaceState) setZoomFromSelect(sel string) {
	switch sel {
	case "fit":
		es.zoom = es.fitZoom()
	default:
		trimmed := strings.TrimSuffix(sel, "%")
		if val, err := strconv.ParseFloat(trimmed, 64); err == nil {
			es.zoom = val / 100.0
		}
	}
	es.applyZoom()
}

func (es *editWorkspaceState) setZoomSelectLabel(label string) {
	if es.zoomSelect == nil {
		return
	}
	isPreset := false
	for _, p := range editZoomPresets {
		if p == label {
			isPreset = true
			break
		}
	}
	if !isPreset {
		opts := make([]string, 0, len(editZoomPresets)+1)
		for _, o := range es.zoomSelect.Options {
			if o != es.customZoom {
				opts = append(opts, o)
			}
		}
		es.customZoom = label
		opts = append(opts, label)
		es.zoomSelect.Options = opts
	}
	es.zoomSelect.SetSelected(label)
}

// sliderRow creates a labelled slider with a live value readout.
// Optional extra callbacks are called in addition to the value label update.
func sliderRow(label string, s *widget.Slider, extra ...func(float64)) fyne.CanvasObject {
	valLabel := widget.NewLabel(fmt.Sprintf("%.0f", s.Value))
	s.OnChanged = func(v float64) {
		valLabel.SetText(fmt.Sprintf("%.2f", v))
		for _, fn := range extra {
			if fn != nil {
				fn(v)
			}
		}
	}
	return container.NewBorder(nil, nil,
		container.New(layout.NewGridWrapLayout(fyne.NewSize(90, 28)), widget.NewLabel(label)),
		container.New(layout.NewGridWrapLayout(fyne.NewSize(48, 28)), valLabel),
		s,
	)
}

// editHistRaster builds a canvas.Raster that draws the histogram for channel ch
// plus vertical marker lines at the current min and max slider positions.
func editHistRaster(es *editWorkspaceState, ch int, col [3]uint8, getMin, getMax func() float64) *canvas.Raster {
	r := canvas.NewRaster(func(w, h int) image.Image {
		img := image.NewRGBA(image.Rect(0, 0, w, h))

		// Background
		for i := 0; i < len(img.Pix); i += 4 {
			img.Pix[i] = 28
			img.Pix[i+1] = 28
			img.Pix[i+2] = 28
			img.Pix[i+3] = 255
		}

		// Histogram bars
		maxCount := 0
		for _, c := range es.rgbBins[ch] {
			if c > maxCount {
				maxCount = c
			}
		}
		if maxCount > 0 {
			for i, c := range es.rgbBins[ch] {
				x := i * w / 256
				barH := int(float64(c) / float64(maxCount) * float64(h))
				for y := h - 1; y >= h-barH; y-- {
					idx := y*img.Stride + x*4
					img.Pix[idx] = col[0]
					img.Pix[idx+1] = col[1]
					img.Pix[idx+2] = col[2]
					img.Pix[idx+3] = 255
				}
			}
		}

		// Min marker (white vertical line)
		minX := int(getMin() / 255.0 * float64(w-1))
		for y := 0; y < h; y++ {
			idx := y*img.Stride + minX*4
			img.Pix[idx] = 255
			img.Pix[idx+1] = 255
			img.Pix[idx+2] = 255
			img.Pix[idx+3] = 255
		}

		// Max marker (white vertical line)
		maxX := int(getMax() / 255.0 * float64(w-1))
		for y := 0; y < h; y++ {
			idx := y*img.Stride + maxX*4
			img.Pix[idx] = 255
			img.Pix[idx+1] = 255
			img.Pix[idx+2] = 255
			img.Pix[idx+3] = 255
		}

		return img
	})
	r.SetMinSize(fyne.NewSize(200, 60))
	return r
}

func newEditWorkspace(app fyne.App, win fyne.Window) (fyne.CanvasObject, func(editImageHandoff) error) {
	es := &editWorkspaceState{zoom: 1.0, win: win}

	// Canvas image
	es.canvasImg = canvas.NewImageFromImage(blankImg())
	es.canvasImg.FillMode = canvas.ImageFillContain

	es.healOverlay = newHealLayer()
	es.healOverlay.Hide()
	es.cropOverlay = newCropLayer()
	es.cropOverlay.Hide()
	es.cropOverlay.onChange = es.onCropChange
	es.imgScroll = container.NewScroll(container.NewMax(es.canvasImg, es.healOverlay, es.cropOverlay))
	es.imgScroll.SetMinSize(fyne.NewSize(400, 300))

	// Zoom controls
	es.zoomSelect = NewSafeSelect(append([]string{}, editZoomPresets...), func(sel string) {
		es.setZoomFromSelect(sel)
	})
	es.zoomSelect.SetSelected("fit")

	zoomOutBtn := widget.NewButton("-", func() { es.stepZoom(1.0 / 1.15) })
	zoomInBtn := widget.NewButton("+", func() { es.stepZoom(1.15) })
	zoomBar := container.NewCenter(container.NewHBox(zoomOutBtn, es.zoomSelect, zoomInBtn))
	rightPanel := container.NewBorder(zoomBar, nil, nil, nil, es.imgScroll)

	// --- Level sliders (0–255) ---
	es.rMinSlider = widget.NewSlider(0, 255)
	es.rMaxSlider = widget.NewSlider(0, 255)
	es.gMinSlider = widget.NewSlider(0, 255)
	es.gMaxSlider = widget.NewSlider(0, 255)
	es.bMinSlider = widget.NewSlider(0, 255)
	es.bMaxSlider = widget.NewSlider(0, 255)
	es.rMaxSlider.SetValue(255)
	es.gMaxSlider.SetValue(255)
	es.bMaxSlider.SetValue(255)

	// Histograms – created after sliders so they can close over them
	histColors := [3][3]uint8{{200, 60, 60}, {60, 160, 60}, {60, 100, 200}}
	es.rgbHists[0] = editHistRaster(es, 0, histColors[0],
		func() float64 { return es.rMinSlider.Value },
		func() float64 { return es.rMaxSlider.Value },
	)
	es.rgbHists[1] = editHistRaster(es, 1, histColors[1],
		func() float64 { return es.gMinSlider.Value },
		func() float64 { return es.gMaxSlider.Value },
	)
	es.rgbHists[2] = editHistRaster(es, 2, histColors[2],
		func() float64 { return es.bMinSlider.Value },
		func() float64 { return es.bMaxSlider.Value },
	)

	// --- Curves widget ---
	es.curves = newCurvesWidget(nil) // onChange wired after applyBtn exists
	es.curvesChannel = NewSafeSelect(
		[]string{"All", "Red", "Green", "Blue"},
		func(sel string) {
			switch sel {
			case "Red":
				es.curves.SetActive(1)
			case "Green":
				es.curves.SetActive(2)
			case "Blue":
				es.curves.SetActive(3)
			default:
				es.curves.SetActive(0)
			}
		},
	)
	es.curvesChannel.SetSelected("All")

	// --- Sharpen sliders ---
	es.sharpSlider = widget.NewSlider(0, 3.0)
	es.sharpSlider.Step = 0.05
	es.sharpRadiusSlider = widget.NewSlider(0.5, 10.0)
	es.sharpRadiusSlider.SetValue(1.0)
	es.sharpRadiusSlider.Step = 0.5

	// --- Load button ---
	loadBtn := widget.NewButton("Load Image...", func() {
		fd := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
			if err != nil || r == nil {
				return
			}
			defer r.Close()
			img, _, err := image.Decode(r)
			if err != nil {
				dialog.ShowError(err, win)
				return
			}
			app.Preferences().SetString("editLastDir", r.URI().Path())
			es.loadedName = r.URI().Name()
			es.setImage(img)
		}, win)
		fd.SetFilter(storage.NewExtensionFileFilter([]string{".png", ".jpg", ".jpeg", ".tif", ".tiff"}))
		if last := app.Preferences().String("editLastDir"); last != "" {
			if lister, err := storage.ListerForURI(storage.NewFileURI(last)); err == nil {
				fd.SetLocation(lister)
			}
		}
		fd.SetView(dialog.ListView)
		sizeFileDialog(fd)
		fd.Show()
	})

	applyBtn := widget.NewButton("Apply", func() { es.applyEdits() })
	es.applyButton = applyBtn
	es.curves.onDragEnd = func() { es.applyEdits() }
	resetBtn := widget.NewButton("Reset", func() {
		if es.diskSource != nil && es.diskSource.store != nil {
			es.diskSource.store.Reset()
			es.resetAdjustmentControls()
			planes, _, _, _ := es.diskSource.store.Source()
			es.diskSource.planes = planes
			es.diskSource.levels = models.RgbLevels{Max: [3]float64{255, 255, 255}}
			// Re-render the immutable baseline to refresh the bounded preview.
			es.applyEdits()
			return
		}
		if es.base == nil {
			return
		}
		es.rMinSlider.SetValue(0)
		es.rMaxSlider.SetValue(255)
		es.gMinSlider.SetValue(0)
		es.gMaxSlider.SetValue(255)
		es.bMinSlider.SetValue(0)
		es.bMaxSlider.SetValue(255)
		es.curves.Reset()
		es.curvesChannel.SetSelected("All")
		es.sharpSlider.SetValue(0)
		es.sharpRadiusSlider.SetValue(1.0)
		es.resetWorkingToBase()
		es.refreshHistograms(es.working)
		es.canvasImg.Image = es.working
		es.canvasImg.Refresh()
	})
	es.resetButton = resetBtn

	saveBtn := widget.NewButton("Save Image...", func() {
		if es.working == nil && es.diskSource == nil {
			dialog.ShowInformation("Nothing to save", "Load or compose an image first.", win)
			return
		}
		save := dialog.NewFileSave(func(uc fyne.URIWriteCloser, err error) {
			if err != nil || uc == nil {
				return
			}
			path := uc.URI().Path()
			_ = uc.Close()

			rgba := es.working
			format := export.PNG
			lower := strings.ToLower(path)
			switch {
			case strings.HasSuffix(lower, ".webp"):
				format = export.WEBP
			case strings.HasSuffix(lower, ".tif"), strings.HasSuffix(lower, ".tiff"):
				format = export.TIFF
			case strings.HasSuffix(lower, ".jpg"), strings.HasSuffix(lower, ".jpeg"):
				format = export.JPEG
			}
			showExportOptionsDialog(format, win, func(opts export.Options) {
				var err error
				if es.diskSource != nil {
					disk := es.diskSource
					recipe := es.diskEditRecipe()
					ctx, cancel := context.WithCancel(context.Background())
					status := widget.NewLabel("Rendering full-resolution Edit source…")
					progress := dialog.NewCustom("Saving", "", container.NewBorder(nil, widget.NewButton("Cancel", func() { cancel() }), nil, nil, container.NewVBox(status, widget.NewProgressBarInfinite())), win)
					progress.Show()
					go func() {
						snapshot, snapshotErr := disk.store.ExportSnapshot(ctx, recipe)
						if snapshotErr == nil {
							snapshotErr = export.FromFloat32Artifacts(ctx, path, snapshot.planes, snapshot.width, snapshot.height, format, opts)
							snapshot.cleanup()
						}
						fyne.Do(func() {
							cancel()
							progress.Hide()
							if snapshotErr != nil && !errors.Is(snapshotErr, context.Canceled) {
								dialog.ShowError(snapshotErr, win)
							}
						})
					}()
					return
				} else {
					err = export.FromImage(path, rgba, format, opts)
				}
				if err != nil {
					dialog.ShowError(err, win)
					return
				}
				if es.diskSource == nil && rgba != nil && es.working == rgba {
					es.commitWorkingToBase()
					es.resetAdjustmentControls()
				}
			})
		}, win)
		saveName := "edited.png"
		if es.loadedName != "" {
			saveName = es.loadedName
		}
		save.SetFileName(saveName)
		save.SetFilter(storage.NewExtensionFileFilter([]string{".png", ".webp", ".jpg", ".jpeg", ".tif", ".tiff"}))
		save.Show()
	})

	// --- Heal tool ---
	es.healBrushSlider = widget.NewSlider(1, 200)
	es.healBrushSlider.SetValue(20)
	es.healBrushSlider.Step = 1

	es.healBrushSlider.OnChanged = func(_ float64) {
		es.updateHealBrushScreenRadius()
	}

	es.healStatusLabel = widget.NewLabel("Click to set source point")
	es.healStatusLabel.TextStyle = fyne.TextStyle{Italic: true}

	healToggleBtn := widget.NewButton("Heal Tool: OFF", nil)
	cropToggleBtn := widget.NewButton("Crop Tool: OFF", nil)

	// Heal and crop are mutually exclusive interactions: enabling one disables
	// the other so their overlays never both capture pointer events.
	deactivateHeal := func() {
		es.healActive = false
		healToggleBtn.SetText("Heal Tool: OFF")
		es.healOverlay.Hide()
		es.healOverlay.Reset()
		es.healStatusLabel.SetText("")
	}
	deactivateCrop := func() {
		es.cropActive = false
		cropToggleBtn.SetText("Crop Tool: OFF")
		es.cropOverlay.Hide()
		es.cropOverlay.Reset()
		es.cropHasSel = false
		es.cropStatusLabel.SetText("")
	}

	healToggleBtn.OnTapped = func() {
		es.healActive = !es.healActive
		if es.healActive {
			deactivateCrop()
			healToggleBtn.SetText("Heal Tool: ON")
			es.healOverlay.Show()
			es.healOverlay.Reset()
			es.updateHealBrushScreenRadius()
			es.healStatusLabel.SetText("Step 1: click source (sample area)")
		} else {
			deactivateHeal()
		}
	}
	cropToggleBtn.OnTapped = func() {
		es.cropActive = !es.cropActive
		if es.cropActive {
			deactivateHeal()
			cropToggleBtn.SetText("Crop Tool: ON")
			es.cropOverlay.Show()
			es.cropOverlay.Reset()
			es.cropHasSel = false
			es.cropStatusLabel.SetText("Drag a rectangle over the image, then Apply Crop.")
		} else {
			deactivateCrop()
		}
	}

	healUndoBtn := widget.NewButton("Undo Heal (Ctrl+Z)", func() {
		es.undoHeal()
	})

	es.cleanStatusLabel = widget.NewLabel("Run this after cross-channel clean to remove tiny color specks and black dropout dots.")
	es.cleanBlobSlider = widget.NewSlider(1, 100)
	es.cleanBlobSlider.Step = 1
	es.cleanBlobSlider.SetValue(25)
	es.cleanIntensitySlider = widget.NewSlider(0, 100)
	es.cleanIntensitySlider.Step = 1
	es.cleanIntensitySlider.SetValue(50)
	cleanBtn := widget.NewButton("Remove Color & Dark Specks", func() {
		es.runColorSpeckClean()
	})

	es.cropStatusLabel = widget.NewLabel("")
	es.cropStatusLabel.TextStyle = fyne.TextStyle{Italic: true}
	applyCropBtn := widget.NewButton("Apply Crop", func() {
		es.applyCrop()
	})

	es.healOverlay.onHealStroke = func(src fyne.Position, dsts []fyne.Position) {
		es.doHealStroke(src, dsts)
		if es.healActive {
			es.healStatusLabel.SetText("Step 1: click source (sample area)")
		}
	}
	es.healOverlay.onSourceSet = func() {
		if es.healActive {
			es.healStatusLabel.SetText("Step 2: click/drag destination to heal")
		}
	}

	// Ctrl+Z undo shortcut on the window canvas.
	win.Canvas().AddShortcut(
		&desktop.CustomShortcut{KeyName: fyne.KeyZ, Modifier: fyne.KeyModifierControl},
		func(_ fyne.Shortcut) { es.undoHeal() },
	)

	// Refresh histograms whenever a level slider changes
	refreshHists := func(_ float64) {
		for _, h := range es.rgbHists {
			h.Refresh()
		}
	}

	levelsTab := container.NewTabItem("Levels", container.NewVBox(
		widget.NewLabel("Red"),
		es.rgbHists[0],
		sliderRow("Min", es.rMinSlider, refreshHists),
		sliderRow("Max", es.rMaxSlider, refreshHists),
		widget.NewLabel("Green"),
		es.rgbHists[1],
		sliderRow("Min", es.gMinSlider, refreshHists),
		sliderRow("Max", es.gMaxSlider, refreshHists),
		widget.NewLabel("Blue"),
		es.rgbHists[2],
		sliderRow("Min", es.bMinSlider, refreshHists),
		sliderRow("Max", es.bMaxSlider, refreshHists),
	))
	curvesTab := container.NewTabItem("Curves", container.NewVBox(
		es.curvesChannel,
		es.curves,
	))
	sharpenTab := container.NewTabItem("Sharpen", container.NewVBox(
		sliderRow("Strength", es.sharpSlider),
		sliderRow("Radius", es.sharpRadiusSlider),
	))
	cleanTab := container.NewTabItem("Clean", container.NewVBox(
		widget.NewLabel("Targets small red, green, or blue cosmic-ray leftovers and near-black dropout dots in the composed RGB image."),
		sliderRow("Max Blob Size", es.cleanBlobSlider),
		sliderRow("Intensity", es.cleanIntensitySlider),
		cleanBtn,
		es.cleanStatusLabel,
	))
	healTab := container.NewTabItem("Heal", container.NewVBox(
		healToggleBtn,
		es.healStatusLabel,
		sliderRow("Brush Size", es.healBrushSlider),
		healUndoBtn,
	))
	cropTab := container.NewTabItem("Crop", container.NewVBox(
		widget.NewLabel("Turn the tool on, drag a rectangle over the image, then apply. The cropped result becomes the new edit base."),
		cropToggleBtn,
		es.cropStatusLabel,
		applyCropBtn,
	))
	tabs := container.NewAppTabs(levelsTab, curvesTab, sharpenTab, cleanTab, healTab, cropTab)
	es.editTabs = tabs
	es.cleanTab = cleanTab

	controls := container.NewVBox(
		loadBtn,
		widget.NewSeparator(),
		tabs,
		widget.NewSeparator(),
		container.NewHBox(applyBtn, resetBtn),
		saveBtn,
	)

	paddedControls := container.New(layout.NewCustomPaddedLayout(0, 0, 20, 20), controls)
	controlsScroll := container.NewVScroll(paddedControls)
	controlsScroll.SetMinSize(fyne.NewSize(280, 200))

	split := container.NewHSplit(controlsScroll, rightPanel)
	split.SetOffset(0.28)

	setter := func(h editImageHandoff) error {
		return es.installHandoff(h)
	}
	globalEditCleanup = func() {
		if es.diskSource != nil {
			es.diskSource.cleanup()
			es.diskSource = nil
		}
	}
	return split, setter
}
