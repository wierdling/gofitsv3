package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

var colorTabInactive = color.NRGBA{R: 0xb0, G: 0xb0, B: 0xb0, A: 0xff}

const (
	tabDotRadius float32 = 5
	tabHPad      float32 = 10
	tabVPad      float32 = 6
	tabDotGap    float32 = 6
)

// ─── header button ───────────────────────────────────────────────────────────

type channelTabHeader struct {
	widget.BaseWidget
	label  string
	col    color.Color
	active bool
	onTap  func()
}

func newChannelTabHeader(label string, col color.Color, onTap func()) *channelTabHeader {
	h := &channelTabHeader{label: label, col: col, onTap: onTap}
	h.ExtendBaseWidget(h)
	return h
}

func (h *channelTabHeader) Tapped(_ *fyne.PointEvent)          { h.onTap() }
func (h *channelTabHeader) TappedSecondary(_ *fyne.PointEvent) {}

func (h *channelTabHeader) CreateRenderer() fyne.WidgetRenderer {
	dot := canvas.NewCircle(h.col)
	txt := canvas.NewText(h.label, colorTabInactive)
	txt.TextSize = 13
	return &tabHeaderRenderer{h: h, dot: dot, txt: txt}
}

type tabHeaderRenderer struct {
	h   *channelTabHeader
	dot *canvas.Circle
	txt *canvas.Text
}

func (r *tabHeaderRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.dot, r.txt}
}

func (r *tabHeaderRenderer) MinSize() fyne.Size {
	dotD := tabDotRadius * 2
	ts := r.txt.MinSize()
	w := tabHPad + dotD + tabDotGap + ts.Width + tabHPad
	h := ts.Height + tabVPad*2
	if dotD+tabVPad*2 > h {
		h = dotD + tabVPad*2
	}
	return fyne.NewSize(w, h)
}

func (r *tabHeaderRenderer) Layout(size fyne.Size) {
	dotD := tabDotRadius * 2
	r.dot.Move(fyne.NewPos(tabHPad, (size.Height-dotD)/2))
	r.dot.Resize(fyne.NewSize(dotD, dotD))

	ts := r.txt.MinSize()
	r.txt.Move(fyne.NewPos(tabHPad+dotD+tabDotGap, (size.Height-ts.Height)/2))
	r.txt.Resize(ts)
}

func (r *tabHeaderRenderer) Refresh() {
	r.dot.FillColor = r.h.col
	if r.h.active {
		r.txt.Color = r.h.col
	} else {
		r.txt.Color = colorTabInactive
	}
	r.dot.Refresh()
	r.txt.Refresh()
}

func (r *tabHeaderRenderer) Destroy() {}

// ─── ChannelTabs ─────────────────────────────────────────────────────────────

// ChannelTabItem is a single tab: a color, a label, and the content to show.
type ChannelTabItem struct {
	Label   string
	Color   color.Color
	Content fyne.CanvasObject
}

func NewChannelTabItem(label string, col color.Color, content fyne.CanvasObject) *ChannelTabItem {
	return &ChannelTabItem{Label: label, Color: col, Content: content}
}

// ChannelTabs is a custom tabbed panel with colored dot indicators,
// grey/colored label text, and a border around the active content area.
type ChannelTabs struct {
	widget.BaseWidget
	items   []*ChannelTabItem
	active  int
	headers []*channelTabHeader
	slot    *fyne.Container
	root    fyne.CanvasObject
}

func NewChannelTabs(items ...*ChannelTabItem) *ChannelTabs {
	ct := &ChannelTabs{items: items}
	ct.ExtendBaseWidget(ct)

	ct.headers = make([]*channelTabHeader, len(items))
	for i, item := range items {
		idx := i
		ct.headers[i] = newChannelTabHeader(item.Label, item.Color, func() {
			ct.SetActive(idx)
		})
	}
	ct.headers[0].active = true

	ct.slot = container.NewMax(items[0].Content)

	borderRect := canvas.NewRectangle(color.Transparent)
	borderRect.StrokeColor = color.NRGBA{R: 0x44, G: 0x44, B: 0x44, A: 0xff}
	borderRect.StrokeWidth = 1
	borderRect.CornerRadius = 4

	body := container.NewMax(container.NewPadded(ct.slot), borderRect)

	headerRow := container.NewHBox()
	for _, h := range ct.headers {
		headerRow.Add(h)
	}

	ct.root = container.NewVBox(headerRow, body)
	return ct
}

// SetActive switches to tab idx.
func (ct *ChannelTabs) SetActive(idx int) {
	if idx < 0 || idx >= len(ct.items) || idx == ct.active {
		return
	}
	ct.active = idx
	for i, h := range ct.headers {
		h.active = i == idx
		h.Refresh()
	}
	ct.slot.Objects = []fyne.CanvasObject{ct.items[idx].Content}
	ct.slot.Refresh()
}

func (ct *ChannelTabs) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(ct.root)
}
