package ui

import (
	"context"
	"fmt"
	"strings"

	"gofitsv3/internal/astroio"
	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
	"gofitsv3/internal/stretch"
)

// loadExaminePlanesFromPath decodes every supported image plane in a source.
// It is deliberately separate from loadImagesFromPath: Compose's loader keeps
// its historical SCI-only selection and DQ repair semantics.
func loadExaminePlanesFromPath(path string) ([]*models.LoadedImage, []astroio.PlaneID, error) {
	source, err := astroio.Open(context.Background(), path)
	if err != nil {
		return nil, nil, err
	}
	meta, err := source.Metadata(context.Background())
	if err != nil {
		return nil, nil, err
	}
	var fitsFile *fitsio.File
	if meta.Format == "FITS" {
		fitsFile, err = fitsio.LoadFile(path)
		if err != nil {
			return nil, nil, err
		}
	}
	images := make([]*models.LoadedImage, 0, len(meta.Planes))
	ids := make([]astroio.PlaneID, 0, len(meta.Planes))
	for _, info := range meta.Planes {
		plane, readErr := source.ReadPlane(context.Background(), info.ID, astroio.ReadOptions{})
		if readErr != nil {
			return nil, nil, fmt.Errorf("read %s plane %q: %w", meta.Format, info.Name, readErr)
		}
		if plane.Width <= 0 || plane.Height <= 0 || len(plane.Data) != plane.Width*plane.Height {
			continue
		}
		data := fitsio.ImageData{Width: plane.Width, Height: plane.Height, Pixels: plane.Data}
		header := fitsio.Header{Cards: cloneExamineCards(meta.Cards)}
		hdu := fitsio.HDU{Header: header, Data: data, ExtName: info.Name}
		if fitsFile != nil {
			var index int
			if _, scanErr := fmt.Sscanf(string(info.ID), "fits:hdu:%d", &index); scanErr == nil && index >= 0 && index < len(fitsFile.HDUs) {
				hdu = fitsFile.HDUs[index]
				if info.Kind == "science" {
					if cleaned, cleanErr := cleanHDUWithDQ(hdu, fitsFile); cleanErr == nil {
						hdu = cleaned
					}
				}
				if len(hdu.Data.Pixels) == hdu.Data.Width*hdu.Data.Height {
					data = hdu.Data
				}
				header = hdu.Header
			}
		}
		minV, maxV := processing.AutoLevels(data.Pixels)
		median, sigma := processing.EstimateBackground(data.Pixels)
		peak := median + 10*sigma
		if peak > maxV {
			peak = maxV
		}
		if fitsFile != nil {
			header = fitsFile.HDUs[0].Header
		}
		img := &models.LoadedImage{Path: path, Primary: header, HDU: hdu, Mode: stretch.Linear, Black: minV, White: maxV, Background: median, Peak: peak, ScaledPeak: 10, ShowClip: true}
		images = append(images, img)
		ids = append(ids, info.ID)
	}
	if len(images) == 0 {
		return nil, nil, fmt.Errorf("%s file has no supported image planes", meta.Format)
	}
	return images, ids, nil
}

// loadExamineDiagnosticsFromPath performs the optional full-file FITS decode
// on the worker goroutine. Diagnostics are supplemental, so malformed or
// non-FITS input simply yields no diagnostic layers; the image-plane load
// reports its own actionable error.
func loadExamineDiagnosticsFromPath(path string) map[string]*models.LoadedImage {
	layers := make(map[string]*models.LoadedImage)
	diagnosticFile, err := fitsio.LoadFile(path)
	if err != nil {
		return layers
	}
	for _, hdu := range diagnosticFile.HDUs {
		name := fitsio.HeaderString(hdu.Header, "EXTNAME")
		if name == "WHT" || name == "NCONTRIB" || name == "CRMASK" || name == "DQ" || name == "SKYMODEL" || name == "SEAM" || (strings.HasPrefix(name, "CTX") && name != "CTXMAP") {
			layers[name] = diagnosticLoadedImage(name, path, hdu.Data)
		}
	}
	return layers
}

func cloneExamineCards(cards map[string]string) map[string]string {
	result := make(map[string]string, len(cards))
	for key, value := range cards {
		result[key] = value
	}
	return result
}

func examinePlaneLabel(img *models.LoadedImage, id astroio.PlaneID, ordinal int) string {
	name := "image"
	if img != nil && strings.TrimSpace(img.HDU.ExtName) != "" {
		name = strings.TrimSpace(img.HDU.ExtName)
	}
	if name == "image" {
		if strings.HasPrefix(string(id), "asdf:") {
			name = strings.TrimPrefix(string(id), "asdf:")
		} else {
			name = "HDU"
		}
	}
	return fmt.Sprintf("%s %d (%s)", name, ordinal, id)
}

func examinePlaneLabels(images []*models.LoadedImage, ids []astroio.PlaneID) []string {
	labels := make([]string, len(images))
	for i, img := range images {
		var id astroio.PlaneID
		if i < len(ids) {
			id = ids[i]
		}
		labels[i] = examinePlaneLabel(img, id, i+1)
	}
	return labels
}

func applyExamineChannelState(img *models.LoadedImage, state models.ChannelState) {
	img.Mode = labelToMode(state.Mode)
	img.Black = state.Black
	img.White = state.White
	img.Background = state.Background
	img.Peak = state.Peak
	img.ScaledPeak = state.ScaledPeak
	img.MTFMidtone = state.MTFMidtone
	img.ShowClip = state.ShowClip
}
