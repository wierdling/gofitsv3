package ui

import (
	"image"
	"image/color"
	"math"
	"sort"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/widget"
)

// curvePoint is a control point in normalised (0–1) space.
type curvePoint struct {
	X, Y float32
}

// curvesWidget is an interactive tone-curve editor.
//
// It stores four independent curves (0=All, 1=R, 2=G, 3=B).
// The user drags existing points or clicks-and-drags empty space to add new
// ones. Double-clicking a non-endpoint point removes it.
// Endpoints (X=0 and X=1) cannot be removed, but their Y can be moved.
//
// ToLUT(ch) composes the "All" curve with the channel-specific curve.
type curvesWidget struct {
	widget.BaseWidget
	points    [4][]curvePoint // 0=All, 1=R, 2=G, 3=B
	active    int             // 0=All, 1=R, 2=G, 3=B
	dragIdx   int             // index of point being dragged, -1 = none
	lastSize  fyne.Size       // widget size captured during the last Dragged call
	onChange  func()
	onDragEnd func()
}

var curveChannelColors = [4]color.NRGBA{
	{R: 220, G: 220, B: 220, A: 255}, // All
	{R: 255, G: 80, B: 80, A: 255},   // Red
	{R: 80, G: 200, B: 80, A: 255},   // Green
	{R: 80, G: 130, B: 255, A: 255},  // Blue
}

// dimColor darkens a channel colour so inactive (read-only) curves read as
// background context behind the active, editable curve.
func dimColor(c color.NRGBA) color.NRGBA {
	return color.NRGBA{R: c.R / 2, G: c.G / 2, B: c.B / 2, A: c.A}
}

// isIdentityCurve reports whether a curve is still the default identity mapping
// (the two unmoved endpoints), i.e. the user has not set it.
func isIdentityCurve(pts []curvePoint) bool {
	return len(pts) == 2 &&
		pts[0] == curvePoint{0, 0} &&
		pts[1] == curvePoint{1, 1}
}

func newCurvesWidget(onChange func()) *curvesWidget {
	cw := &curvesWidget{dragIdx: -1, onChange: onChange}
	for i := range cw.points {
		cw.points[i] = []curvePoint{{0, 0}, {1, 1}}
	}
	cw.ExtendBaseWidget(cw)
	return cw
}

// SetActive changes which channel curve is displayed and edited.
func (cw *curvesWidget) SetActive(ch int) {
	cw.active = ch
	cw.Refresh()
}

// Reset restores all four curves to the identity mapping.
func (cw *curvesWidget) Reset() {
	for i := range cw.points {
		cw.points[i] = []curvePoint{{0, 0}, {1, 1}}
	}
	cw.Refresh()
}

// ToLUT returns the composed output LUT for the given channel (0=R, 1=G, 2=B).
// It applies the All curve first, then the per-channel curve.
func (cw *curvesWidget) ToLUT(ch int) [256]byte {
	allLUT := computeSplineLUT(cw.points[0])
	chLUT := computeSplineLUT(cw.points[ch+1])
	var out [256]byte
	for i := 0; i < 256; i++ {
		out[i] = chLUT[allLUT[i]]
	}
	return out
}

// --- interaction ---

func (cw *curvesWidget) Dragged(e *fyne.DragEvent) {
	sz := cw.Size()
	if sz.Width <= 0 || sz.Height <= 0 {
		return
	}
	cw.lastSize = sz
	nx := clampF32(float32(e.Position.X)/float32(sz.Width), 0, 1)
	ny := clampF32(1.0-float32(e.Position.Y)/float32(sz.Height), 0, 1)

	pts := &cw.points[cw.active]

	if cw.dragIdx == -1 {
		// Find nearest existing point in pixel space
		nearest, nearestDistPx := -1, float32(math.MaxFloat32)
		for i, p := range *pts {
			dx := (p.X - nx) * float32(sz.Width)
			dy := (p.Y - ny) * float32(sz.Height)
			d := float32(math.Sqrt(float64(dx*dx + dy*dy)))
			if d < nearestDistPx {
				nearestDistPx = d
				nearest = i
			}
		}
		const snapPx = 15
		if nearestDistPx <= snapPx {
			cw.dragIdx = nearest
		} else {
			// Insert a new point in sorted X order
			insertAt := len(*pts)
			for i, p := range *pts {
				if p.X > nx {
					insertAt = i
					break
				}
			}
			*pts = append(*pts, curvePoint{})
			copy((*pts)[insertAt+1:], (*pts)[insertAt:])
			(*pts)[insertAt] = curvePoint{nx, ny}
			cw.dragIdx = insertAt
		}
	}

	if cw.dragIdx < 0 || cw.dragIdx >= len(*pts) {
		return
	}
	p := &(*pts)[cw.dragIdx]

	// Endpoints: only Y moves.
	if cw.dragIdx == 0 {
		p.Y = ny
	} else if cw.dragIdx == len(*pts)-1 {
		p.Y = ny
	} else {
		// Clamp X between neighbours so order stays sorted (no re-sort needed).
		minX := (*pts)[cw.dragIdx-1].X + 1.0/255.0
		maxX := (*pts)[cw.dragIdx+1].X - 1.0/255.0
		p.X = clampF32(nx, minX, maxX)
		p.Y = ny
	}

	cw.Refresh()
	if cw.onChange != nil {
		cw.onChange()
	}
}

func (cw *curvesWidget) DragEnd() {
	idx := cw.dragIdx
	cw.dragIdx = -1

	notify := func() {
		if cw.onChange != nil {
			cw.onChange()
		}
		if cw.onDragEnd != nil {
			cw.onDragEnd()
		}
	}

	// Only non-endpoint points can be merged or removed.
	pts := &cw.points[cw.active]
	if idx <= 0 || idx >= len(*pts)-1 {
		notify()
		return
	}

	sw := float32(cw.lastSize.Width)
	sh := float32(cw.lastSize.Height)
	if sw <= 0 || sh <= 0 {
		notify()
		return
	}

	p := (*pts)[idx]
	const threshPx = float32(15)

	// Check proximity to any other point.
	for i, other := range *pts {
		if i == idx {
			continue
		}
		dx := (other.X - p.X) * sw
		dy := (other.Y - p.Y) * sh
		if float32(math.Sqrt(float64(dx*dx+dy*dy))) <= threshPx {
			*pts = append((*pts)[:idx], (*pts)[idx+1:]...)
			cw.Refresh()
			notify()
			return
		}
	}

	// Check proximity to any canvas corner.
	corners := [4]curvePoint{{0, 0}, {0, 1}, {1, 0}, {1, 1}}
	for _, c := range corners {
		dx := (c.X - p.X) * sw
		dy := (c.Y - p.Y) * sh
		if float32(math.Sqrt(float64(dx*dx+dy*dy))) <= threshPx {
			*pts = append((*pts)[:idx], (*pts)[idx+1:]...)
			cw.Refresh()
			notify()
			return
		}
	}

	notify()
}

func (cw *curvesWidget) DoubleTapped(e *fyne.PointEvent) {
	sz := cw.Size()
	if sz.Width <= 0 {
		return
	}
	nx := clampF32(float32(e.Position.X)/float32(sz.Width), 0, 1)
	ny := clampF32(1.0-float32(e.Position.Y)/float32(sz.Height), 0, 1)

	pts := &cw.points[cw.active]
	nearest, nearestDistPx := -1, float32(math.MaxFloat32)
	for i, p := range *pts {
		dx := (p.X - nx) * float32(sz.Width)
		dy := (p.Y - ny) * float32(sz.Height)
		d := float32(math.Sqrt(float64(dx*dx + dy*dy)))
		if d < nearestDistPx {
			nearestDistPx = d
			nearest = i
		}
	}
	// Remove if within 20px and not an endpoint
	if nearestDistPx <= 20 && nearest > 0 && nearest < len(*pts)-1 {
		*pts = append((*pts)[:nearest], (*pts)[nearest+1:]...)
		cw.Refresh()
		if cw.onChange != nil {
			cw.onChange()
		}
	}
}

func (cw *curvesWidget) Tapped(*fyne.PointEvent)          {}
func (cw *curvesWidget) TappedSecondary(*fyne.PointEvent) {}

// --- rendering ---

func (cw *curvesWidget) MinSize() fyne.Size { return fyne.NewSize(200, 180) }

func (cw *curvesWidget) CreateRenderer() fyne.WidgetRenderer {
	r := canvas.NewRaster(cw.draw)
	return &curvesRenderer{widget: cw, raster: r}
}

func (cw *curvesWidget) draw(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if w <= 0 || h <= 0 {
		return img
	}

	// Background
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i] = 28
		img.Pix[i+1] = 28
		img.Pix[i+2] = 28
		img.Pix[i+3] = 255
	}

	setPixel := func(x, y int, r, g, b uint8) {
		if x < 0 || x >= w || y < 0 || y >= h {
			return
		}
		idx := y*img.Stride + x*4
		img.Pix[idx] = r
		img.Pix[idx+1] = g
		img.Pix[idx+2] = b
	}

	// Grid (4×4)
	for i := 1; i < 4; i++ {
		gx := i * w / 4
		gy := i * h / 4
		for y := 0; y < h; y++ {
			setPixel(gx, y, 55, 55, 55)
		}
		for x := 0; x < w; x++ {
			setPixel(x, gy, 55, 55, 55)
		}
	}

	// Identity diagonal
	for x := 0; x < w; x++ {
		y := h - 1
		if w > 1 {
			y = h - 1 - x*(h-1)/(w-1)
		}
		setPixel(x, y, 70, 70, 70)
	}

	// Curve – evaluate spline directly for smooth rendering.
	drawCurve := func(pts []curvePoint, col color.NRGBA) {
		n := len(pts)
		if n < 2 {
			return
		}
		sortedPts := make([]curvePoint, n)
		copy(sortedPts, pts)
		sort.Slice(sortedPts, func(i, j int) bool { return sortedPts[i].X < sortedPts[j].X })

		xs := make([]float64, n)
		ys := make([]float64, n)
		for i, p := range sortedPts {
			xs[i] = float64(p.X)
			ys[i] = float64(p.Y)
		}
		tangents := monotoneCubicTangents(xs, ys)

		prevPy := -1
		for x := 0; x < w; x++ {
			t := 0.0
			if w > 1 {
				t = float64(x) / float64(w-1)
			}
			yv := evalMonotoneCubic(xs, ys, tangents, t)
			py := h - 1 - int(clampF64(yv, 0, 1)*float64(h-1))
			// Connect vertically to previous pixel to avoid gaps
			lo, hi := py, py
			if prevPy >= 0 {
				if prevPy < py {
					lo = prevPy + 1
				} else if prevPy > py {
					hi = prevPy - 1
				}
			}
			for ry := lo; ry <= hi; ry++ {
				setPixel(x, ry, col.R, col.G, col.B)
			}
			prevPy = py
		}
	}

	// Draw any edited (non-identity) inactive channels dimmed underneath, so the
	// user can see all curves they have set, then the active curve on top.
	for ch := 0; ch < 4; ch++ {
		if ch == cw.active || isIdentityCurve(cw.points[ch]) {
			continue
		}
		drawCurve(cw.points[ch], dimColor(curveChannelColors[ch]))
	}
	pts := cw.points[cw.active]
	col := curveChannelColors[cw.active]
	drawCurve(pts, col)

	// Control points – only the active channel is editable, so only it gets handles.
	for i, p := range pts {
		px := int(p.X * float32(w-1))
		py := h - 1 - int(p.Y*float32(h-1))
		const r = 5
		for dy := -r; dy <= r; dy++ {
			for dx := -r; dx <= r; dx++ {
				d2 := dx*dx + dy*dy
				if d2 > r*r {
					continue
				}
				if d2 >= (r-1)*(r-1) {
					// Border ring – white
					setPixel(px+dx, py+dy, 255, 255, 255)
				} else if i == cw.dragIdx {
					setPixel(px+dx, py+dy, 255, 230, 80)
				} else {
					setPixel(px+dx, py+dy, col.R, col.G, col.B)
				}
			}
		}
	}

	return img
}

type curvesRenderer struct {
	widget *curvesWidget
	raster *canvas.Raster
}

func (r *curvesRenderer) Layout(size fyne.Size)        { r.raster.Resize(size) }
func (r *curvesRenderer) MinSize() fyne.Size           { return r.widget.MinSize() }
func (r *curvesRenderer) Objects() []fyne.CanvasObject { return []fyne.CanvasObject{r.raster} }
func (r *curvesRenderer) Refresh()                     { r.raster.Refresh() }
func (r *curvesRenderer) Destroy()                     {}

// --- spline math ---

// computeSplineLUT builds a 256-byte LUT using monotone cubic interpolation.
func computeSplineLUT(points []curvePoint) [256]byte {
	var lut [256]byte
	n := len(points)
	if n == 0 {
		for i := range lut {
			lut[i] = byte(i)
		}
		return lut
	}

	sorted := make([]curvePoint, n)
	copy(sorted, points)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].X < sorted[j].X })

	xs := make([]float64, n)
	ys := make([]float64, n)
	for i, p := range sorted {
		xs[i] = float64(p.X)
		ys[i] = float64(p.Y)
	}

	tangents := monotoneCubicTangents(xs, ys)
	for i := 0; i < 256; i++ {
		t := float64(i) / 255.0
		y := evalMonotoneCubic(xs, ys, tangents, t)
		lut[i] = byte(clampF64(y*255, 0, 255))
	}
	return lut
}

// monotoneCubicTangents computes Fritsch-Carlson monotone cubic tangents.
func monotoneCubicTangents(xs, ys []float64) []float64 {
	n := len(xs)
	ms := make([]float64, n)
	if n < 2 {
		return ms
	}
	hs := make([]float64, n-1)
	ds := make([]float64, n-1)
	for i := 0; i < n-1; i++ {
		hs[i] = xs[i+1] - xs[i]
		if hs[i] == 0 {
			hs[i] = 1e-10
		}
		ds[i] = (ys[i+1] - ys[i]) / hs[i]
	}
	ms[0] = ds[0]
	ms[n-1] = ds[n-2]
	for i := 1; i < n-1; i++ {
		ms[i] = (ds[i-1] + ds[i]) / 2
	}
	// Enforce monotonicity (Fritsch-Carlson step)
	for i := 0; i < n-1; i++ {
		if ds[i] == 0 {
			ms[i] = 0
			ms[i+1] = 0
			continue
		}
		a := ms[i] / ds[i]
		b := ms[i+1] / ds[i]
		h := a*a + b*b
		if h > 9 {
			tau := 3.0 / math.Sqrt(h)
			ms[i] = tau * a * ds[i]
			ms[i+1] = tau * b * ds[i]
		}
	}
	return ms
}

// evalMonotoneCubic evaluates the spline at t using cubic Hermite interpolation.
func evalMonotoneCubic(xs, ys, ms []float64, t float64) float64 {
	n := len(xs)
	if t <= xs[0] {
		return ys[0]
	}
	if t >= xs[n-1] {
		return ys[n-1]
	}
	i := sort.SearchFloat64s(xs, t) - 1
	if i < 0 {
		i = 0
	}
	if i >= n-1 {
		i = n - 2
	}
	h := xs[i+1] - xs[i]
	if h == 0 {
		return ys[i]
	}
	u := (t - xs[i]) / h
	h00 := 2*u*u*u - 3*u*u + 1
	h10 := u*u*u - 2*u*u + u
	h01 := -2*u*u*u + 3*u*u
	h11 := u*u*u - u*u
	return h00*ys[i] + h10*h*ms[i] + h01*ys[i+1] + h11*h*ms[i+1]
}

func clampF32(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampF64(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
