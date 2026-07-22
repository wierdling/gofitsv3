package ui

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"path/filepath"
	"sort"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// legendEntry is one row of the Compose color legend: a swatch color plus the
// display name that goes on the PNG. filter (FITS header) and path are shown
// only in the naming dialog for reference, not drawn on the legend.
type legendEntry struct {
	name   string
	filter string
	path   string
	color  color.RGBA
}

// legendHue returns the hue in [0,360) of an RGB color, or -1 for grayscale.
// Sorting by this value orders swatches red -> orange -> yellow -> green -> blue.
func legendHue(c color.RGBA) float64 {
	r := float64(c.R) / 255
	g := float64(c.G) / 255
	b := float64(c.B) / 255
	max := math.Max(r, math.Max(g, b))
	min := math.Min(r, math.Min(g, b))
	d := max - min
	if d == 0 {
		return -1 // grayscale: no hue, sorts first
	}
	var h float64
	switch max {
	case r:
		h = math.Mod((g-b)/d, 6)
	case g:
		h = (b-r)/d + 2
	default:
		h = (r-g)/d + 4
	}
	h *= 60
	if h < 0 {
		h += 360
	}
	return h
}

// sortLegendEntriesByHue orders entries red -> orange -> yellow -> green -> blue.
func sortLegendEntriesByHue(entries []legendEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		return legendHue(entries[i].color) < legendHue(entries[j].color)
	})
}

// renderColorLegend rasterizes the legend to a small RGBA image, one row per
// entry: a color swatch followed by "<name> — <file>". Kept compact so the PNG
// is readable but not large.
func renderColorLegend(entries []legendEntry) *image.RGBA {
	const (
		padX    = 14
		padY    = 12
		swatchW = 46
		swatchH = 20
		gap     = 14
		rowH    = 30
	)
	face := basicfont.Face7x13
	ascent := face.Metrics().Ascent.Ceil()

	labels := make([]string, len(entries))
	maxTextW := 0
	for i, e := range entries {
		labels[i] = e.name
		if w := font.MeasureString(face, e.name).Ceil(); w > maxTextW {
			maxTextW = w
		}
	}

	rows := len(entries)
	if rows == 0 {
		rows = 1
	}
	imgW := padX + swatchW + gap + maxTextW + padX
	imgH := padY*2 + rowH*rows
	img := image.NewRGBA(image.Rect(0, 0, imgW, imgH))

	draw.Draw(img, img.Bounds(), &image.Uniform{color.RGBA{32, 32, 36, 255}}, image.Point{}, draw.Src)

	textCol := image.NewUniform(color.RGBA{235, 235, 235, 255})
	borderCol := color.RGBA{90, 90, 98, 255}
	for i, e := range entries {
		rowTop := padY + i*rowH
		sy0 := rowTop + (rowH-swatchH)/2
		rect := image.Rect(padX, sy0, padX+swatchW, sy0+swatchH)
		draw.Draw(img, rect, &image.Uniform{e.color}, image.Point{}, draw.Src)
		drawRectBorder(img, rect, borderCol)

		baseY := rowTop + (rowH+ascent)/2 - 1
		d := &font.Drawer{
			Dst:  img,
			Src:  textCol,
			Face: face,
			Dot:  fixed.P(padX+swatchW+gap, baseY),
		}
		d.DrawString(labels[i])
	}
	return img
}

// drawRectBorder outlines the given rectangle with a 1px border.
func drawRectBorder(img *image.RGBA, r image.Rectangle, c color.RGBA) {
	for x := r.Min.X; x < r.Max.X; x++ {
		img.SetRGBA(x, r.Min.Y, c)
		img.SetRGBA(x, r.Max.Y-1, c)
	}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		img.SetRGBA(r.Min.X, y, c)
		img.SetRGBA(r.Max.X-1, y, c)
	}
}

// showLegendNameDialog lets the user enter the display name for each legend
// row. Every row shows the swatch color and the source file/filter so the user
// knows which channel they are naming. On confirm the edited entries (with the
// typed names) are passed to onConfirm.
func showLegendNameDialog(parent fyne.Window, entries []legendEntry, onConfirm func([]legendEntry)) {
	if len(entries) == 0 {
		dialog.ShowInformation("Color Legend", "Load a channel or add a colored layer first.", parent)
		return
	}
	nameEntries := make([]*widget.Entry, len(entries))
	rows := make([]fyne.CanvasObject, 0, len(entries)*2)
	for i, e := range entries {
		swatch := canvas.NewRectangle(e.color)
		swatch.SetMinSize(fyne.NewSize(28, 28))
		swatch.StrokeColor = color.RGBA{90, 90, 98, 255}
		swatch.StrokeWidth = 1

		file := "(no file loaded)"
		if e.path != "" {
			file = filepath.Base(e.path)
		}
		filter := e.filter
		if filter == "" {
			filter = "—"
		}
		info := widget.NewLabel(fmt.Sprintf("%s\nfilter: %s", file, filter))

		entry := widget.NewEntry()
		entry.SetPlaceHolder("Name shown on legend")
		entry.SetText(e.name)
		nameEntries[i] = entry

		rows = append(rows,
			container.NewBorder(nil, nil, swatch, nil, container.NewVBox(info, entry)),
			widget.NewSeparator(),
		)
	}
	content := container.NewVScroll(container.NewVBox(rows...))
	content.SetMinSize(fyne.NewSize(440, float32(math.Min(float64(len(entries))*96+16, 520))))

	d := dialog.NewCustomConfirm("Color Legend Labels", "Generate", "Cancel", content, func(ok bool) {
		if !ok {
			return
		}
		out := make([]legendEntry, len(entries))
		copy(out, entries)
		for i := range out {
			name := strings.TrimSpace(nameEntries[i].Text)
			if name != "" {
				out[i].name = name
			}
		}
		onConfirm(out)
	}, parent)
	d.Resize(fyne.NewSize(480, 560))
	d.Show()
}

// showColorLegendWindow renders the entries and shows them in a small window
// with a Save PNG button.
func showColorLegendWindow(app fyne.App, parent fyne.Window, entries []legendEntry) {
	if len(entries) == 0 {
		dialog.ShowInformation("Color Legend", "Load a channel or add a colored layer first.", parent)
		return
	}
	img := renderColorLegend(entries)
	pic := canvas.NewImageFromImage(img)
	pic.FillMode = canvas.ImageFillOriginal
	pic.ScaleMode = canvas.ImageScalePixels

	saveBtn := widget.NewButton("Save PNG...", func() {
		save := dialog.NewFileSave(func(uc fyne.URIWriteCloser, err error) {
			if err != nil || uc == nil {
				return
			}
			defer uc.Close()
			if err := png.Encode(uc, img); err != nil {
				dialog.ShowError(err, parent)
			}
		}, parent)
		save.SetFileName("color_legend.png")
		save.Show()
	})

	w := app.NewWindow("Color Legend")
	w.SetContent(container.NewBorder(
		nil,
		container.NewHBox(layout.NewSpacer(), saveBtn),
		nil, nil,
		container.NewCenter(pic),
	))
	w.Resize(fyne.NewSize(float32(img.Bounds().Dx())+48, float32(img.Bounds().Dy())+96))
	w.Show()
}
