package processing

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"gofitsv3/internal/debuglog"
	"gofitsv3/internal/fitsio"
)

// D2ITable is a 2D lookup table for detector-to-image pixel correction,
// as stored in D2IMARR FITS extensions of HST calibrated (FLC/FLT) files.
// The correction value at a given 0-indexed image pixel (x, y) is found by
// bilinear interpolation and represents an additive offset in pixels.
type D2ITable struct {
	crpix1, crpix2 float64 // 1-indexed reference pixel in the lookup table
	crval1, crval2 float64 // FITS 1-indexed image coords at the reference pixel
	cdelt1, cdelt2 float64 // image pixels per lookup table step
	width, height  int
	data           []float32
}

// ParseD2ITableFromHDU constructs a D2ITable from a D2IMARR HDU.
func ParseD2ITableFromHDU(hdu fitsio.HDU) (*D2ITable, error) {
	if hdu.Data.Width == 0 || hdu.Data.Height == 0 {
		return nil, fmt.Errorf("D2IMARR HDU has no data")
	}
	crpix1, ok1 := tryHeaderFloat(hdu.Header, "CRPIX1")
	crpix2, ok2 := tryHeaderFloat(hdu.Header, "CRPIX2")
	crval1, ok3 := tryHeaderFloat(hdu.Header, "CRVAL1")
	crval2, ok4 := tryHeaderFloat(hdu.Header, "CRVAL2")
	cdelt1, ok5 := tryHeaderFloat(hdu.Header, "CDELT1")
	cdelt2, ok6 := tryHeaderFloat(hdu.Header, "CDELT2")
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || !ok6 {
		return nil, fmt.Errorf("D2IMARR HDU missing coordinate keywords")
	}
	if math.Abs(cdelt1) < 1e-15 || math.Abs(cdelt2) < 1e-15 {
		return nil, fmt.Errorf("D2IMARR HDU has zero CDELT")
	}
	t := &D2ITable{
		crpix1: crpix1, crpix2: crpix2,
		crval1: crval1, crval2: crval2,
		cdelt1: cdelt1, cdelt2: cdelt2,
		width:  hdu.Data.Width,
		height: hdu.Data.Height,
		data:   make([]float32, len(hdu.Data.Pixels)),
	}
	copy(t.data, hdu.Data.Pixels)
	return t, nil
}

// interpolate returns the correction (in pixels) for 0-indexed image pixel (x, y).
func (t *D2ITable) interpolate(imgX, imgY float64) float64 {
	// Map 0-indexed image pixel to 0-indexed lookup table coords via the
	// FITS WCS keywords stored on the D2IMARR extension.
	lx := (imgX+1-t.crval1)/t.cdelt1 + t.crpix1 - 1
	ly := (imgY+1-t.crval2)/t.cdelt2 + t.crpix2 - 1
	return bilinearSampleD2I(t.data, t.width, t.height, lx, ly)
}

func bilinearSampleD2I(data []float32, w, h int, x, y float64) float64 {
	x0 := int(math.Floor(x))
	y0 := int(math.Floor(y))
	x1 := x0 + 1
	y1 := y0 + 1
	x0 = clampInt(x0, 0, w-1)
	x1 = clampInt(x1, 0, w-1)
	y0 = clampInt(y0, 0, h-1)
	y1 = clampInt(y1, 0, h-1)
	wx := x - math.Floor(x)
	wy := y - math.Floor(y)
	p00 := float64(data[y0*w+x0])
	p10 := float64(data[y0*w+x1])
	p01 := float64(data[y1*w+x0])
	p11 := float64(data[y1*w+x1])
	return p00*(1-wx)*(1-wy) + p10*wx*(1-wy) + p01*(1-wx)*wy + p11*wx*wy
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// WCSMapper projects source pixels to reference pixels using the full
// SIP + D2IMARR WCS pipeline, without any linear approximation.
type WCSMapper struct {
	sourceWCS linearWCS
	refWCS    linearWCS
}

// NewWCSMapper constructs a WCSMapper from headers and optional D2I tables.
// Pass nil for d2iX/d2iY if the image has no D2IMARR corrections.
func NewWCSMapper(srcHeader fitsio.Header, srcD2IX, srcD2IY *D2ITable, refHeader fitsio.Header, refD2IX, refD2IY *D2ITable) (*WCSMapper, error) {
	src, err := parseLinearWCS(srcHeader)
	if err != nil {
		return nil, err
	}
	src.d2iX = srcD2IX
	src.d2iY = srcD2IY

	ref, err := parseLinearWCS(refHeader)
	if err != nil {
		return nil, err
	}
	ref.d2iX = refD2IX
	ref.d2iY = refD2IY

	return &WCSMapper{sourceWCS: src, refWCS: ref}, nil
}

// NewWCSMapperToLinearRef constructs a mapper for drizzle output. Source pixels
// are corrected through their own distortion model, then projected onto the
// reference image's linear tangent plane without applying the reference chip's
// inverse detector distortion. This avoids extrapolating a reference chip SIP
// solution across other chips in multi-detector instruments such as WFPC2.
func NewWCSMapperToLinearRef(srcHeader fitsio.Header, srcD2IX, srcD2IY *D2ITable, refHeader fitsio.Header) (*WCSMapper, error) {
	mapper, err := NewWCSMapper(srcHeader, srcD2IX, srcD2IY, refHeader, nil, nil)
	if err != nil {
		return nil, err
	}
	mapper.refWCS.a = sipPoly{}
	mapper.refWCS.b = sipPoly{}
	mapper.refWCS.ap = sipPoly{}
	mapper.refWCS.bp = sipPoly{}
	mapper.refWCS.d2iX = nil
	mapper.refWCS.d2iY = nil
	return mapper, nil
}

// MapPixel maps a 0-indexed source pixel (x, y) to a 0-indexed reference
// pixel using the full SIP + D2I pipeline.
func (m *WCSMapper) MapPixel(x, y float64) (float64, float64) {
	ra, dec := pixelToWorldLinear(x, y, m.sourceWCS)
	rx, ry, _ := worldToPixelLinear(ra, dec, m.refWCS)
	return rx, ry
}

// MapPixelDiag maps one pixel and returns a diagnostic string tracing every
// intermediate value through the full WCS pipeline. Useful for debugging
// extreme or NaN outputs.
func (m *WCSMapper) MapPixelDiag(x, y float64) (float64, float64, string) {
	src := m.sourceWCS
	ref := m.refWCS

	xd, yd := x, y
	if src.d2iX != nil {
		xd += src.d2iX.interpolate(x, y)
	}
	if src.d2iY != nil {
		yd += src.d2iY.interpolate(x, y)
	}
	dx := (xd + 1) - src.crpix1
	dy := (yd + 1) - src.crpix2
	dxSIP, dySIP := dx, dy
	if !src.a.empty() || !src.b.empty() {
		dxSIP, dySIP = applyForwardSIP(src, dx, dy)
	}
	xi := src.cd11*dxSIP + src.cd12*dySIP
	eta := src.cd21*dxSIP + src.cd22*dySIP
	cosSrc := math.Cos(src.crval2 * math.Pi / 180)
	if math.Abs(cosSrc) < 1e-9 {
		cosSrc = 1e-9
	}
	ra := src.crval1 + xi/cosSrc
	dec := src.crval2 + eta

	det := ref.cd11*ref.cd22 - ref.cd12*ref.cd21
	cosRef := math.Cos(ref.crval2 * math.Pi / 180)
	if math.Abs(cosRef) < 1e-9 {
		cosRef = 1e-9
	}
	d1 := normalizeAngleDelta(ra-ref.crval1) * cosRef
	d2 := dec - ref.crval2
	rdx := (ref.cd22*d1 - ref.cd12*d2) / det
	rdy := (-ref.cd21*d1 + ref.cd11*d2) / det
	rdxSIP, rdySIP := rdx, rdy
	if !ref.a.empty() || !ref.b.empty() {
		rdxSIP, rdySIP = applyInverseSIP(ref, rdx, rdy)
	}
	rx := rdxSIP + ref.crpix1 - 1
	ry := rdySIP + ref.crpix2 - 1

	diag := fmt.Sprintf(
		"pixel(%.1f,%.1f) d2i(%.3g,%.3g) dx(%.3g,%.3g) sipDx(%.3g,%.3g) xi(%.3g,%.3g) ra/dec(%.6f,%.6f) d1/d2(%.3g,%.3g) rdx(%.3g,%.3g) sipRdx(%.3g,%.3g) out(%.3g,%.3g)",
		x, y, xd-x, yd-y, dx, dy, dxSIP, dySIP, xi, eta, ra, dec, d1, d2, rdx, rdy, rdxSIP, rdySIP, rx, ry,
	)
	return rx, ry, diag
}

type sipCoeff struct {
	p     int
	q     int
	coeff float64
}

type sipPoly struct {
	coeffs []sipCoeff
	maxP   int
	maxQ   int
}

func (p sipPoly) empty() bool {
	return len(p.coeffs) == 0
}

type linearWCS struct {
	crpix1 float64
	crpix2 float64
	crval1 float64
	crval2 float64
	cd11   float64
	cd12   float64
	cd21   float64
	cd22   float64
	a      sipPoly
	b      sipPoly
	ap     sipPoly
	bp     sipPoly
	d2iX   *D2ITable
	d2iY   *D2ITable
}

func AlignChannelUsingWCS(targetPixels []float32, targetWidth, targetHeight int, targetHeader fitsio.Header, refPixels []float32, refWidth, refHeight int, refHeader fitsio.Header) ([]float32, AffineTransform, error) {
	debuglog.Log("AlignChannelUsingWCS: starting")
	defer debuglog.Log("AlignChannelUsingWCS: finished")
	if targetWidth <= 0 || targetHeight <= 0 || refWidth <= 0 || refHeight <= 0 {
		return nil, AffineTransform{}, fmt.Errorf("invalid image dimensions (target: %dx%d, ref: %dx%d)", targetWidth, targetHeight, refWidth, refHeight)
	}

	transform, err := ComputeWCSTransform(targetHeader, refHeader)
	if err != nil {
		return nil, AffineTransform{}, err
	}

	alignedPixels := WarpImageToSize(targetPixels, targetWidth, targetHeight, refWidth, refHeight, transform)
	if len(alignedPixels) != refWidth*refHeight {
		return nil, AffineTransform{}, fmt.Errorf("unexpected aligned pixel count: got %d want %d", len(alignedPixels), refWidth*refHeight)
	}
	return alignedPixels, transform, nil
}

func ComputeWCSTransform(targetHeader fitsio.Header, refHeader fitsio.Header) (AffineTransform, error) {
	debuglog.Log("ComputeWCSTransform: starting")
	defer debuglog.Log("ComputeWCSTransform: finished")
	targetWCS, err := parseLinearWCS(targetHeader)
	if err != nil {
		return AffineTransform{}, err
	}
	refWCS, err := parseLinearWCS(refHeader)
	if err != nil {
		return AffineTransform{}, err
	}

	x00, y00, err := mapRefPixelToTargetPixel(0, 0, refWCS, targetWCS)
	if err != nil {
		return AffineTransform{}, err
	}
	x10, y10, err := mapRefPixelToTargetPixel(1, 0, refWCS, targetWCS)
	if err != nil {
		return AffineTransform{}, err
	}
	x01, y01, err := mapRefPixelToTargetPixel(0, 1, refWCS, targetWCS)
	if err != nil {
		return AffineTransform{}, err
	}

	return AffineTransform{
		A: x10 - x00,
		B: x01 - x00,
		C: x00,
		D: y10 - y00,
		E: y01 - y00,
		F: y00,
	}, nil
}

func parseLinearWCS(header fitsio.Header) (linearWCS, error) {
	crpix1, err := headerFloat(header, "CRPIX1")
	if err != nil {
		return linearWCS{}, err
	}
	crpix2, err := headerFloat(header, "CRPIX2")
	if err != nil {
		return linearWCS{}, err
	}
	crval1, err := headerFloat(header, "CRVAL1")
	if err != nil {
		return linearWCS{}, err
	}
	crval2, err := headerFloat(header, "CRVAL2")
	if err != nil {
		return linearWCS{}, err
	}

	cd11, ok11 := tryHeaderFloat(header, "CD1_1")
	cd12, ok12 := tryHeaderFloat(header, "CD1_2")
	cd21, ok21 := tryHeaderFloat(header, "CD2_1")
	cd22, ok22 := tryHeaderFloat(header, "CD2_2")
	if ok11 && ok12 && ok21 && ok22 {
		w := linearWCS{crpix1: crpix1, crpix2: crpix2, crval1: crval1, crval2: crval2, cd11: cd11, cd12: cd12, cd21: cd21, cd22: cd22}
		parseSIP(header, &w)
		return w, nil
	}

	cdelt1, err1 := headerFloat(header, "CDELT1")
	cdelt2, err2 := headerFloat(header, "CDELT2")
	if err1 != nil || err2 != nil {
		return linearWCS{}, fmt.Errorf("missing WCS matrix: need CD* or PC*+CDELT* keywords")
	}

	pc11, ok11 := tryHeaderFloat(header, "PC1_1")
	pc12, ok12 := tryHeaderFloat(header, "PC1_2")
	pc21, ok21 := tryHeaderFloat(header, "PC2_1")
	pc22, ok22 := tryHeaderFloat(header, "PC2_2")
	if ok11 && ok12 && ok21 && ok22 {
		// FITS standard: CD_ij = PC_ij * CDELT_i (row index selects CDELT).
		w := linearWCS{
			crpix1: crpix1,
			crpix2: crpix2,
			crval1: crval1,
			crval2: crval2,
			cd11:   pc11 * cdelt1,
			cd12:   pc12 * cdelt1,
			cd21:   pc21 * cdelt2,
			cd22:   pc22 * cdelt2,
		}
		parseSIP(header, &w)
		return w, nil
	}

	w := linearWCS{crpix1: crpix1, crpix2: crpix2, crval1: crval1, crval2: crval2, cd11: cdelt1, cd12: 0, cd21: 0, cd22: cdelt2}
	parseSIP(header, &w)
	return w, nil
}

func mapRefPixelToTargetPixel(refX, refY float64, refWCS, targetWCS linearWCS) (float64, float64, error) {
	world1, world2 := pixelToWorldLinear(refX, refY, refWCS)
	return worldToPixelLinear(world1, world2, targetWCS)
}

// pixelToWorldLinear converts 0-indexed pixel (x, y) to sky (RA, Dec) in degrees.
// Applies D2IMARR correction first (if present), then SIP, then the CD matrix.
func pixelToWorldLinear(x, y float64, w linearWCS) (float64, float64) {
	if w.d2iX != nil {
		x += w.d2iX.interpolate(x, y)
	}
	if w.d2iY != nil {
		y += w.d2iY.interpolate(x, y)
	}
	dx := (x + 1) - w.crpix1
	dy := (y + 1) - w.crpix2
	if !w.a.empty() || !w.b.empty() {
		dx, dy = applyForwardSIP(w, dx, dy)
	}
	xi := w.cd11*dx + w.cd12*dy
	eta := w.cd21*dx + w.cd22*dy
	cosDec := math.Cos(w.crval2 * math.Pi / 180)
	if math.Abs(cosDec) < 1e-9 {
		cosDec = 1e-9
	}
	return w.crval1 + xi/cosDec, w.crval2 + eta
}

// worldToPixelLinear converts sky (RA, Dec) in degrees to 0-indexed pixel (x, y).
// Undoes the D2IMARR correction (if present) via fixed-point iteration after SIP.
func worldToPixelLinear(ra, dec float64, w linearWCS) (float64, float64, error) {
	det := w.cd11*w.cd22 - w.cd12*w.cd21
	if math.Abs(det) < 1e-18 {
		return 0, 0, fmt.Errorf("singular WCS matrix")
	}
	cosDec := math.Cos(w.crval2 * math.Pi / 180)
	if math.Abs(cosDec) < 1e-9 {
		cosDec = 1e-9
	}
	d1 := normalizeAngleDelta(ra-w.crval1) * cosDec
	d2 := dec - w.crval2
	dx := (w.cd22*d1 - w.cd12*d2) / det
	dy := (-w.cd21*d1 + w.cd11*d2) / det
	if !w.a.empty() || !w.b.empty() {
		dx, dy = applyInverseSIP(w, dx, dy)
	}
	// xImg/yImg is the D2I-corrected image pixel (0-indexed).
	xImg := dx + w.crpix1 - 1
	yImg := dy + w.crpix2 - 1
	if w.d2iX == nil && w.d2iY == nil {
		return xImg, yImg, nil
	}
	// Invert D2I: find detector pixel xDet such that xDet + d2i(xDet) == xImg.
	// Fixed-point iteration converges quickly because corrections are < 1 pixel.
	xDet, yDet := xImg, yImg
	for range 5 {
		var cx, cy float64
		if w.d2iX != nil {
			cx = w.d2iX.interpolate(xDet, yDet)
		}
		if w.d2iY != nil {
			cy = w.d2iY.interpolate(xDet, yDet)
		}
		xDet = xImg - cx
		yDet = yImg - cy
	}
	return xDet, yDet, nil
}

func parseSIP(header fitsio.Header, w *linearWCS) {
	w.a = parseSIPPoly(header, "A")
	w.b = parseSIPPoly(header, "B")
	w.ap = parseSIPPoly(header, "AP")
	w.bp = parseSIPPoly(header, "BP")
}

func parseSIPPoly(header fitsio.Header, prefix string) sipPoly {
	order, ok := tryHeaderFloat(header, prefix+"_ORDER")
	if !ok || order < 0 {
		return sipPoly{}
	}
	maxOrder := int(order)
	coeffs := make([]sipCoeff, 0, (maxOrder+1)*(maxOrder+1))
	maxP, maxQ := 0, 0
	for p := 0; p <= maxOrder; p++ {
		for q := 0; q <= maxOrder; q++ {
			key := fmt.Sprintf("%s_%d_%d", prefix, p, q)
			if v, ok := tryHeaderFloat(header, key); ok {
				coeffs = append(coeffs, sipCoeff{p: p, q: q, coeff: v})
				if p > maxP {
					maxP = p
				}
				if q > maxQ {
					maxQ = q
				}
			}
		}
	}
	if len(coeffs) == 0 {
		return sipPoly{}
	}
	return sipPoly{coeffs: coeffs, maxP: maxP, maxQ: maxQ}
}

func applySIPPolynomial(poly sipPoly, u, v float64) float64 {
	if poly.empty() {
		return 0
	}
	var smallU [16]float64
	var smallV [16]float64
	uPows := smallU[:]
	if poly.maxP+1 > len(uPows) {
		uPows = make([]float64, poly.maxP+1)
	} else {
		uPows = uPows[:poly.maxP+1]
	}
	vPows := smallV[:]
	if poly.maxQ+1 > len(vPows) {
		vPows = make([]float64, poly.maxQ+1)
	} else {
		vPows = vPows[:poly.maxQ+1]
	}
	uPows[0] = 1
	vPows[0] = 1
	for i := 1; i <= poly.maxP; i++ {
		uPows[i] = uPows[i-1] * u
	}
	for i := 1; i <= poly.maxQ; i++ {
		vPows[i] = vPows[i-1] * v
	}
	sum := 0.0
	for _, term := range poly.coeffs {
		sum += term.coeff * uPows[term.p] * vPows[term.q]
	}
	return sum
}

func applyForwardSIP(w linearWCS, u, v float64) (float64, float64) {
	return u + applySIPPolynomial(w.a, u, v), v + applySIPPolynomial(w.b, u, v)
}

func isFinite64(x float64) bool {
	return !math.IsNaN(x) && !math.IsInf(x, 0)
}

func applyInverseSIP(w linearWCS, u, v float64) (float64, float64) {
	if !w.ap.empty() || !w.bp.empty() {
		cu := applySIPPolynomial(w.ap, u, v)
		cv := applySIPPolynomial(w.bp, u, v)
		// SIP corrections for in-domain pixels are sub-pixel to a few pixels.
		// Out-of-domain extrapolation produces enormous or NaN values — discard.
		if isFinite64(cu) && isFinite64(cv) && math.Abs(cu) < 1000 && math.Abs(cv) < 1000 {
			return u + cu, v + cv
		}
		return u, v
	}
	guessU, guessV := u, v
	for range 8 {
		fwdU, fwdV := applyForwardSIP(w, guessU, guessV)
		if !isFinite64(fwdU) || !isFinite64(fwdV) {
			return u, v
		}
		guessU += u - fwdU
		guessV += v - fwdV
	}
	if isFinite64(guessU) && isFinite64(guessV) && math.Abs(guessU-u) < 1000 && math.Abs(guessV-v) < 1000 {
		return guessU, guessV
	}
	return u, v
}

func headerFloat(header fitsio.Header, key string) (float64, error) {
	if v, ok := tryHeaderFloat(header, key); ok {
		return v, nil
	}
	return 0, fmt.Errorf("missing %s in FITS header", key)
}

func tryHeaderFloat(header fitsio.Header, key string) (float64, bool) {
	raw, ok := header.Cards[key]
	if !ok {
		return 0, false
	}
	val := raw
	if idx := strings.Index(val, "/"); idx >= 0 {
		val = val[:idx]
	}
	val = strings.TrimSpace(strings.Trim(val, "'"))
	if val == "" {
		return 0, false
	}
	parsed, err := strconv.ParseFloat(val, 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func normalizeAngleDelta(delta float64) float64 {
	for delta > 180 {
		delta -= 360
	}
	for delta < -180 {
		delta += 360
	}
	return delta
}

// CenterDistInRefPixels returns the distance (in reference image pixels) between
// the center of the input image and the center of the reference image, using WCS
// to project the input center into reference pixel space.  Returns an error if
// either header lacks usable WCS keywords.
func CenterDistInRefPixels(
	inputHeader fitsio.Header, inputWidth, inputHeight int,
	refHeader fitsio.Header, refWidth, refHeight int,
) (float64, error) {
	inputWCS, err := parseLinearWCS(inputHeader)
	if err != nil {
		return 0, err
	}
	refWCS, err := parseLinearWCS(refHeader)
	if err != nil {
		return 0, err
	}
	// Convert input center pixel ? sky.
	ra, dec := pixelToWorldLinear(float64(inputWidth)/2.0, float64(inputHeight)/2.0, inputWCS)
	// Convert sky ? reference pixel space.
	rx, ry, err := worldToPixelLinear(ra, dec, refWCS)
	if err != nil {
		return 0, err
	}
	rcx := float64(refWidth) / 2.0
	rcy := float64(refHeight) / 2.0
	return math.Sqrt((rx-rcx)*(rx-rcx) + (ry-rcy)*(ry-rcy)), nil
}
