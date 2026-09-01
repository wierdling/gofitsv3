package processing

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/histogram"
	"gofitsv3/internal/models"
	"gofitsv3/internal/stretch"
)

// diskComposeRename is isolated for deterministic transaction-failure tests.
// Production uses os.Rename unchanged.
var diskComposeRename = os.Rename
var diskComposeTxnID uint64

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
	Channels        [3]DiskChannel // B, G, R, matching Compose's channel ordering
	Overlays        []DiskOverlay
	Output          [3]string // R, G, B artifact paths
	PreviewMax      int
	RGBLevels       *models.RgbLevels
	CompositionMode models.ComposeMode
	MixWeights      []models.ComposeMixWeight
	LRGB            models.LRGBSettings
}

type DiskComposeResult struct {
	Preview                     []byte
	PreviewWidth, PreviewHeight int
	Width, Height               int
	Stats                       [3]histogram.Stats
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
	if req.LRGB.Enabled {
		return DiskComposeResult{}, errors.New("disk-backed Compose does not support LRGB; disable LRGB or use normal Compose")
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
	weightedSources := 3
	for _, ov := range req.Overlays {
		if ov.Channel.ArtifactPath != "" {
			weightedSources++
		}
	}
	if (models.ComposeProject{CompositionMode: req.CompositionMode}).ResolveComposeMode(weightedSources) == models.ComposeModeWeighted {
		return composeDiskWeighted(ctx, req, w, h)
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
		// Non-HistEq stretches are scalar and can be fused into the mapping
		// pass. HistEq intentionally remains a separate multi-pass operation
		// because its CDF is calculated from the complete prepared raster.
		applyStretch := stretchMeta.Mode != stretch.HistEq
		if err := prepareDiskChannel(ctx, req.Channels[i], req.Channels[1].Image, stretchMeta, w, h, prepared[i], applyStretch); err != nil {
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
		// Artistic overlays own their stretch metadata; preserve that
		// ownership while fusing only non-HistEq scalar modes.
		applyStretch := ov.Channel.Image.Mode != stretch.HistEq
		prepareMeta := ov.Channel.Image
		if !applyStretch {
			// HistEq is applied by the caller-owned CDF pass below. Keep the
			// preparation pass purely calibrated/mapped so prepareDiskChannel's
			// legacy internal HistEq pass does not consume the overlay.
			prepareMeta.Mode = stretch.Linear
		}
		if err := prepareDiskChannel(ctx, ov.Channel, req.Channels[1].Image, prepareMeta, w, h, p, applyStretch); err != nil {
			return DiskComposeResult{}, err
		}
		artistic = append(artistic, artisticDiskOverlay{path: p, settings: ov.Settings, meta: ov.Channel.Image})
	}
	var cdf []float32
	if stretchMeta.Mode == stretch.HistEq {
		var histErr error
		cdf, histErr = diskHistEqCDF(ctx, prepared[1], stretchMeta)
		if histErr != nil {
			return DiskComposeResult{}, histErr
		}
	}
	if stretchMeta.Mode == stretch.HistEq {
		for _, p := range prepared {
			if err := stretchDiskArtifact(ctx, p, stretchMeta, cdf); err != nil {
				return DiskComposeResult{}, err
			}
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
		if ov.meta.Mode == stretch.HistEq {
			if err := stretchDiskArtifact(ctx, ov.path, ov.meta, overlayCDF); err != nil {
				return DiskComposeResult{}, err
			}
		}
		if err := blendDiskOverlay(ctx, prepared, ov.path, ov.settings, w, h); err != nil {
			return DiskComposeResult{}, err
		}
		_ = os.Remove(ov.path)
	}
	// The prepared artifacts are already complete output planes in historical
	// B,G,R order. Preview them before publication, then rename them directly
	// into the R,G,B destinations to avoid another pair of full-plane copies.
	previewPaths := [3]string{prepared[2], prepared[1], prepared[0]}
	preview, stats, err := diskCompositePreviewForCompose(ctx, previewPaths, w, h, req.PreviewMax, nil)
	if err != nil {
		return DiskComposeResult{}, err
	}
	if err := publishDiskArtifacts(ctx, previewPaths, req.Output); err != nil {
		return DiskComposeResult{}, err
	}
	step := 1
	if w > req.PreviewMax || h > req.PreviewMax {
		if w > h {
			step = (w + req.PreviewMax - 1) / req.PreviewMax
		} else {
			step = (h + req.PreviewMax - 1) / req.PreviewMax
		}
	}
	return DiskComposeResult{Preview: preview, PreviewWidth: (w + step - 1) / step, PreviewHeight: (h + step - 1) / step, Width: w, Height: h, Stats: stats}, nil
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

// composeDiskWeighted accumulates every source directly into three bounded
// float32 planes. Sources are opened and streamed one at a time; the output
// destinations are not touched until all accumulation and preview work has
// completed.
func composeDiskWeighted(ctx context.Context, req DiskComposeRequest, w, h int) (DiskComposeResult, error) {
	acc := [3]string{}
	txs := [3]*fitsio.Float32ArtifactTransaction{}
	cleanup := func() {
		for i, tx := range txs {
			if tx != nil {
				_ = tx.Abort()
			} else if acc[i] != "" {
				_ = os.Remove(acc[i])
			}
		}
	}
	for c := range acc {
		acc[c] = req.Output[c] + fmt.Sprintf(".weighted-%d", c)
		var err error
		txs[c], err = fitsio.BeginFloat32ArtifactTransaction(acc[c], w, h)
		if err != nil {
			cleanup()
			return DiskComposeResult{}, err
		}
	}
	zero := make([]float32, w)
	for _, tx := range txs {
		for y := 0; y < h; y++ {
			if err := tx.Artifact().WriteRow(y, zero); err != nil {
				cleanup()
				return DiskComposeResult{}, err
			}
		}
	}
	for _, tx := range txs {
		if err := tx.Commit(); err != nil {
			cleanup()
			return DiskComposeResult{}, err
		}
	}
	defer func() {
		for _, p := range acc {
			_ = os.Remove(p)
		}
	}()

	type weightedDiskSource struct {
		channel  DiskChannel
		id       string
		settings models.OrangeLayerState
	}
	sources := make([]weightedDiskSource, 0, 3+len(req.Overlays))
	ids := []string{models.ComposeChannel1BlinkID, models.ComposeChannel2BlinkID, models.ComposeChannel3BlinkID}
	for i, channel := range req.Channels {
		sources = append(sources, weightedDiskSource{channel: channel, id: ids[i]})
	}
	for _, ov := range req.Overlays {
		if ov.Channel.ArtifactPath != "" {
			id := ov.Settings.BlinkID
			if id == "" {
				id = fmt.Sprintf("overlay-%d", len(sources)-2)
			}
			sources = append(sources, weightedDiskSource{channel: ov.Channel, id: id, settings: ov.Settings})
		}
	}
	lookup := make(map[string]models.ComposeMixWeight, len(req.MixWeights))
	for _, weight := range req.MixWeights {
		if err := weight.Validate(); err != nil {
			return DiskComposeResult{}, err
		}
		if weight.BlinkID == "" {
			return DiskComposeResult{}, errors.New("compose mix weight BlinkID is required")
		}
		lookup[weight.BlinkID] = weight
	}
	defaults := [][3]float64{{0, 0, 1}, {0, 1, 0}, {1, 0, 0}}
	for i, source := range sources {
		id := source.id
		weight, ok := lookup[id]
		if !ok {
			if i < 3 {
				weight = models.ComposeMixWeight{BlinkID: id, Red: defaults[i][0], Green: defaults[i][1], Blue: defaults[i][2]}
			} else {
				settings := source.settings
				opacity := math.Max(0, math.Min(1, settings.Opacity))
				weight = models.ComposeMixWeight{BlinkID: id, Red: float64(settings.ColorR) / 255 * opacity, Green: float64(settings.ColorG) / 255 * opacity, Blue: float64(settings.ColorB) / 255 * opacity}
			}
		}
		if err := streamWeightedDiskSource(ctx, source.channel, req.Channels[1].Image, acc, weight, w, h); err != nil {
			return DiskComposeResult{}, fmt.Errorf("weighted source %s: %w", id, err)
		}
	}
	if err := compressDiskRGB(ctx, acc, w, h); err != nil {
		return DiskComposeResult{}, fmt.Errorf("weighted compression: %w", err)
	}
	preview, stats, err := diskCompositePreviewForCompose(ctx, [3]string{acc[0], acc[1], acc[2]}, w, h, req.PreviewMax, nil)
	if err != nil {
		return DiskComposeResult{}, fmt.Errorf("weighted preview: %w", err)
	}
	if err := publishDiskArtifacts(ctx, acc, req.Output); err != nil {
		return DiskComposeResult{}, fmt.Errorf("weighted publish: %w", err)
	}
	step := 1
	if w > req.PreviewMax || h > req.PreviewMax {
		if w > h {
			step = (w + req.PreviewMax - 1) / req.PreviewMax
		} else {
			step = (h + req.PreviewMax - 1) / req.PreviewMax
		}
	}
	return DiskComposeResult{Preview: preview, PreviewWidth: (w + step - 1) / step, PreviewHeight: (h + step - 1) / step, Width: w, Height: h, Stats: stats}, nil
}

func streamWeightedDiskSource(ctx context.Context, src DiskChannel, ref models.LoadedImage, acc [3]string, weight models.ComposeMixWeight, w, h int) error {
	a, err := fitsio.OpenFloat32ArtifactReadOnly(src.ArtifactPath)
	if err != nil {
		return err
	}
	defer a.Close()
	mapper := newDiskCoordinateMapper(src.Image, ref, src.OffsetX, src.OffsetY, src.OffsetRot, w, h, a.Width, a.Height)
	peak, cdf, err := calibrateWeightedDiskSource(ctx, src, ref, a, w, h, mapper)
	if err != nil {
		return err
	}
	outs := [3]*fitsio.Float32Artifact{}
	for c, p := range acc {
		outs[c], err = fitsio.OpenFloat32Artifact(p)
		if err != nil {
			for _, out := range outs {
				if out != nil {
					_ = out.Close()
				}
			}
			return err
		}
		defer outs[c].Close()
	}
	sampler := newArtifactSampler(a)
	rows := [3][]float32{make([]float32, w), make([]float32, w), make([]float32, w)}
	weights := [3]float64{weight.Red, weight.Green, weight.Blue}
	for y := 0; y < h; y++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		for c := range rows {
			if err := outs[c].ReadRow(y, rows[c]); err != nil {
				return err
			}
		}
		for x := 0; x < w; x++ {
			fx, fy := mapper.mapCoordinate(x, y)
			v := float64(stretchDiskValue(sampler.sample(fx, fy), src.Image))
			if peak > 0 {
				v /= peak
			}
			if len(cdf) == 256 {
				v = float64(cdf[int(clamp01(v)*255)])
			}
			for c := range rows {
				rows[c][x] += float32(v * weights[c])
			}
		}
		for c := range rows {
			if err := outs[c].WriteRow(y, rows[c]); err != nil {
				return err
			}
		}
	}
	return nil
}

func calibrateWeightedDiskSource(ctx context.Context, src DiskChannel, ref models.LoadedImage, a *fitsio.Float32Artifact, w, h int, mapper *diskCoordinateMapper) (float64, []float32, error) {
	sampler := newArtifactSampler(a)
	peak := 0.0
	hist := make([]int, 256)
	for y := 0; y < h; y++ {
		if err := ctx.Err(); err != nil {
			return 0, nil, err
		}
		for x := 0; x < w; x++ {
			fx, fy := mapper.mapCoordinate(x, y)
			v := float64(stretchDiskValue(sampler.sample(fx, fy), src.Image))
			if v > peak {
				peak = v
			}
			if src.Image.Mode == stretch.HistEq {
				hist[int(clamp01(v)*255)]++
			}
		}
	}
	if peak == 0 {
		return 0, nil, nil
	}
	if src.Image.Mode != stretch.HistEq {
		return peak, nil, nil
	}
	// HistEq's CDF is defined over the raw stretched samples. WeightedCompose
	// then normalizes the CDF output by its own finite peak (normally 1).
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
	return 1, cdf, nil
}

func compressDiskRGB(ctx context.Context, paths [3]string, w, h int) error {
	a := [3]*fitsio.Float32Artifact{}
	for i, p := range paths {
		var err error
		a[i], err = fitsio.OpenFloat32Artifact(p)
		if err != nil {
			for _, x := range a {
				if x != nil {
					_ = x.Close()
				}
			}
			return err
		}
		defer a[i].Close()
	}
	rows := [3][]float32{make([]float32, w), make([]float32, w), make([]float32, w)}
	for y := 0; y < h; y++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		for c := range a {
			if err := a[c].ReadRow(y, rows[c]); err != nil {
				return err
			}
		}
		for x := 0; x < w; x++ {
			m := math.Max(float64(rows[0][x]), math.Max(float64(rows[1][x]), float64(rows[2][x])))
			if m > 1 {
				for c := range rows {
					rows[c][x] /= float32(m)
				}
			}
		}
		for c := range a {
			if err := a[c].WriteRow(y, rows[c]); err != nil {
				return err
			}
		}
	}
	return nil
}

func prepareDiskChannel(ctx context.Context, src DiskChannel, ref, stretchMeta models.LoadedImage, dw, dh int, dst string, applyStretch bool) error {
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
	mapper := newDiskCoordinateMapper(src.Image, ref, src.OffsetX, src.OffsetY, src.OffsetRot, dw, dh, a.Width, a.Height)
	hist := make([]int, 256)
	for y := 0; y < dh; y++ {
		if err := ctx.Err(); err != nil {
			_ = tx.Abort()
			return err
		}
		for x := 0; x < dw; x++ {
			fx, fy := mapper.mapCoordinate(x, y)
			row[x] = sampler.sample(fx, fy)
			if applyStretch {
				row[x] = stretchDiskValue(row[x], stretchMeta)
			}
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

// diskCoordinateMapper is an immutable output-to-source mapping for one
// source/render. Constructing it once avoids rediscovering WCS transforms and
// recalculating rotation constants for every sampled pixel.
type diskCoordinateMapper struct {
	aligned          bool
	affine           AffineTransform
	useWCS           bool
	wcs              AffineTransform
	dw, dh           int
	sw, sh           int
	offsetX, offsetY float64
	rotate           bool
	cosRot, sinRot   float64
}

func newDiskCoordinateMapper(img, ref models.LoadedImage, offsetX, offsetY, offsetRot float64, dw, dh, sw, sh int) *diskCoordinateMapper {
	m := &diskCoordinateMapper{
		aligned: img.HasAlignTransform,
		affine:  AffineTransform{A: img.AlignA, B: img.AlignB, C: img.AlignC, D: img.AlignD, E: img.AlignE, F: img.AlignF},
		dw:      dw, dh: dh, sw: sw, sh: sh,
		offsetX: offsetX, offsetY: offsetY,
	}
	if !m.aligned && img.Rotation90 == 0 && ref.Rotation90 == 0 && !sharedDrizzleGrid(&img, &ref) {
		if tr, err := ComputeWCSTransform(img.HDU.Header, ref.HDU.Header); err == nil {
			m.useWCS = true
			m.wcs = tr
		}
	}
	if offsetRot != 0 {
		rad := -offsetRot * math.Pi / 180
		m.rotate = true
		m.cosRot, m.sinRot = math.Cos(rad), math.Sin(rad)
	}
	return m
}

func (m *diskCoordinateMapper) mapCoordinate(x, y int) (float64, float64) {
	var fx, fy float64
	if m.aligned {
		// Stored alignment transforms are backward (reference/output -> source)
		// mappings. Apply them directly in the reference grid; composing them
		// after resize/WCS mapping would interpret the affine in the wrong frame.
		fx, fy = ApplyAffineTransform(m.affine, float64(x), float64(y))
	} else {
		fx = (float64(x)+0.5)*float64(m.sw)/float64(m.dw) - 0.5
		fy = (float64(y)+0.5)*float64(m.sh)/float64(m.dh) - 0.5
		if m.useWCS {
			fx, fy = ApplyAffineTransform(m.wcs, float64(x), float64(y))
		}
	}
	fx -= m.offsetX
	fy -= m.offsetY
	if m.rotate {
		cx, cy := float64(m.sw)/2, float64(m.sh)/2
		xc, yc := fx-cx, fy-cy
		fx, fy = m.cosRot*xc-m.sinRot*yc+cx, m.sinRot*xc+m.cosRot*yc+cy
	}
	return fx, fy
}

func mapDiskCoordinate(img, ref models.LoadedImage, offsetX, offsetY, offsetRot float64, x, y, dw, dh, sw, sh int) (float64, float64) {
	return newDiskCoordinateMapper(img, ref, offsetX, offsetY, offsetRot, dw, dh, sw, sh).mapCoordinate(x, y)
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

// publishDiskArtifacts atomically publishes a complete R,G,B artifact set.
// Sources are consumed by the successful renames. Existing destinations are
// first moved aside so any failure can restore the exact prior set.
func publishDiskArtifacts(ctx context.Context, src, dst [3]string) (err error) {
	backups := [3]string{}
	backedUp := 0
	published := 0
	committed := false
	defer func() {
		if committed {
			for _, p := range backups {
				if p != "" {
					_ = os.Remove(p)
				}
			}
			return
		}
		// Remove newly published destinations first, including a destination
		// whose source rename may have succeeded immediately before an error.
		for i := published - 1; i >= 0; i-- {
			_ = os.Remove(dst[i])
		}
		for i := backedUp - 1; i >= 0; i-- {
			if backups[i] != "" {
				_ = diskComposeRename(backups[i], dst[i])
			}
		}
	}()
	reserved := make([]string, 0, 9)
	reserved = append(reserved, src[:]...)
	reserved = append(reserved, dst[:]...)
	for i := range backups {
		backups[i] = diskComposeBackupPath(dst[i], reserved...)
		reserved = append(reserved, backups[i])
	}
	for i := range src {
		if err := ctx.Err(); err != nil {
			return err
		}
		if src[i] == "" || dst[i] == "" {
			return fmt.Errorf("missing artifact path %d", i)
		}
		if sameDiskPath(src[i], dst[i]) {
			return fmt.Errorf("artifact source overlaps destination %d", i)
		}
		if e := diskComposeRename(dst[i], backups[i]); e != nil {
			if !os.IsNotExist(e) {
				return e
			}
			backups[i] = ""
		} else {
			backedUp = i + 1
		}
	}
	for i := range src {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := diskComposeRename(src[i], dst[i]); err != nil {
			return err
		}
		published = i + 1
	}
	committed = true
	return nil
}

func diskComposeBackupPath(dst string, reserved ...string) string {
	for {
		id := atomic.AddUint64(&diskComposeTxnID, 1)
		path := fmt.Sprintf("%s.render-backup-%d-%d", dst, os.Getpid(), id)
		collision := false
		for _, other := range reserved {
			if sameDiskPath(path, other) {
				collision = true
				break
			}
		}
		if collision {
			continue
		}
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return path
		} else if err != nil {
			return path
		}
	}
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
	reserved := make([]string, 0, 9)
	reserved = append(reserved, src[:]...)
	reserved = append(reserved, dst[:]...)
	for i := range backups {
		backups[i] = diskComposeBackupPath(dst[i], reserved...)
		reserved = append(reserved, backups[i])
	}
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
