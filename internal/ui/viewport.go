package ui

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// hpad returns a fixed-width invisible spacer for horizontal edge padding.
func hpad(w float32) fyne.CanvasObject {
	r := canvas.NewRectangle(color.Transparent)
	r.SetMinSize(fyne.NewSize(w, 0))
	return r
}

// vpad returns a fixed-height invisible spacer for vertical padding.
func vpad(h float32) fyne.CanvasObject {
	r := canvas.NewRectangle(color.Transparent)
	r.SetMinSize(fyne.NewSize(0, h))
	return r
}

// ─── chanBadge ───────────────────────────────────────────────────────────────

type chanBadge struct {
	widget.BaseWidget
	letter string
	col    color.Color
}

func newChanBadge(letter string, col color.Color) *chanBadge {
	b := &chanBadge{letter: letter, col: col}
	b.ExtendBaseWidget(b)
	return b
}

func (b *chanBadge) MinSize() fyne.Size {
	s := theme.TextSize() + 2
	return fyne.NewSize(s, s)
}

func (b *chanBadge) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(b.col)
	bg.CornerRadius = 4
	txt := canvas.NewText(b.letter, color.White)
	txt.TextSize = theme.TextSize() - 1
	txt.TextStyle = fyne.TextStyle{Bold: true}
	return &chanBadgeRenderer{b: b, bg: bg, txt: txt}
}

type chanBadgeRenderer struct {
	b   *chanBadge
	bg  *canvas.Rectangle
	txt *canvas.Text
}

func (r *chanBadgeRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.bg, r.txt}
}
func (r *chanBadgeRenderer) MinSize() fyne.Size {
	s := theme.TextSize() + 2
	return fyne.NewSize(s, s)
}
func (r *chanBadgeRenderer) Layout(size fyne.Size) {
	s := theme.TextSize() + 2
	x := (size.Width - s) / 2
	y := (size.Height - s) / 2
	r.bg.Move(fyne.NewPos(x, y))
	r.bg.Resize(fyne.NewSize(s, s))
	ts := r.txt.MinSize()
	r.txt.Move(fyne.NewPos(x+(s-ts.Width)/2, y+(s-ts.Height)/2))
	r.txt.Resize(ts)
}
func (r *chanBadgeRenderer) Refresh() {
	r.bg.FillColor = r.b.col
	r.txt.Color = color.White
	r.bg.Refresh()
	r.txt.Refresh()
}
func (r *chanBadgeRenderer) Destroy() {}

// ─── compactBtn ──────────────────────────────────────────────────────────────

const (
	compactVPad float32 = 5
	compactHPad float32 = 10
)

type compactBtn struct {
	widget.BaseWidget
	text  string
	onTap func()
}

func newCompactBtn(text string, onTap func()) *compactBtn {
	b := &compactBtn{text: text, onTap: onTap}
	b.ExtendBaseWidget(b)
	return b
}

func (b *compactBtn) Tapped(_ *fyne.PointEvent) {
	if b.onTap != nil {
		b.onTap()
	}
}
func (b *compactBtn) TappedSecondary(_ *fyne.PointEvent) {}

func (b *compactBtn) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(theme.ButtonColor())
	bg.CornerRadius = 4
	txt := canvas.NewText(b.text, theme.ForegroundColor())
	txt.TextSize = theme.TextSize()
	return &compactBtnRenderer{btn: b, bg: bg, txt: txt}
}

type compactBtnRenderer struct {
	btn *compactBtn
	bg  *canvas.Rectangle
	txt *canvas.Text
}

func (r *compactBtnRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.bg, r.txt}
}
func (r *compactBtnRenderer) MinSize() fyne.Size {
	ts := r.txt.MinSize()
	return fyne.NewSize(ts.Width+compactHPad*2, ts.Height+compactVPad*2)
}
func (r *compactBtnRenderer) Layout(size fyne.Size) {
	ms := r.MinSize()
	x := (size.Width - ms.Width) / 2
	if x < 0 {
		x = 0
	}
	y := (size.Height - ms.Height) / 2
	if y < 0 {
		y = 0
	}
	r.bg.Move(fyne.NewPos(x, y))
	r.bg.Resize(ms)
	ts := r.txt.MinSize()
	r.txt.Move(fyne.NewPos(x+(ms.Width-ts.Width)/2, y+(ms.Height-ts.Height)/2))
	r.txt.Resize(ts)
}
func (r *compactBtnRenderer) Refresh() {
	r.bg.FillColor = theme.ButtonColor()
	r.txt.Color = theme.ForegroundColor()
	r.bg.Refresh()
	r.txt.Refresh()
}
func (r *compactBtnRenderer) Destroy() {}

type imagePoint struct {
	X int
	Y int
}

type viewport struct {
	image         *canvas.Image
	histogram     *canvas.Raster
	zoomLabel     *SafeSelect
	zoomOut       *widget.Button
	zoomIn        *widget.Button
	blackBox      *NumberEntry
	whiteBox      *NumberEntry
	container     fyne.CanvasObject
	zoom          float64
	origW         int
	origH         int
	scroll        *container.Scroll
	overlay       *viewerInteractionLayer
	bins          [256]int
	customZoom    string
	histColor     [4]uint8 // bar color; if zero, use default white-bg/gray-bar style
	StatsLabel    *widget.Label
	pickerLabel   *widget.Label
	pickerBox     fyne.CanvasObject
	actionRow     *fyne.Container
	onViewChanged func()
	histMax       int
	onPickBlack   func()
	onPickWhite   func()
}

var presetZoomOptions = []string{"fit in preview", "1%", "5%", "10%", "20%", "25%", "50%", "75%", "100%", "200%", "300%"}

func newViewport() *viewport {
	img := canvas.NewImageFromImage(blankImg())
	img.FillMode = canvas.ImageFillContain

	vp := &viewport{image: img, zoom: 1}
	vp.histogram = canvas.NewRaster(vp.drawHist)
	vp.histogram.SetMinSize(fyne.NewSize(200, 48))

	overlay := newViewerInteractionLayer()
	vp.overlay = overlay
	vp.scroll = container.NewScroll(container.NewMax(img, overlay))
	overlay.scroll = vp.scroll
	vp.scroll.SetMinSize(fyne.NewSize(260, 180))

	vp.blackBox = NewNumberEntry(0.001, 4)
	vp.whiteBox = NewNumberEntry(0.001, 4)
	vp.zoomLabel = NewSafeSelect([]string{"fit in preview", "1%", "5%", "10%", "20%", "25%", "50%", "75%", "100%", "200%", "300%"}, func(s string) {
		vp.setZoomFromSelect(s)
	})
	vp.zoomOut = widget.NewButton("-", func() { vp.stepZoom(0.95) })
	vp.zoomIn = widget.NewButton("+", func() { vp.stepZoom(1.05) })

	vp.StatsLabel = widget.NewLabel("Sky --  μ --  σ --")
	vp.StatsLabel.TextStyle = fyne.TextStyle{Monospace: true}
	vp.StatsLabel.Alignment = fyne.TextAlignCenter
	vp.pickerLabel = widget.NewLabel("Value: --")
	vp.pickerLabel.TextStyle = fyne.TextStyle{Monospace: true}
	vp.pickerBox = container.New(layout.NewGridWrapLayout(fyne.NewSize(135, vp.pickerLabel.MinSize().Height)), vp.pickerLabel)

	vp.actionRow = container.NewHBox(layout.NewSpacer(), vp.StatsLabel, layout.NewSpacer())

	header := container.NewVBox(
		vpad(4),
		vp.actionRow,
		vpad(4),
		vp.histogram,
	)
	footerRow := container.NewHBox(
		hpad(6),
		layout.NewSpacer(),
		widget.NewLabel("Black"),
		vp.blackBox,
		vp.zoomOut,
		vp.zoomLabel,
		vp.zoomIn,
		widget.NewLabel("White"),
		vp.whiteBox,
		layout.NewSpacer(),
		hpad(6),
	)
	footer := container.NewVBox(footerRow, vpad(5))
	vp.container = container.NewBorder(header, footer, nil, nil, vp.scroll)

	vp.zoomLabel.SetSelected("fit in preview")

	return vp
}

func (vp *viewport) SetLevelPickers(blackFn, whiteFn func()) {
	vp.onPickBlack = blackFn
	vp.onPickWhite = whiteFn
}

func (vp *viewport) SetPickerValueText(text string) {
	if vp == nil || vp.pickerLabel == nil {
		return
	}
	if text == "" {
		text = "Value: --"
	}
	vp.pickerLabel.SetText(text)
	vp.pickerLabel.Refresh()
	if vp.pickerBox != nil {
		vp.pickerBox.Refresh()
		canvas.Refresh(vp.pickerBox)
	}
}

func (vp *viewport) SetLoadSave(chanLabel, letter string, col color.Color, loadFn, saveFn func()) {
	badge := newChanBadge(letter, col)
	nameText := canvas.NewText(chanLabel, col)
	nameText.TextSize = theme.TextSize()
	nameText.TextStyle = fyne.TextStyle{Bold: true}
	objects := []fyne.CanvasObject{
		hpad(6), badge, hpad(4), nameText,
		layout.NewSpacer(),
		vp.StatsLabel,
	}
	if vp.onPickBlack != nil && vp.onPickWhite != nil {
		objects = append(objects,
			hpad(6), vp.pickerBox,
			hpad(6), newCompactBtn("B", vp.onPickBlack),
			newCompactBtn("W", vp.onPickWhite),
		)
	}
	objects = append(objects, hpad(6), newCompactBtn("Load", loadFn), newCompactBtn("Save", saveFn), hpad(6))
	vp.actionRow.Objects = objects
	vp.actionRow.Refresh()
}

func (vp *viewport) SetCenterAction(chanLabel, letter string, col color.Color, actionLabel string, fn func()) {
	badge := newChanBadge(letter, col)
	nameText := canvas.NewText(chanLabel, col)
	nameText.TextSize = theme.TextSize()
	nameText.TextStyle = fyne.TextStyle{Bold: true}
	vp.actionRow.Objects = []fyne.CanvasObject{
		hpad(6), badge, hpad(4), nameText,
		layout.NewSpacer(),
		vp.StatsLabel, hpad(6), newCompactBtn(actionLabel, fn), hpad(6),
	}
	vp.actionRow.Refresh()
}

func isPresetZoom(option string) bool {
	for _, o := range presetZoomOptions {
		if o == option {
			return true
		}
	}
	return false
}

func (vp *viewport) setZoomLabelValue(option string) {
	if !isPresetZoom(option) {
		if vp.customZoom != "" {
			var opts []string
			for _, o := range vp.zoomLabel.Options {
				if o != vp.customZoom {
					opts = append(opts, o)
				}
			}
			vp.zoomLabel.Options = opts
		}
		vp.customZoom = option
		vp.zoomLabel.Options = append(vp.zoomLabel.Options, option)
	}
	vp.zoomLabel.SetSelected(option)
}

func (vp *viewport) setZoomFromSelect(sel string) {
	switch sel {
	case "fit in preview":
		vp.zoom = vp.fitZoom()
	default:
		sel = strings.TrimSuffix(sel, "%")
		if val, err := strconv.ParseFloat(sel, 64); err == nil {
			vp.zoom = val / 100.0
		}
	}
	vp.applyZoom()
}

func (vp *viewport) stepZoom(factor float64) {
	if vp.zoomLabel.Selected == "fit in preview" {
		vp.zoom = vp.fitZoom()
	}
	vp.zoom *= factor
	vp.setZoomLabelValue(fmt.Sprintf("%d%%", int(math.Round(vp.zoom*100))))
	vp.applyZoom()
}

func (vp *viewport) applyZoom() {
	if vp == nil || vp.scroll == nil || vp.image == nil {
		return
	}
	if vp.zoom <= 0 {
		vp.zoom = 1
	}
	avail := vp.scroll.Size()
	if avail.Width <= 1 || avail.Height <= 1 {
		avail = fyne.NewSize(300, 300)
	}
	if vp.origW == 0 || vp.origH == 0 {
		vp.image.SetMinSize(avail)
		vp.image.Refresh()
		if vp.overlay != nil {
			vp.overlay.Refresh()
		}
		if vp.onViewChanged != nil {
			vp.onViewChanged()
		}
		return
	}
	w := float32(vp.origW) * float32(vp.zoom)
	h := float32(vp.origH) * float32(vp.zoom)
	vp.image.SetMinSize(fyne.NewSize(w, h))
	vp.image.Refresh()
	if vp.overlay != nil {
		vp.overlay.Refresh()
	}
	if vp.onViewChanged != nil {
		vp.onViewChanged()
	}
}

func (vp *viewport) fitZoom() float64 {
	if vp.origW == 0 || vp.origH == 0 {
		return 1
	}
	sz := vp.scroll.Size()
	if sz.Width <= 1 || sz.Height <= 1 {
		sz = fyne.NewSize(300, 300)
	}
	return math.Min(float64(sz.Width)/float64(vp.origW), float64(sz.Height)/float64(vp.origH))
}

func (vp *viewport) drawHist(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))

	colored := vp.histColor[3] > 0
	if colored {
		// black background — img.Pix is already zero (transparent), set alpha
		for i := 3; i < len(img.Pix); i += 4 {
			img.Pix[i] = 255
		}
	} else {
		// white background
		for i := range img.Pix {
			img.Pix[i] = 255
		}
	}

	maxCount := 0
	if vp.histMax > 0 {
		maxCount = vp.histMax
	} else {
		for _, c := range vp.bins {
			if c > maxCount {
				maxCount = c
			}
		}
	}
	if maxCount == 0 {
		return img
	}

	var r, g, b uint8
	if colored {
		r, g, b = vp.histColor[0], vp.histColor[1], vp.histColor[2]
	} else {
		r, g, b = 80, 80, 80
	}

	for i, c := range vp.bins {
		x := i * w / len(vp.bins)
		barH := int(float64(c) / float64(maxCount) * float64(h))
		for y := h - 1; y >= h-barH; y-- {
			idx := y*img.Stride + x*4
			img.Pix[idx] = r
			img.Pix[idx+1] = g
			img.Pix[idx+2] = b
			img.Pix[idx+3] = 255
		}
	}
	return img
}

func blankImg() *image.RGBA {
	return image.NewRGBA(image.Rect(0, 0, 10, 10))
}

func (vp *viewport) imagePointAtPosition(pos fyne.Position, flipped bool) (imagePoint, bool) {
	return mapViewportPositionToImage(pos, fyne.NewPos(0, 0), vp.zoom, vp.origW, vp.origH, flipped)
}

func (vp *viewport) setMeasurementOverlay(first *imagePoint, second *imagePoint, flipped bool) {
	if vp == nil || vp.overlay == nil {
		return
	}
	var start *fyne.Position
	var end *fyne.Position
	if first != nil {
		pos := imagePointToCanvasPosition(*first, vp.zoom, vp.origH, flipped)
		start = &pos
	}
	if second != nil {
		pos := imagePointToCanvasPosition(*second, vp.zoom, vp.origH, flipped)
		end = &pos
	}
	vp.overlay.setMeasurement(start, end)
}
