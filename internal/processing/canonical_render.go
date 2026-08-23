package processing

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"

	"gofitsv3/internal/histogram"
	"gofitsv3/internal/models"
	"gofitsv3/internal/render"
	"gofitsv3/internal/stretch"
)

// AlignedPlane is an unstretched plane on the reference grid. Valid marks the
// pixels inside the source footprint; invalid pixels must not participate in
// calibration or stretching.
type AlignedPlane struct {
	Pixels        []float32
	Valid         []bool
	Width, Height int
}

// ComposeRenderRequest is the immutable input to the calibrated compositor.
// Planes may be supplied by an alignment stage; when omitted, Images are
// aligned to the green image's grid using the existing fallback behavior.
type ComposeRenderRequest struct {
	Images      []*models.LoadedImage
	Planes      [3]AlignedPlane // R,G,B; takes precedence over Images
	Overlays    []OverlayLayer
	Calibration *models.ColorCalibrationState
	Result      *CalibrationResult
}

// ComposeRenderResult contains the one linear representation consumed by
// preview and high-bit-depth export, plus the derived legacy preview bytes.
type ComposeRenderResult struct {
	R, G, B            []float32
	Preview            []byte
	Width, Height      int
	Stats              [3]histogram.Stats
	Status             models.CalibrationStatus
	OverlayStatus      []models.CalibrationStatus
	OverlayDiagnostics []models.CalibrationDiagnostics
	RenderFingerprint  string
}

// AlignedPlanesForCalibration prepares immutable reference-grid snapshots and
// source-footprint masks for a background/photometry calculation. It is kept
// on the same path as rendering so calibration never treats WCS or resize
// fill pixels as valid samples.
func AlignedPlanesForCalibration(ctx context.Context, images []*models.LoadedImage) ([3]AlignedPlane, int, int, error) {
	return composeAlignedPlanes(ctx, ComposeRenderRequest{Images: images})
}

// ComposeRender is the canonical calibrated float compositor. A disabled,
// stale, unsupported, or failed calibration is intentionally skipped.
func ComposeRender(ctx context.Context, req ComposeRenderRequest) (ComposeRenderResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// Preserve the established per-channel stretch and byte conversion exactly
	// when callers request the legacy/off path through loaded images.
	if len(req.Images) >= 3 && renderLegacyPath(req) {
		var buf []byte
		var w, h int
		var stats [3]histogram.Stats
		if len(req.Overlays) > 0 {
			buf, w, h, stats = ComposeRGBWithOverlays(ctx, req.Images, req.Overlays)
		} else {
			buf, w, h, stats = ComposeRGB(ctx, req.Images)
		}
		if ctx.Err() != nil {
			return ComposeRenderResult{Status: models.CalibrationCancelled}, ctx.Err()
		}
		r, g, b, _, _ := ComposeRGBFloat32(req.Images)
		if len(req.Overlays) > 0 && len(req.Images) > 1 {
			for _, ov := range req.Overlays {
				applyOverlayFloat(ctx, &r, &g, &b, ov, req.Images[1])
			}
		}
		legacyStatuses := make([]models.CalibrationStatus, len(req.Overlays))
		legacyDiagnostics := make([]models.CalibrationDiagnostics, len(req.Overlays))
		for i := range legacyStatuses {
			legacyStatuses[i] = models.CalibrationDisabled
		}
		return ComposeRenderResult{R: r, G: g, B: b, Preview: buf, Width: w, Height: h, Stats: stats, Status: models.CalibrationDisabled, OverlayStatus: legacyStatuses, OverlayDiagnostics: legacyDiagnostics, RenderFingerprint: OverlayRenderFingerprint(req.Overlays, nil)}, nil
	}
	planes, w, h, err := composeAlignedPlanes(ctx, req)
	if err != nil {
		return ComposeRenderResult{}, err
	}
	status := models.CalibrationDisabled
	var transforms [3]models.LinearTransform
	for i := range transforms {
		transforms[i] = models.LinearTransform{Gain: 1}
	}
	if req.Result != nil {
		status = req.Result.Status
		if status == models.CalibrationValid {
			transforms = req.Result.Base
		}
	} else if req.Calibration != nil {
		status = req.Calibration.Effective().Status
		if status == models.CalibrationValid {
			transforms = req.Calibration.BaseTransforms
		}
	}
	if status != models.CalibrationValid {
		status = effectiveRenderStatus(status)
	}
	for c := range planes {
		applyRenderTransform(&planes[c], transforms[c], status == models.CalibrationValid)
	}
	overlayStates := []models.OverlayCalibrationState(nil)
	if req.Calibration != nil {
		overlayStates = req.Calibration.Overlays
	}
	if req.Result != nil {
		overlayStates = req.Result.Overlays
	}
	// Calibrated-linear overlays are accumulated before the shared stretch.
	// Artistic overlays remain on the post-stretch legacy blend path, even when
	// the base image itself is calibrated.
	var artisticOverlays []OverlayLayer
	overlayStatuses := make([]models.CalibrationStatus, len(req.Overlays))
	overlayDiagnostics := make([]models.CalibrationDiagnostics, len(req.Overlays))
	if status == models.CalibrationValid && len(req.Overlays) > 0 {
		for i, ov := range req.Overlays {
			if i >= len(overlayStates) || req.Overlays[i].Image == nil {
				overlayStatuses[i] = models.CalibrationDisabled
				if ov.Image != nil {
					artisticOverlays = append(artisticOverlays, ov)
				}
				continue
			}
			state := overlayStates[i]
			overlayStatuses[i], overlayDiagnostics[i] = state.Status, state.Diagnostics
			if overlayStatuses[i] == "" {
				overlayStatuses[i] = models.CalibrationDisabled
			}
			if state.Mode != models.OverlayCalibratedLinear {
				artisticOverlays = append(artisticOverlays, ov)
				continue
			}
			// Unsupported or incomplete opt-in metadata is explicit in the
			// persisted layer status. Fall back deterministically to the artistic
			// path so a bad optional layer cannot erase the base render.
			if state.Status != models.CalibrationValid || !state.Transform.Valid() {
				artisticOverlays = append(artisticOverlays, ov)
				continue
			}
			if len(req.Images) < 2 || req.Images[1] == nil {
				if ov.Image.HDU.Data.Width != planes[0].Width || ov.Image.HDU.Data.Height != planes[0].Height {
					return ComposeRenderResult{Status: models.CalibrationUnsupported}, fmt.Errorf("calibrated overlay requires image reference metadata")
				}
				applyCalibratedOverlayPixels(ctx, planes[:], ov.Image.HDU.Data.Pixels, alignedFiniteMask(ov.Image.HDU.Data.Pixels), state, ov.Settings)
				continue
			}
			ref := req.Images[1]
			raw := ImageDataForReferenceGridCtx(ctx, ov.Image, ref)
			valid := alignedOverlayMask(raw.Pixels, raw.Width, raw.Height, ov.Image, ref)
			applyCalibratedOverlayPixels(ctx, planes[:], raw.Pixels, valid, state, ov.Settings)
		}
	} else if len(req.Overlays) > 0 {
		// A non-valid base result cannot apply calibrated transforms. Artistic
		// overlays still retain their established behavior.
		for _, ov := range req.Overlays {
			artisticOverlays = append(artisticOverlays, ov)
		}
	}
	if ctx.Err() != nil {
		return ComposeRenderResult{Status: models.CalibrationCancelled}, ctx.Err()
	}
	// A single reference stretch is applied to all channels, preserving color.
	ref := models.LoadedImage{}
	if len(req.Images) > 1 && req.Images[1] != nil {
		ref = *req.Images[1]
	}
	for c := range planes {
		planes[c].Pixels = stretchShared(planes[c].Pixels, planes[c].Valid, &ref)
	}
	for _, ov := range artisticOverlays {
		applyOverlayFloat(ctx, &planes[0].Pixels, &planes[1].Pixels, &planes[2].Pixels, ov, &ref)
	}
	preview := render.ComposeRGB(planes[0].Pixels, planes[1].Pixels, planes[2].Pixels, w, h, stretch.Linear, stretch.Linear, stretch.Linear)
	return ComposeRenderResult{R: planes[0].Pixels, G: planes[1].Pixels, B: planes[2].Pixels, Preview: preview, Width: w, Height: h, Stats: HistogramRGB(preview), Status: status, OverlayStatus: overlayStatuses, OverlayDiagnostics: overlayDiagnostics, RenderFingerprint: OverlayRenderFingerprint(req.Overlays, overlayStates)}, nil
}

// OverlayRenderFingerprint hashes the ordered overlay content and behavior.
// Pixel bits and explicit fields are serialized in order, never via map
// iteration, so reorder/source/tint/strength changes invalidate rendering.
func OverlayRenderFingerprint(overlays []OverlayLayer, states []models.OverlayCalibrationState) string {
	var h canonicalHash
	h.stringValue("overlay-render-v1")
	for i, ov := range overlays {
		h.h.b.WriteString(fmt.Sprintf("index:%d;", i))
		if ov.Image == nil {
			h.stringValue("nil-image")
		} else {
			h.stringValue(ov.Image.Path)
			h.h.b.WriteString(fmt.Sprintf("dims:%d:%d;", ov.Image.HDU.Data.Width, ov.Image.HDU.Data.Height))
			for _, p := range ov.Image.HDU.Data.Pixels {
				var b [4]byte
				binary.LittleEndian.PutUint32(b[:], math.Float32bits(p))
				h.bytesValue(b[:])
			}
		}
		h.jsonValue(ov.Settings)
		if i < len(states) {
			h.jsonValue(states[i])
		}
	}
	return h.sum()
}

func applyCalibratedOverlayPixels(ctx context.Context, planes []AlignedPlane, pixels []float32, valid []bool, state models.OverlayCalibrationState, settings models.OrangeLayerState) {
	if len(planes) < 3 || !state.Transform.Valid() {
		return
	}
	strength := state.Strength
	tints := [3]float64{float64(settings.ColorR) / 255, float64(settings.ColorG) / 255, float64(settings.ColorB) / 255}
	for i, v := range pixels {
		if ctx.Err() != nil {
			return
		}
		if i >= len(planes[0].Pixels) || i >= len(planes[1].Pixels) || i >= len(planes[2].Pixels) ||
			i >= len(planes[0].Valid) || i >= len(planes[1].Valid) || i >= len(planes[2].Valid) ||
			!planes[0].Valid[i] || !planes[1].Valid[i] || !planes[2].Valid[i] ||
			i >= len(valid) || !valid[i] || !finite(float64(v)) {
			continue
		}
		offset := state.Transform.Offset
		if !state.NeutralizeBackground {
			offset = 0
		}
		x := (float64(v) - offset) * state.Transform.Gain * strength
		for c := range planes[:3] {
			planes[c].Pixels[i] += float32(x * tints[c])
		}
	}
}

func alignedOverlayMask(pixels []float32, width, height int, overlay, ref *models.LoadedImage) []bool {
	valid := alignedFiniteMask(pixels)
	if overlay == nil || ref == nil || overlay == ref || sharedDrizzleGrid(overlay, ref) {
		return valid
	}
	if tr, err := ComputeWCSTransform(overlay.HDU.Header, ref.HDU.Header); err == nil {
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				sx, sy := ApplyAffineTransform(tr, float64(x), float64(y))
				if !finite(sx) || !finite(sy) || sx < 0 || sy < 0 || sx > float64(overlay.HDU.Data.Width-1) || sy > float64(overlay.HDU.Data.Height-1) {
					valid[y*width+x] = false
				}
			}
		}
	}
	return valid
}

func applyOverlayFloat(ctx context.Context, r, g, b *[]float32, ov OverlayLayer, ref *models.LoadedImage) {
	if ov.Image == nil || ref == nil || ov.Settings.Opacity <= 0 {
		return
	}
	data := stretchForReferenceGrid(ctx, ov.Image, ref)
	opacity := math.Max(0, math.Min(1, ov.Settings.Opacity))
	k := math.Max(0, math.Min(1, ov.Settings.HighlightProtect))
	tints := [3]float64{float64(ov.Settings.ColorR) / 255, float64(ov.Settings.ColorG) / 255, float64(ov.Settings.ColorB) / 255}
	channels := [3]*[]float32{r, g, b}
	for i, v := range data.Pixels {
		if i >= len(*r) || i >= len(*g) || i >= len(*b) {
			break
		}
		strength := math.Max(0, math.Min(1, float64(v))) * opacity
		for c := range channels {
			base := float64((*channels[c])[i])
			layer := strength * tints[c]
			(*channels[c])[i] = float32(math.Max(0, math.Min(1, base+layer-k*base*layer)))
		}
	}
}

func renderLegacyPath(req ComposeRenderRequest) bool {
	if req.Result != nil {
		return req.Result.Status == models.CalibrationDisabled
	}
	return req.Calibration == nil || req.Calibration.Effective().Status == models.CalibrationDisabled
}

func effectiveRenderStatus(status models.CalibrationStatus) models.CalibrationStatus {
	if status == "" {
		return models.CalibrationDisabled
	}
	return status
}

func composeAlignedPlanes(ctx context.Context, req ComposeRenderRequest) ([3]AlignedPlane, int, int, error) {
	var out [3]AlignedPlane
	if req.Planes[0].Width > 0 && req.Planes[0].Height > 0 {
		w, h := req.Planes[0].Width, req.Planes[0].Height
		for i := range out {
			if req.Planes[i].Width != w || req.Planes[i].Height != h || w <= 0 || h <= 0 {
				return out, 0, 0, fmt.Errorf("aligned plane %d dimensions do not match reference", i)
			}
			if len(req.Planes[i].Pixels) != w*h {
				return out, 0, 0, fmt.Errorf("aligned plane %d pixel length %d, want %d", i, len(req.Planes[i].Pixels), w*h)
			}
			if len(req.Planes[i].Valid) != w*h {
				return out, 0, 0, fmt.Errorf("aligned plane %d validity length %d, want %d", i, len(req.Planes[i].Valid), w*h)
			}
			out[i] = cloneAlignedPlane(req.Planes[i])
		}
		return out, out[0].Width, out[0].Height, nil
	}
	if len(req.Images) < 3 || req.Images[0] == nil || req.Images[1] == nil || req.Images[2] == nil {
		return out, 0, 0, nil
	}
	ref := req.Images[1]
	for i, img := range []*models.LoadedImage{req.Images[2], req.Images[1], req.Images[0]} {
		if ctx.Err() != nil {
			return out, 0, 0, ctx.Err()
		}
		raw := ImageDataForReferenceGridCtx(ctx, img, ref)
		if ctx.Err() != nil {
			return out, 0, 0, ctx.Err()
		}
		valid := alignedFiniteMask(raw.Pixels)
		// WCS and resize warps use zero for out-of-footprint samples. Recompute
		// their source-coordinate footprint so those zeros remain invalid.
		if img != ref && !sharedDrizzleGrid(img, ref) {
			if tr, e := ComputeWCSTransform(img.HDU.Header, ref.HDU.Header); e == nil {
				for y := 0; y < raw.Height; y++ {
					for x := 0; x < raw.Width; x++ {
						sx, sy := ApplyAffineTransform(tr, float64(x), float64(y))
						if !finite(sx) || !finite(sy) || sx < 0 || sy < 0 || sx > float64(img.HDU.Data.Width-1) || sy > float64(img.HDU.Data.Height-1) {
							valid[y*raw.Width+x] = false
						}
					}
				}
			} else if img.HDU.Data.Width != ref.HDU.Data.Width || img.HDU.Data.Height != ref.HDU.Data.Height {
				for y := 0; y < raw.Height; y++ {
					for x := 0; x < raw.Width; x++ {
						sx := (float64(x)+0.5)*float64(img.HDU.Data.Width)/float64(raw.Width) - 0.5
						sy := (float64(y)+0.5)*float64(img.HDU.Data.Height)/float64(raw.Height) - 0.5
						if sx < 0 || sy < 0 || sx >= float64(img.HDU.Data.Width) || sy >= float64(img.HDU.Data.Height) {
							valid[y*raw.Width+x] = false
						}
					}
				}
			}
		}
		out[i] = AlignedPlane{Pixels: raw.Pixels, Valid: valid, Width: raw.Width, Height: raw.Height}
	}
	return out, ref.HDU.Data.Width, ref.HDU.Data.Height, nil
}

func alignedFiniteMask(p []float32) []bool {
	valid := make([]bool, len(p))
	for i, v := range p {
		valid[i] = finite(float64(v))
	}
	return valid
}

func cloneAlignedPlane(p AlignedPlane) AlignedPlane {
	return AlignedPlane{Pixels: append([]float32(nil), p.Pixels...), Valid: append([]bool(nil), p.Valid...), Width: p.Width, Height: p.Height}
}

func applyRenderTransform(p *AlignedPlane, t models.LinearTransform, enabled bool) {
	if !enabled || !t.Valid() {
		return
	}
	for i, v := range p.Pixels {
		if i >= len(p.Valid) || !p.Valid[i] || math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			continue
		}
		p.Pixels[i] = float32((float64(v) - t.Offset) * t.Gain)
	}
}

func stretchShared(p []float32, valid []bool, ref *models.LoadedImage) []float32 {
	out := make([]float32, len(p))
	background, peak, scaled := ref.Background, ref.Peak, ref.ScaledPeak
	if !finite(background) {
		background = 0
	}
	if !finite(peak) || peak <= background {
		peak = background + 1
	}
	if !finite(scaled) || scaled <= 0 {
		scaled = 100
	}
	denom := peak - background
	for i, raw := range p {
		if i >= len(valid) || !valid[i] || math.IsNaN(float64(raw)) || math.IsInf(float64(raw), 0) {
			continue
		}
		v := (float64(raw) - background) * scaled / denom
		if v < 0 {
			v = 0
		}
		if v > scaled {
			v = scaled
		}
		out[i] = float32(v / scaled)
	}
	return out
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
