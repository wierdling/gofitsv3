package ui

import (
	"fmt"
	"image/color"
	"math"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/mosaic"
)

type mosaicFilterPreview struct {
	root            *fyne.Container
	label           *widget.Label
	cachedGroups    []mosaic.FootprintPreview
	cachedWidth     int
	cachedHeight    int
	renderedSize    fyne.Size
	hasCachedPlan   bool
	renderScheduled bool
}

const (
	previewLabelTextSize    = 18
	previewLabelMinDistance = 24
)

// mosaicFilterPreviewLayout keeps a useful minimum for the standalone window
// while allowing the preview content to expand with that window.
type mosaicFilterPreviewLayout struct {
	min      fyne.Size
	onResize func(fyne.Size)
}

func (l *mosaicFilterPreviewLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	for _, object := range objects {
		object.Move(fyne.NewPos(0, 0))
		object.Resize(size)
	}
	if l.onResize != nil {
		l.onResize(size)
	}
}

func (l *mosaicFilterPreviewLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	min := l.min
	for _, object := range objects {
		size := object.MinSize()
		if size.Width > min.Width {
			min.Width = size.Width
		}
		if size.Height > min.Height {
			min.Height = size.Height
		}
	}
	return min
}

func newMosaicFilterPreview() *mosaicFilterPreview {
	p := &mosaicFilterPreview{label: widget.NewLabel("Select files to preview their drizzle footprints.")}
	// The custom layout lets the footprint drawing use all available space in
	// the standalone preview window while retaining a useful minimum size.
	layout := &mosaicFilterPreviewLayout{min: fyne.NewSize(300, 200)}
	p.root = container.New(layout, p.label)
	layout.onResize = func(size fyne.Size) {
		if !p.hasCachedPlan || size == p.renderedSize || p.renderScheduled {
			return
		}
		p.renderScheduled = true
		fyne.Do(func() {
			p.renderScheduled = false
			if p.hasCachedPlan && p.root.Size() != p.renderedSize {
				p.renderCached()
			}
		})
	}
	p.root.Resize(fyne.NewSize(520, 420))
	return p
}

func (p *mosaicFilterPreview) loading() {
	p.hasCachedPlan = false
	p.root.Objects = []fyne.CanvasObject{widget.NewLabel("Reading WCS metadata…")}
	p.root.Refresh()
}

func (p *mosaicFilterPreview) show(groups []mosaic.FootprintPreview, width, height int) {
	p.cachedGroups = append(p.cachedGroups[:0], groups...)
	p.cachedWidth = width
	p.cachedHeight = height
	p.hasCachedPlan = true
	p.renderCached()
}

func (p *mosaicFilterPreview) renderCached() {
	groups, width, height := p.cachedGroups, p.cachedWidth, p.cachedHeight
	p.renderedSize = p.root.Size()
	if width <= 0 || height <= 0 {
		message := "No valid WCS footprints available."
		if warning := mosaicFilterPreviewWarning(groups); warning != "" {
			message += "\n" + warning
		}
		p.root.Objects = []fyne.CanvasObject{widget.NewLabel(message)}
		p.root.Refresh()
		return
	}
	const pad float32 = 12
	previewSize := p.root.Size()
	if previewSize.Width <= 0 || previewSize.Height <= 0 {
		previewSize = fyne.NewSize(420, 200)
	}
	availW, availH := previewSize.Width-2*pad, previewSize.Height-2*pad
	// The caption occupies the bottom of the bordered preview container.
	if availH > 32 {
		availH -= 32
	}
	s := float32(math.Min(float64(availW/float32(width)), float64(availH/float32(height))))
	if s <= 0 {
		s = 1
	}
	o := container.NewWithoutLayout(canvas.NewRectangle(color.RGBA{R: 20, G: 24, B: 30, A: 255}))
	o.Objects[0].Resize(fyne.NewSize(float32(width)*s+2*pad, float32(height)*s+2*pad))
	colors := []color.NRGBA{{R: 255, G: 190, B: 70, A: 255}, {R: 80, G: 210, B: 255, A: 255}, {R: 180, G: 120, B: 255, A: 255}, {R: 100, G: 240, B: 130, A: 255}}
	var labelPositions []fyne.Position
	for gi, group := range groups {
		c := colors[gi%len(colors)]
		for _, fp := range group.Footprints {
			pairs := [][2]int{{0, 1}, {1, 3}, {3, 2}, {2, 0}}
			for _, pair := range pairs {
				a, b := fp[pair[0]], fp[pair[1]]
				line := canvas.NewLine(c)
				line.StrokeWidth = 2
				line.Position1 = fyne.NewPos(pad+float32(a[0])*s, pad+float32(a[1])*s)
				line.Position2 = fyne.NewPos(pad+float32(b[0])*s, pad+float32(b[1])*s)
				o.Add(line)
			}
			if len(fp) > 0 {
				lbl := canvas.NewText(fmt.Sprintf("%d", group.SourceNumber), c)
				lbl.TextSize = previewLabelTextSize
				labelSize := lbl.MinSize()
				anchor := footprintPreviewLabelAnchor(fp, s, pad, labelSize)
				occupiedSize := fyne.NewSize(labelSize.Width+1, labelSize.Height+1)
				labelPos := deconflictedPreviewLabelPositionBounded(anchor, labelPositions, float32(width)*s+2*pad, float32(height)*s+2*pad, occupiedSize)
				labelPositions = append(labelPositions, labelPos)
				// A small dark copy keeps labels legible when they cross a bright
				// footprint line, while the colored copy preserves source identity.
				shadow := canvas.NewText(lbl.Text, color.NRGBA{A: 255})
				shadow.TextSize = lbl.TextSize
				shadow.Move(fyne.NewPos(labelPos.X+1, labelPos.Y+1))
				o.Add(shadow)
				lbl.Move(labelPos)
				o.Add(lbl)
			}
		}
	}
	captionText := "Numbered outlines show source placement; colors distinguish files."
	if warning := mosaicFilterPreviewWarning(groups); warning != "" {
		captionText += "\n" + warning
	}
	caption := widget.NewLabel(captionText)
	p.root.Objects = []fyne.CanvasObject{container.NewBorder(nil, caption, nil, nil, o)}
	p.root.Refresh()
}

// deconflictedPreviewLabelPosition places labels in stable nearby slots so
// overlapping footprints do not render their source numbers on top of one
// another. The input order is the deterministic source/extension order.
func deconflictedPreviewLabelPosition(anchor fyne.Position, placed []fyne.Position) fyne.Position {
	const minDistance float32 = previewLabelMinDistance
	offsets := []fyne.Position{
		{X: 0, Y: 0}, {X: 24, Y: 0}, {X: 0, Y: 24}, {X: 24, Y: 24},
		{X: -24, Y: 0}, {X: -24, Y: 24}, {X: 48, Y: 0}, {X: 48, Y: 24},
	}
	for _, offset := range offsets {
		candidate := fyne.NewPos(anchor.X+offset.X, anchor.Y+offset.Y)
		clear := true
		for _, other := range placed {
			dx, dy := candidate.X-other.X, candidate.Y-other.Y
			if dx*dx+dy*dy < minDistance*minDistance {
				clear = false
				break
			}
		}
		if clear {
			return candidate
		}
	}
	// More than eight labels can share one point; continue deterministically
	// down a diagonal rather than reverting to an unreadable overlap.
	step := float32(len(placed)-len(offsets)+1) * minDistance
	return fyne.NewPos(anchor.X+step, anchor.Y+step)
}

func deconflictedPreviewLabelPositionBounded(anchor fyne.Position, placed []fyne.Position, width, height float32, labelSize fyne.Size) fyne.Position {
	const minDistance float32 = previewLabelMinDistance
	// Search stable anchor-relative rings. Candidates are bounded only after
	// applying the offset, and the final positions are checked for spacing, so
	// edge clamping cannot collapse two slots or detach labels from the anchor.
	offsets := make([]fyne.Position, 0, 128)
	offsets = append(offsets, fyne.NewPos(0, 0))
	maxRadius := int(math.Max(float64(width), float64(height))) * 2
	for radius := int(minDistance); radius <= maxRadius; radius += int(minDistance) {
		for y := -radius; y <= radius; y += int(minDistance) {
			for x := -radius; x <= radius; x += int(minDistance) {
				if int(math.Max(math.Abs(float64(x)), math.Abs(float64(y)))) != radius {
					continue
				}
				offsets = append(offsets, fyne.NewPos(float32(x), float32(y)))
			}
		}
	}
	for _, offset := range offsets {
		candidate := boundedPreviewLabelPosition(fyne.NewPos(anchor.X+offset.X, anchor.Y+offset.Y), width, height, labelSize)
		clear := true
		for _, other := range placed {
			dx, dy := candidate.X-other.X, candidate.Y-other.Y
			if dx*dx+dy*dy < minDistance*minDistance {
				clear = false
				break
			}
		}
		if clear {
			return candidate
		}
	}
	return boundedPreviewLabelPosition(anchor, width, height, labelSize)
}

func boundedPreviewLabelPosition(pos fyne.Position, width, height float32, labelSize fyne.Size) fyne.Position {
	return fyne.NewPos(clampPreviewLabelCoordinate(pos.X, width-labelSize.Width-12), clampPreviewLabelCoordinate(pos.Y, height-labelSize.Height-12))
}

func clampPreviewLabelCoordinate(value, maximum float32) float32 {
	if maximum < 12 {
		return maximum
	}
	return float32(math.Max(12, math.Min(float64(value), float64(maximum))))
}

func footprintPreviewCenter(fp [4][2]float64) [2]float64 {
	var center [2]float64
	for _, corner := range fp {
		center[0] += corner[0]
		center[1] += corner[1]
	}
	center[0] /= float64(len(fp))
	center[1] /= float64(len(fp))
	return center
}

func footprintPreviewLabelAnchor(fp [4][2]float64, scale, pad float32, labelSize fyne.Size) fyne.Position {
	center := footprintPreviewCenter(fp)
	return fyne.NewPos(
		pad+float32(center[0])*scale-labelSize.Width/2,
		pad+float32(center[1])*scale-labelSize.Height/2,
	)
}

func mosaicFilterPreviewWarning(groups []mosaic.FootprintPreview) string {
	warnings := 0
	for _, group := range groups {
		if group.Error != "" {
			warnings++
		}
	}
	if warnings > 0 {
		return fmt.Sprintf("%d source(s) have invalid/missing WCS and no outline.", warnings)
	}
	return ""
}
