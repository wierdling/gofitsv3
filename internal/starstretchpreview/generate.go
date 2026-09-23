package starstretchpreview

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/mosaic"
	"gofitsv3/internal/processing"
	"gofitsv3/internal/stretch"
)

func validNumber(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func validateRequest(req Request) error {
	if req.SciencePath == "" {
		return fmt.Errorf("select an original linear mosaic FITS")
	}
	if !validNumber(req.Strength) || req.Strength < 0 || req.Strength > 1 {
		return fmt.Errorf("strength must be finite and in 0..1")
	}
	if req.Limit < 1 || req.Limit > 24 || req.SourceID < 0 {
		return fmt.Errorf("preview count must be 1..24 and source ID must be nonnegative")
	}
	switch req.Metadata.Mode {
	case stretch.Linear, stretch.Log, stretch.Asinh, stretch.Sqrt, stretch.MTF:
	default:
		return fmt.Errorf("this preview supports Linear, Log, Asinh, Sqrt and MTF; select a supported source stretch")
	}
	if !req.AutoLevels && (!validNumber(req.Metadata.Background) || !validNumber(req.Metadata.Peak) || req.Metadata.Peak <= req.Metadata.Background) {
		return fmt.Errorf("stretch peak must be finite and greater than background")
	}
	for _, v := range []float64{req.Metadata.ScaledPeak, req.Metadata.AsinhScale, req.Metadata.MTFMidtone} {
		if !validNumber(v) {
			return fmt.Errorf("stretch parameters must be finite")
		}
	}
	if req.Metadata.ScaledPeak < 0 || req.Metadata.AsinhScale < 0 || req.Metadata.MTFMidtone < 0 || req.Metadata.MTFMidtone >= 1 {
		return fmt.Errorf("invalid stretch parameters")
	}
	return nil
}

// Generate reloads the original science file, verifies its saved map, and
// evaluates bounded source-grid cutouts. It intentionally does not render a
// composite or modify the supplied metadata's image buffers.
func Generate(ctx context.Context, req Request, progress func(string, int, int)) (*Report, error) {
	if err := validateRequest(req); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := filepath.Abs(req.SciencePath)
	if err != nil {
		return nil, err
	}
	mapPath := req.MapPath
	if mapPath == "" {
		mapPath = mosaic.StarMapWorkingPath(path)
	}
	mapPath, err = filepath.Abs(mapPath)
	if err != nil {
		return nil, err
	}
	scienceStat, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	mapStat, err := os.Stat(mapPath)
	if err != nil {
		return nil, fmt.Errorf("open reviewed star map (create it first in Mosaic): %w", err)
	}
	if progress != nil {
		progress("Reading original linear mosaic", 0, 0)
	}
	file, err := fitsio.LoadFile(path)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(file.HDUs) == 0 {
		return nil, fmt.Errorf("empty science file")
	}
	hdu := file.HDUs[0]
	if hdu.Data.Width < 25 || hdu.Data.Height < 25 || fitsio.HeaderString(hdu.Header, "PRODUCT") != "" {
		return nil, fmt.Errorf("select an original primary-image science mosaic, not a derived mask")
	}
	if progress != nil {
		progress("Validating saved star map against science", 0, 0)
	}
	product, err := mosaic.LoadStarMapFITS(ctx, mapPath, hdu.Data, hdu.Header)
	if err != nil {
		return nil, fmt.Errorf("validate reviewed star map: %w", err)
	}
	meta := scalarMetadata(req.Metadata)
	if req.AutoLevels {
		tmp := meta
		tmp.HDU = hdu
		tmp.Black, tmp.White = 0, 0
		processing.AutoScaleLikeFitsLiberator(&tmp)
		meta.Background, meta.Peak = tmp.Background, tmp.Peak
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	report := &Report{SciencePath: path, MapPath: mapPath, Strength: req.Strength, Metadata: meta}
	names := map[stretch.Mode]string{stretch.Linear: "Linear", stretch.Log: "Log", stretch.Asinh: "Asinh", stretch.Sqrt: "Sqrt", stretch.MTF: "MTF"}
	report.Settings = fmt.Sprintf("%s · Background %g · Peak %g · Scaled peak %g", names[meta.Mode], meta.Background, meta.Peak, meta.ScaledPeak)
	if meta.Mode == stretch.MTF {
		report.Settings += fmt.Sprintf(" · Midtone %g", meta.MTFMidtone)
	}
	selected := selectSources(product.Map.Sources, req.SourceID, req.Limit)
	if len(selected) == 0 {
		return nil, fmt.Errorf("no accepted sources match the requested selection")
	}
	usable := 0
	for i, source := range selected {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if progress != nil {
			progress("Fitting and comparing stellar cutouts", i+1, len(selected))
		}
		preview, err := previewSource(ctx, hdu.Data, product.Map, source, meta, req.Strength)
		if err != nil {
			return nil, err
		}
		if preview.Status == "preview" {
			usable++
		}
		report.Stars = append(report.Stars, preview)
	}
	for _, item := range []struct {
		path   string
		before os.FileInfo
	}{{path, scienceStat}, {mapPath, mapStat}} {
		after, err := os.Stat(item.path)
		if err != nil {
			return nil, err
		}
		if after.Size() != item.before.Size() || !after.ModTime().Equal(item.before.ModTime()) {
			return nil, fmt.Errorf("input changed during preview; rerun: %s", item.path)
		}
	}
	report.Summary = fmt.Sprintf("%d of %d selected stars have a usable gentler-stretch preview; %d skipped. Experimental source-grid diagnostic; no Compose changes applied.", usable, len(selected), len(selected)-usable)
	return report, ctx.Err()
}

func scalarMetadata(m models.LoadedImage) models.LoadedImage {
	return models.LoadedImage{Mode: m.Mode, Background: m.Background, Peak: m.Peak, ScaledPeak: m.ScaledPeak,
		AsinhScale: m.AsinhScale, MTFMidtone: m.MTFMidtone, GHSStretch: m.GHSStretch, GHSLocal: m.GHSLocal, GHSSymmetry: m.GHSSymmetry}
}

func selectSources(sources []processing.StarMapSource, id, limit int) []processing.StarMapSource {
	var selected []processing.StarMapSource
	for _, s := range sources {
		if s.Accepted() && (id == 0 || s.ID == id) {
			selected = append(selected, s)
		}
	}
	sort.SliceStable(selected, func(i, j int) bool {
		a, b := selected[i].Amplitude, selected[j].Amplitude
		if !validNumber(a) {
			a = 0
		}
		if !validNumber(b) {
			b = 0
		}
		if a != b {
			return a > b
		}
		return selected[i].ID < selected[j].ID
	})
	if len(selected) > limit {
		selected = selected[:limit]
	}
	return selected
}

func previewSource(ctx context.Context, science fitsio.ImageData, catalog *processing.StarMap, source processing.StarMapSource, meta models.LoadedImage, strength float64) (StarPreview, error) {
	preview := StarPreview{SourceID: source.ID, Status: "skipped"}
	if !validNumber(source.X) || !validNumber(source.Y) || source.X < 0 || source.Y < 0 || source.X >= float64(science.Width) || source.Y >= float64(science.Height) || !validNumber(source.Radius) {
		preview.Reason = "Invalid catalog geometry"
		return preview, nil
	}
	// Extended halo/spike validation can use up to 192 pixels of radial support;
	// keep the source-grid cutout large enough that a valid feather is visible.
	radius := 196 // background margin beyond the maximum measured support
	x0, y0 := max(0, int(math.Floor(source.X))-radius), max(0, int(math.Floor(source.Y))-radius)
	x1, y1 := min(science.Width, int(math.Ceil(source.X))+radius+1), min(science.Height, int(math.Ceil(source.Y))+radius+1)
	w, h := x1-x0, y1-y0
	preview.CutoutX, preview.CutoutY, preview.Width, preview.Height = x0, y0, w, h
	pixels := make([]float32, w*h)
	for y := 0; y < h; y++ {
		copy(pixels[y*w:(y+1)*w], science.Pixels[(y+y0)*science.Width+x0:(y+y0)*science.Width+x1])
	}
	local := &processing.StarMap{Width: w, Height: h}
	for _, s := range catalog.Sources {
		if s.X >= float64(x0) && s.X < float64(x1) && s.Y >= float64(y0) && s.Y < float64(y1) {
			s.X -= float64(x0)
			s.Y -= float64(y0)
			local.Sources = append(local.Sources, s)
		}
	}
	normal, err := processing.ApplyGentlerStarStretch(ctx, pixels, w, h, meta, nil, nil, processing.StarStretchOptions{})
	if err != nil {
		return preview, err
	}
	preview.Normal = grayImage(normal, w, h)
	fits, err := processing.FitStarTreatment(ctx, local, pixels, w, h, processing.StarTreatmentOptions{})
	if err != nil {
		return preview, err
	}
	// The selected star leads the target list; blended group members follow
	// so the preview renders the shared footprint the way Compose will.
	var target []processing.StarTreatmentFit
	for _, f := range fits {
		if f.SourceID == source.ID {
			target = append(target, f)
			preview.Diagnostics = f
			preview.Reason = f.Reason
		}
	}
	if len(target) != 1 || !target[0].Usable {
		return preview, nil
	}
	for _, f := range fits {
		if f.Usable && f.SourceID != source.ID && f.GroupID != 0 && f.GroupID == target[0].GroupID {
			target = append(target, f)
		}
	}
	target, err = processing.PrepareStarStretchFits(ctx, pixels, w, h, meta, target, local.Sources)
	if err != nil {
		if ctx.Err() != nil {
			return preview, ctx.Err()
		}
		preview.Reason = err.Error()
		return preview, nil
	}
	preview.Diagnostics = target[0]
	preview.Reason = target[0].Reason
	if !target[0].Usable {
		return preview, nil
	}
	mask, err := processing.BuildStarTreatmentMask(ctx, target, w, h)
	if err != nil {
		return preview, err
	}
	gentle, err := processing.ApplyGentlerStarStretch(ctx, pixels, w, h, meta, target, mask, processing.StarStretchOptions{Strength: strength})
	if err != nil {
		if ctx.Err() != nil {
			return preview, ctx.Err()
		}
		preview.Reason = err.Error()
		return preview, nil
	}
	preview.Status = "preview"
	preview.Gentle = grayImage(gentle, w, h)
	preview.Mask = grayImage(mask, w, h)
	addRadialProfiles(&preview, normal, gentle, mask, w, h, target[0].X, target[0].Y, processing.StarTreatmentExtent(target[0]))
	return preview, nil
}
