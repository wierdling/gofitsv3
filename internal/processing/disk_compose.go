package processing

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/histogram"
	"gofitsv3/internal/models"
	"gofitsv3/internal/stretch"
)

// diskComposeRename is isolated for deterministic transaction-failure tests.
// Production uses os.Rename unchanged.
var diskComposeRename = os.Rename

// DiskChannel is the small immutable description consumed by DiskCompose. The
// raster remains in ArtifactPath; Image contains only render metadata.
type DiskChannel struct {
	ArtifactPath string
	Image        models.LoadedImage
	// Manual offsets are kept separate from LoadedImage for disk callers that
	// cannot retain the UI's ChannelState. They are applied in output→source
	// coordinates after the fitted affine.
	OffsetX, OffsetY float64
	OffsetRot        float64
}

type DiskOverlay struct {
	Channel  DiskChannel
	Settings models.OrangeLayerState
}

// DiskComposeRequest describes a disk-backed RGB render. Output paths are
// replaced atomically and are never retained by processing.
type DiskComposeRequest struct {
	Channels    [3]DiskChannel // B, G, R, matching Compose's channel ordering
	Overlays    []DiskOverlay
	Calibration *models.ColorCalibrationState
	Output      [3]string // R, G, B artifact paths
	PreviewMax  int
	RGBLevels   *models.RgbLevels
}

type DiskComposeResult struct {
	Preview                     []byte
	PreviewWidth, PreviewHeight int
	Width, Height               int
	Stats                       [3]histogram.Stats
	Status                      models.CalibrationStatus
}

var diskComposeSem = make(chan struct{}, 1)
var diskCompositePreviewForCompose = diskCompositePreview

// ComposeDisk streams channels and overlays without creating a full RGB image
// or retaining source pixels. A source is opened only for its preparation
// pass; the final combine uses bounded rows from the prepared artifacts.
func ComposeDisk(ctx context.Context, req DiskComposeRequest) (DiskComposeResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case diskComposeSem <- struct{}{}:
	case <-ctx.Done():
		return DiskComposeResult{}, ctx.Err()
	}
	defer func() { <-diskComposeSem }()
	if req.PreviewMax <= 0 {
		req.PreviewMax = 1600
	}
	ref := req.Channels[1]
	if ref.ArtifactPath == "" || ref.Image.HDU.Data.Width <= 0 || ref.Image.HDU.Data.Height <= 0 {
		return DiskComposeResult{}, errors.New("disk Compose requires a reference channel")
	}
	w, h := ref.Image.HDU.Data.Width, ref.Image.HDU.Data.Height
	if w <= 0 || h <= 0 {
		return DiskComposeResult{}, errors.New("invalid reference dimensions")
	}
	for i := range req.Output {
		if req.Output[i] == "" {
			return DiskComposeResult{}, fmt.Errorf("missing output path %d", i)
		}
		for j := 0; j < i; j++ {
			if sameDiskPath(req.Output[i], req.Output[j]) {
				return DiskComposeResult{}, errors.New("disk Compose output paths must be distinct")
			}
		}
		for _, ch := range req.Channels {
			if sameDiskPath(req.Output[i], ch.ArtifactPath) {
				return DiskComposeResult{}, errors.New("disk Compose output overlaps an input artifact")
			}
		}
		for _, ov := range req.Overlays {
			if sameDiskPath(req.Output[i], ov.Channel.ArtifactPath) {
				return DiskComposeResult{}, errors.New("disk Compose output overlaps an overlay artifact")
			}
		}
	}
	prepared := [3]string{}
	stretchMeta := req.Channels[1].Image
	// Every intermediate is staging-owned and must disappear on all failure
	// paths, including cancellation during preparation/calibration.
	var intermediates []string
	defer func() {
		for _, p := range intermediates {
			if p != "" {
				_ = os.Remove(p)
			}
		}
	}()
	type artisticDiskOverlay struct {
		path     string
		settings models.OrangeLayerState
		meta     models.LoadedImage
	}
	var artistic []artisticDiskOverlay
	for i := range req.Channels {
		if err := ctx.Err(); err != nil {
			return DiskComposeResult{}, err
		}
		if req.Channels[i].ArtifactPath == "" {
			return DiskComposeResult{}, fmt.Errorf("missing channel %d artifact", i)
		}
		prepared[i] = req.Output[i] + fmt.Sprintf(".prepared-%d", i)
		if req.Output[i] == "" {
			prepared[i] = req.Channels[i].ArtifactPath + fmt.Sprintf(".prepared-%d", i)
		}
		intermediates = append(intermediates, prepared[i])
		transform := models.LinearTransform{Gain: 1}
		applyTransform := req.Calibration != nil && req.Calibration.Effective().Status == models.CalibrationValid
		if applyTransform {
			// DiskCompose channels are supplied in Compose's historical B,G,R
			// order, while calibration transforms are persisted R,G,B.
			transform = req.Calibration.BaseTransforms[[3]int{2, 1, 0}[i]]
		}
		// Keep the base channels in calibrated linear space until all
		// calibrated-linear overlays have been accumulated. The shared Channel 2
		// stretch is applied in a separate pass below.
		if err := prepareDiskChannel(ctx, req.Channels[i], req.Channels[1].Image, stretchMeta, w, h, prepared[i], applyTransform, transform, false); err != nil {
			for _, p := range prepared {
				if p != "" {
					_ = os.Remove(p)
				}
			}
			return DiskComposeResult{}, err
		}
	}
	for oi, ov := range req.Overlays {
		if err := ctx.Err(); err != nil {
			return DiskComposeResult{}, err
		}
		if ov.Channel.ArtifactPath == "" {
			continue
		}
		p := ov.Channel.ArtifactPath + fmt.Sprintf(".overlay-prepared-%d", oi)
		intermediates = append(intermediates, p)
		cal := overlayCalibration(req.Calibration, oi)
		applyCal := cal != nil && cal.Mode == models.OverlayCalibratedLinear && cal.Status == models.CalibrationValid && cal.Transform.Valid()
		// Both overlay kinds are prepared in linear space. Calibrated overlays
		// are accumulated before the shared stretch; artistic overlays are held
		// until after that stretch.
		if err := prepareDiskChannel(ctx, ov.Channel, req.Channels[1].Image, stretchMeta, w, h, p, false, models.LinearTransform{Gain: 1}, false); err != nil {
			return DiskComposeResult{}, err
		}
		// Blend overlays directly into prepared base artifacts, one row at a time.
		if applyCal {
			if err := blendCalibratedDiskOverlay(ctx, prepared, p, ov.Settings, *cal, w, h); err != nil {
				_ = os.Remove(p)
				return DiskComposeResult{}, err
			}
		} else {
			artistic = append(artistic, artisticDiskOverlay{path: p, settings: ov.Settings, meta: ov.Channel.Image})
		}
		if applyCal {
			_ = os.Remove(p)
		}
	}
	var cdf []float32
	if stretchMeta.Mode == stretch.HistEq {
		var histErr error
		cdf, histErr = diskHistEqCDF(ctx, prepared[1], stretchMeta)
		if histErr != nil {
			return DiskComposeResult{}, histErr
		}
	}
	for _, p := range prepared {
		if err := stretchDiskArtifact(ctx, p, stretchMeta, cdf); err != nil {
			return DiskComposeResult{}, err
		}
	}
	for _, ov := range artistic {
		var overlayCDF []float32
		if ov.meta.Mode == stretch.HistEq {
			var histErr error
			overlayCDF, histErr = diskHistEqCDF(ctx, ov.path, ov.meta)
			if histErr != nil {
				return DiskComposeResult{}, histErr
			}
		}
		if err := stretchDiskArtifact(ctx, ov.path, ov.meta, overlayCDF); err != nil {
			return DiskComposeResult{}, err
		}
		if err := blendDiskOverlay(ctx, prepared, ov.path, ov.settings, w, h); err != nil {
			return DiskComposeResult{}, err
		}
		_ = os.Remove(ov.path)
	}
	// Keep the existing destinations untouched until the complete render,
	// including preview generation, has succeeded. The first combine publishes
	// only to staging paths; the second combine performs the final atomic swap.
	staged := [3]string{}
	for i := range staged {
		staged[i] = req.Output[i] + fmt.Sprintf(".compose-staged-%d", i)
		_ = os.Remove(staged[i])
		intermediates = append(intermediates, staged[i])
	}
	if err := combineDiskChannels(ctx, prepared, staged, w, h); err != nil {
		return DiskComposeResult{}, err
	}
	preview, stats, err := diskCompositePreviewForCompose(ctx, staged, w, h, req.PreviewMax, req.RGBLevels)
	if err != nil {
		return DiskComposeResult{}, err
	}
	// combineDiskChannels consumes historical B,G,R source ordering. Staged
	// artifacts are already in output R,G,B order, so reverse the source tuple
	// for this final transactional copy.
	if err := combineDiskChannels(ctx, [3]string{staged[2], staged[1], staged[0]}, req.Output, w, h); err != nil {
		return DiskComposeResult{}, err
	}
	status := models.CalibrationDisabled
	if req.Calibration != nil {
		status = req.Calibration.Effective().Status
	}
	step := 1
	if w > req.PreviewMax || h > req.PreviewMax {
		if w > h {
			step = (w + req.PreviewMax - 1) / req.PreviewMax
		} else {
			step = (h + req.PreviewMax - 1) / req.PreviewMax
		}
	}
	return DiskComposeResult{Preview: preview, PreviewWidth: (w + step - 1) / step, PreviewHeight: (h + step - 1) / step, Width: w, Height: h, Stats: stats, Status: status}, nil
}

func sameDiskPath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	aa, errA := filepath.Abs(filepath.Clean(a))
	bb, errB := filepath.Abs(filepath.Clean(b))
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return strings.EqualFold(filepath.Clean(aa), filepath.Clean(bb))
}

func prepareDiskChannel(ctx context.Context, src DiskChannel, ref, stretchMeta models.LoadedImage, dw, dh int, dst string, applyTransform bool, transform models.LinearTransform, applyStretch bool) error {
	a, err := fitsio.OpenFloat32ArtifactReadOnly(src.ArtifactPath)
	if err != nil {
		return err
	}
	defer a.Close()
	tx, err := fitsio.BeginFloat32ArtifactTransaction(dst, dw, dh)
	if err != nil {
		return err
	}
	out, row := tx.Artifact(), make([]float32, dw)
	sampler := newArtifactSampler(a)
	hist := make([]int, 256)
	for y := 0; y < dh; y++ {
		if err := ctx.Err(); err != nil {
			_ = tx.Abort()
			return err
		}
		for x := 0; x < dw; x++ {
			fx, fy := mapDiskCoordinate(src.Image, ref, src.OffsetX, src.OffsetY, src.OffsetRot, x, y, dw, dh, a.Width, a.Height)
			row[x] = sampler.sample(fx, fy)
			v := row[x]
			if applyTransform {
				v = float32((float64(v) - transform.Offset) * transform.Gain)
			}
			if applyStretch {
				v = stretchDiskValue(v, stretchMeta)
			}
			row[x] = v
		}
		if err := out.WriteRow(y, row); err != nil {
			_ = tx.Abort()
			return err
		}
		if applyStretch && stretchMeta.Mode == stretch.HistEq {
			for _, v := range row {
				idx := int(clamp01(float64(v)) * 255)
				hist[idx]++
			}
		}
	}
	if stretchMeta.Mode == stretch.HistEq {
		cdf := make([]float32, 256)
		total := 0
		for _, n := range hist {
			total += n
		}
		sum := 0
		for i, n := range hist {
			sum += n
			if total > 0 {
				cdf[i] = float32(float64(sum) / float64(total))
			}
		}
		for y := 0; y < dh; y++ {
			if err := ctx.Err(); err != nil {
				_ = tx.Abort()
				return err
			}
			if err := out.ReadRow(y, row); err != nil {
				_ = tx.Abort()
				return err
			}
			for i, v := range row {
				row[i] = cdf[int(clamp01(float64(v))*255)]
			}
			if err := out.WriteRow(y, row); err != nil {
				_ = tx.Abort()
				return err
			}
		}
	}
	return tx.Commit()
}

// stretchDiskArtifact applies the shared reference stretch in place after
// calibrated-linear overlays have been accumulated. It intentionally uses a
// bounded row buffer and never materializes the artifact.
func stretchDiskArtifact(ctx context.Context, path string, meta models.LoadedImage, cdf []float32) error {
	a, err := fitsio.OpenFloat32Artifact(path)
	if err != nil {
		return err
	}
	defer a.Close()
	row := make([]float32, a.Width)
	for y := 0; y < a.Height; y++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := a.ReadRow(y, row); err != nil {
			return err
		}
		for i, v := range row {
			if len(cdf) == 256 {
				x := stretchNormalizedValue(v, meta)
				row[i] = cdf[int(clamp01(float64(x))*255)]
			} else {
				row[i] = stretchDiskValue(v, meta)
			}
		}
		if err := a.WriteRow(y, row); err != nil {
			return err
		}
	}
	return nil
}

func stretchNormalizedValue(v float32, img models.LoadedImage) float32 {
	if !finite(float64(v)) {
		return 0
	}
	bg, peak, scaled := img.Background, img.Peak, img.ScaledPeak
	if !finite(bg) {
		bg = 0
	}
	if !finite(peak) || peak <= bg {
		peak = bg + 1
	}
	if !finite(scaled) || scaled <= 0 {
		scaled = 100
	}
	x := (float64(v) - bg) * scaled / (peak - bg)
	return float32(clamp01(x / scaled))
}

func diskHistEqCDF(ctx context.Context, path string, meta models.LoadedImage) ([]float32, error) {
	a, err := fitsio.OpenFloat32ArtifactReadOnly(path)
	if err != nil {
		return nil, err
	}
	defer a.Close()
	hist := make([]int, 256)
	row := make([]float32, a.Width)
	for y := 0; y < a.Height; y++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := a.ReadRow(y, row); err != nil {
			return nil, err
		}
		for _, v := range row {
			hist[int(clamp01(float64(stretchNormalizedValue(v, meta)))*255)]++
		}
	}
	cdf := make([]float32, 256)
	total, sum := 0, 0
	for _, n := range hist {
		total += n
	}
	for i, n := range hist {
		sum += n
		if total > 0 {
			cdf[i] = float32(float64(sum) / float64(total))
		}
	}
	return cdf, nil
}

func mapDiskCoordinate(img, ref models.LoadedImage, offsetX, offsetY, offsetRot float64, x, y, dw, dh, sw, sh int) (float64, float64) {
	if img.HasAlignTransform {
		// Stored alignment transforms are backward (reference/output -> source)
		// mappings. Apply them directly in the reference grid; composing them
		// after resize/WCS mapping would interpret the affine in the wrong frame.
		fx := img.AlignA*float64(x) + img.AlignB*float64(y) + img.AlignC
		fy := img.AlignD*float64(x) + img.AlignE*float64(y) + img.AlignF
		fx -= offsetX
		fy -= offsetY
		if offsetRot != 0 {
			cx, cy := float64(sw)/2, float64(sh)/2
			rad := -offsetRot * math.Pi / 180
			xc, yc := fx-cx, fy-cy
			fx, fy = math.Cos(rad)*xc-math.Sin(rad)*yc+cx, math.Sin(rad)*xc+math.Cos(rad)*yc+cy
		}
		return fx, fy
	}
	fx := (float64(x)+0.5)*float64(sw)/float64(dw) - 0.5
	fy := (float64(y)+0.5)*float64(sh)/float64(dh) - 0.5
	if img.Rotation90 == 0 && ref.Rotation90 == 0 && !sharedDrizzleGrid(&img, &ref) {
		if tr, err := ComputeWCSTransform(img.HDU.Header, ref.HDU.Header); err == nil {
			fx, fy = ApplyAffineTransform(tr, float64(x), float64(y))
		}
	}
	fx -= offsetX
	fy -= offsetY
	if offsetRot != 0 {
		cx, cy := float64(sw)/2, float64(sh)/2
		rad := -offsetRot * math.Pi / 180
		xc, yc := fx-cx, fy-cy
		fx, fy = math.Cos(rad)*xc-math.Sin(rad)*yc+cx, math.Sin(rad)*xc+math.Cos(rad)*yc+cy
	}
	// Rotation90 is physical artifact state. RotateArtifact90CW rewrites the
	// raster and dimensions, so sampling must not apply the quarter-turn again.
	return fx, fy
}

// MapDiskCoordinate exposes the compositor's output-to-source mapping to
// bounded consumers (for example Gaia aperture readers). Keeping this as the
// single mapping implementation prevents calibration from sampling a raw,
// unaligned artifact while rendering samples the fitted/offset grid.
func MapDiskCoordinate(img, ref models.LoadedImage, offsetX, offsetY, offsetRot float64, x, y, dw, dh, sw, sh int) (float64, float64) {
	return mapDiskCoordinate(img, ref, offsetX, offsetY, offsetRot, x, y, dw, dh, sw, sh)
}

type artifactSampler struct {
	a      *fitsio.Float32Artifact
	y0, y1 int
	r0, r1 []float32
}

func newArtifactSampler(a *fitsio.Float32Artifact) *artifactSampler {
	return &artifactSampler{a: a, y0: -1, y1: -1, r0: make([]float32, a.Width), r1: make([]float32, a.Width)}
}
func (s *artifactSampler) sample(x, y float64) float32 {
	a := s.a
	if a == nil || a.Width <= 0 || a.Height <= 0 || x < 0 || y < 0 || x > float64(a.Width-1) || y > float64(a.Height-1) {
		return 0
	}
	x0, y0 := int(math.Floor(x)), int(math.Floor(y))
	wx, wy := x-float64(x0), y-float64(y0)
	x1, y1 := x0+1, y0+1
	if x1 >= a.Width {
		x1 = x0
		wx = 0
	}
	if y1 >= a.Height {
		y1 = y0
		wy = 0
	}
	if s.y0 != y0 {
		if a.ReadRow(y0, s.r0) != nil {
			return 0
		}
		s.y0 = y0
	}
	if s.y1 != y1 {
		if a.ReadRow(y1, s.r1) != nil {
			return 0
		}
		s.y1 = y1
	}
	r0, r1 := s.r0, s.r1
	v00, v10, v01, v11 := r0[x0], r0[x1], r1[x0], r1[x1]
	if !finite(float64(v00)) || !finite(float64(v10)) || !finite(float64(v01)) || !finite(float64(v11)) {
		return 0
	}
	return float32(float64(v00)*(1-wx)*(1-wy) + float64(v10)*wx*(1-wy) + float64(v01)*(1-wx)*wy + float64(v11)*wx*wy)
}

func stretchDiskValue(v float32, img models.LoadedImage) float32 {
	if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
		return 0
	}
	bg, peak, scaled := img.Background, img.Peak, img.ScaledPeak
	if math.IsNaN(bg) || math.IsInf(bg, 0) {
		bg = 0
	}
	if math.IsNaN(peak) || math.IsInf(peak, 0) || peak <= bg {
		peak = bg + 1
	}
	if scaled <= 0 || math.IsNaN(scaled) || math.IsInf(scaled, 0) {
		scaled = 100
	}
	x := (float64(v) - bg) * scaled / (peak - bg)
	if x < 0 {
		x = 0
	}
	var out float64
	switch img.Mode {
	case stretch.HistEq:
		// Histogram equalisation is applied in a streaming second pass by
		// callers that need exact CDF parity; this fallback preserves the
		// normalized value for one-pass consumers.
		out = x / scaled
	case stretch.Log:
		out = math.Log1p(x) / math.Log1p(scaled)
	case stretch.Asinh:
		b := img.AsinhScale
		if b <= 0 {
			b = stretch.DefaultAsinhScale
		}
		out = math.Asinh(x/b) / math.Asinh(scaled/b)
	case stretch.Sqrt:
		out = math.Sqrt(x) / math.Sqrt(scaled)
	case stretch.MTF:
		m := img.MTFMidtone
		if m <= 0 || m >= 1 {
			m = stretch.DefaultMTFMidtone
		}
		out = stretch.Mtf(m, x/scaled)
	case stretch.GHS:
		d := img.GHSStretch
		if d <= 0 {
			d = stretch.DefaultGHSStretch
		}
		sp := img.GHSSymmetry
		if sp <= 0 || sp >= 1 {
			sp = stretch.DefaultGHSSymmetry
		}
		out = stretch.NewGHS(d, img.GHSLocal, sp, 0, 1).Eval(x / scaled)
	default:
		out = x / scaled
	}
	if out < 0 || math.IsNaN(out) || math.IsInf(out, 0) {
		out = 0
	}
	if out > 1 {
		out = 1
	}
	return float32(out)
}

// DiskStretchPreviewValue applies the same bounded scalar stretch used by the
// disk compositor; it intentionally allocates nothing and is safe for UI
// preview sampling.
func DiskStretchPreviewValue(v float32, img models.LoadedImage) float32 {
	return stretchDiskValue(v, img)
}

func overlayCalibration(state *models.ColorCalibrationState, index int) *models.OverlayCalibrationState {
	if state == nil || index < 0 || index >= len(state.Overlays) {
		return nil
	}
	return &state.Overlays[index]
}
func overlayTransform(state *models.OverlayCalibrationState) models.LinearTransform {
	if state == nil {
		return models.LinearTransform{Gain: 1}
	}
	return state.Transform
}

func blendCalibratedDiskOverlay(ctx context.Context, bases [3]string, overlay string, settings models.OrangeLayerState, state models.OverlayCalibrationState, w, h int) error {
	if state.Strength == 0 || !state.Transform.Valid() {
		return nil
	}
	o, err := fitsio.OpenFloat32ArtifactReadOnly(overlay)
	if err != nil {
		return err
	}
	defer o.Close()
	rows := [3]*fitsio.Float32Artifact{}
	for i, p := range bases {
		rows[i], err = fitsio.OpenFloat32Artifact(p)
		if err != nil {
			return err
		}
		defer rows[i].Close()
	}
	base, ov := make([]float32, w), make([]float32, w)
	tints := [3]float64{float64(settings.ColorR) / 255, float64(settings.ColorG) / 255, float64(settings.ColorB) / 255}
	offset := state.Transform.Offset
	if !state.NeutralizeBackground {
		offset = 0
	}
	for y := 0; y < h; y++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := o.ReadRow(y, ov); err != nil {
			return err
		}
		for c := 0; c < 3; c++ {
			bi := [3]int{2, 1, 0}[c]
			if err := rows[bi].ReadRow(y, base); err != nil {
				return err
			}
			for x, v := range ov {
				if !finite(float64(v)) {
					continue
				}
				base[x] += float32((float64(v) - offset) * state.Transform.Gain * state.Strength * tints[c])
			}
			if err := rows[bi].WriteRow(y, base); err != nil {
				return err
			}
		}
	}
	return nil
}

func blendDiskOverlay(ctx context.Context, bases [3]string, overlay string, s models.OrangeLayerState, w, h int) error {
	return blendDiskOverlayScaled(ctx, bases, overlay, s, 1, w, h)
}

func blendDiskOverlayScaled(ctx context.Context, bases [3]string, overlay string, s models.OrangeLayerState, strength float64, w, h int) error {
	o, err := fitsio.OpenFloat32ArtifactReadOnly(overlay)
	if err != nil {
		return err
	}
	defer o.Close()
	rows := make([]*fitsio.Float32Artifact, 3)
	for i, p := range bases {
		rows[i], err = fitsio.OpenFloat32Artifact(p)
		if err != nil {
			return err
		}
		defer rows[i].Close()
	}
	row, ov := make([]float32, w), make([]float32, w)
	opacity := math.Max(0, math.Min(1, s.Opacity)) * strength
	k := math.Max(0, math.Min(1, s.HighlightProtect))
	tint := [3]float64{float64(s.ColorR) / 255, float64(s.ColorG) / 255, float64(s.ColorB) / 255}
	for y := 0; y < h; y++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := o.ReadRow(y, ov); err != nil {
			return err
		}
		for c := 0; c < 3; c++ {
			bi := [3]int{2, 1, 0}[c]
			if err := rows[bi].ReadRow(y, row); err != nil {
				return err
			}
			for x, v := range ov {
				layer := math.Max(0, math.Min(1, float64(v))) * opacity * tint[c]
				base := float64(row[x])
				row[x] = float32(math.Max(0, math.Min(1, base+layer-k*base*layer)))
			}
			if err := rows[bi].WriteRow(y, row); err != nil {
				return err
			}
		}
	}
	return nil
}

func combineDiskChannels(ctx context.Context, src, dst [3]string, w, h int) error {
	tmps := [3]string{}
	txs := [3]*fitsio.Float32ArtifactTransaction{}
	backups := [3]string{}
	backedUp := 0
	published := 0
	committed := false
	defer func() {
		for _, p := range tmps {
			if p != "" {
				_ = os.Remove(p)
			}
		}
		if !committed {
			for i := published - 1; i >= 0; i-- {
				_ = os.Remove(dst[i])
				if backups[i] != "" {
					_ = diskComposeRename(backups[i], dst[i])
				}
			}
			for i := published; i < backedUp; i++ {
				if backups[i] != "" {
					_ = diskComposeRename(backups[i], dst[i])
				}
			}
		}
		for i := range backups {
			if committed && backups[i] != "" {
				_ = os.Remove(backups[i])
			}
		}
	}()
	for c := 0; c < 3; c++ {
		tmps[c] = dst[c] + fmt.Sprintf(".render-%d", os.Getpid())
		tx, e := fitsio.BeginFloat32ArtifactTransaction(tmps[c], w, h)
		if e != nil {
			return e
		}
		txs[c] = tx
		in, e := fitsio.OpenFloat32ArtifactReadOnly(src[[3]int{2, 1, 0}[c]])
		if e != nil {
			_ = tx.Abort()
			return e
		}
		row := make([]float32, w)
		for y := 0; y < h; y++ {
			if e = ctx.Err(); e != nil {
				_ = in.Close()
				for _, t := range txs {
					if t != nil {
						_ = t.Abort()
					}
				}
				return e
			}
			if e = in.ReadRow(y, row); e != nil {
				_ = tx.Abort()
				_ = in.Close()
				for _, t := range txs {
					if t != nil {
						_ = t.Abort()
					}
				}
				return e
			}
			if e = tx.Artifact().WriteRow(y, row); e != nil {
				_ = in.Close()
				for _, t := range txs {
					if t != nil {
						_ = t.Abort()
					}
				}
				return e
			}
		}
		_ = in.Close()
	}
	for c := range txs {
		if e := txs[c].Commit(); e != nil {
			for _, t := range txs {
				if t != nil {
					_ = t.Abort()
				}
			}
			return e
		}
	}
	// Replace the three destinations only after every source pass has
	// completed. A failed rename restores the originals from backups.
	for c := 0; c < 3; c++ {
		backups[c] = dst[c] + ".render-backup"
		_ = os.Remove(backups[c])
		if err := diskComposeRename(dst[c], backups[c]); err != nil && !os.IsNotExist(err) {
			return err
		}
		if _, err := os.Stat(backups[c]); err == nil {
			backedUp = c + 1
		}
	}
	for c := 0; c < 3; c++ {
		if err := diskComposeRename(tmps[c], dst[c]); err != nil {
			return err
		}
		published = c + 1
	}
	committed = true
	return nil
}

func diskCompositePreview(ctx context.Context, paths [3]string, w, h, maxEdge int, levels *models.RgbLevels) ([]byte, [3]histogram.Stats, error) {
	a := [3]*fitsio.Float32Artifact{}
	var err error
	for i, p := range paths {
		a[i], err = fitsio.OpenFloat32ArtifactReadOnly(p)
		if err != nil {
			return nil, [3]histogram.Stats{}, err
		}
		defer a[i].Close()
	}
	step := 1
	if w > maxEdge || h > maxEdge {
		if w > h {
			step = (w + maxEdge - 1) / maxEdge
		} else {
			step = (h + maxEdge - 1) / maxEdge
		}
	}
	pw, ph := (w+step-1)/step, (h+step-1)/step
	buf := make([]byte, pw*ph*4)
	rows := [3][]float32{make([]float32, w), make([]float32, w), make([]float32, w)}
	for y := 0; y < h; y++ {
		if err := ctx.Err(); err != nil {
			return nil, [3]histogram.Stats{}, err
		}
		for c := 0; c < 3; c++ {
			if err := a[c].ReadRow(y, rows[c]); err != nil {
				return nil, [3]histogram.Stats{}, err
			}
		}
		if y%step != 0 {
			continue
		}
		for x := 0; x < w; x += step {
			idx := ((y/step)*pw + x/step) * 4
			// Output artifacts are ordered R, G, B. Their samples are normalized
			// float32 values, whereas RGB levels are stored in the UI's 0–255
			// range. Keep this conversion aligned with Edit's disk preview.
			vals := [3]float64{float64(rows[0][x]), float64(rows[1][x]), float64(rows[2][x])}
			for c := 0; c < 3; c++ {
				if levels != nil {
					min := levels.Min[c] / 255
					max := levels.Max[c] / 255
					span := max - min
					if span > 0 {
						vals[c] = (vals[c] - min) / span
					}
				}
			}
			buf[idx] = byte(clamp01(vals[0]) * 255)
			buf[idx+1] = byte(clamp01(vals[1]) * 255)
			buf[idx+2] = byte(clamp01(vals[2]) * 255)
			buf[idx+3] = 255
		}
	}
	return buf, HistogramRGB(buf), nil
}

func clamp01(v float64) float64 {
	if v < 0 || math.IsNaN(v) {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
