package ui

import (
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"

	"gofitsv3/internal/astroio"
	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
	"gofitsv3/internal/stretch"
)

// loadImagesFromPath loads all SCI extensions from a FITS file as separate LoadedImage values.
// For multi-chip files (e.g. HST FLC), this returns one entry per SCI extension.
// Falls back to the first HDU if no SCI extensions are found.
func loadImagesFromPath(path string) (results []*models.LoadedImage, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("fatal crash intercepted: %v", r)
		}
	}()

	if decoder, matched, probeErr := astroio.DefaultRegistry.Probe(path); probeErr != nil {
		return nil, probeErr
	} else if matched && decoder.Name() == "ASDF" {
		return loadASDFImages(path)
	}
	file, loadErr := fitsio.LoadFile(path)
	if loadErr != nil {
		return nil, loadErr
	}

	sciHDUs := file.SelectSCI()
	if len(sciHDUs) == 0 {
		sciHDUs = []fitsio.HDU{file.HDUs[0]}
	}

	primary := file.HDUs[0].Header
	for _, hdu := range sciHDUs {
		if cleaned, cleanErr := cleanHDUWithDQ(hdu, file); cleanErr == nil {
			hdu = cleaned
		}
		minV, maxV := processing.AutoLevels(hdu.Data.Pixels)
		median, sigma := processing.EstimateBackground(hdu.Data.Pixels)
		peak := median + 10*sigma
		if peak > maxV {
			peak = maxV
		}
		results = append(results, &models.LoadedImage{
			Path:       path,
			HDU:        hdu,
			Primary:    primary,
			Mode:       stretch.Linear,
			Black:      minV,
			White:      maxV,
			Background: median,
			Peak:       peak,
			ScaledPeak: 10,
			ShowClip:   true,
		})
	}
	return results, nil
}

func loadASDFImages(path string) ([]*models.LoadedImage, error) {
	source, err := astroio.Open(context.Background(), path)
	if err != nil {
		return nil, err
	}
	meta, err := source.Metadata(context.Background())
	if err != nil {
		return nil, err
	}
	var info astroio.PlaneInfo
	for _, candidate := range meta.Planes {
		if candidate.ID == astroio.PlaneID("asdf:data") {
			info = candidate
			break
		}
	}
	if info.ID == "" {
		return nil, fmt.Errorf("ASDF file has no data plane")
	}
	plane, err := source.ReadPlane(context.Background(), info.ID, astroio.ReadOptions{})
	if err != nil {
		return nil, err
	}
	primary := fitsio.Header{Cards: meta.Cards}
	data := fitsio.ImageData{Width: plane.Width, Height: plane.Height, Pixels: plane.Data}
	minV, maxV := processing.AutoLevels(data.Pixels)
	median, sigma := processing.EstimateBackground(data.Pixels)
	peak := median + 10*sigma
	if peak > maxV {
		peak = maxV
	}
	return []*models.LoadedImage{{Path: path, HDU: fitsio.HDU{Header: primary, Data: data, ExtName: "data"}, Primary: primary, Mode: stretch.Linear, Black: minV, White: maxV, Background: median, Peak: peak, ScaledPeak: 10, ShowClip: true}}, nil
}

func loadImageFromPath(path string) (*models.LoadedImage, error) {
	imgs, err := loadImagesFromPath(path)
	if err != nil {
		return nil, err
	}
	return imgs[0], nil
}

func loadLargeComposeImage(path string, store *composeLargeStore, slot string) (*models.LoadedImage, *image.RGBA, composeArtifactDescriptor, error) {
	name, extver := "SCI", ""
	primary, hdu, err := fitsio.InspectSelectedHDU(path, name, extver)
	if err != nil {
		name = ""
		primary, hdu, err = fitsio.InspectSelectedHDU(path, name, extver)
	}
	if err != nil {
		return nil, nil, composeArtifactDescriptor{}, err
	}
	d, err := store.ReplaceFromFITS(slot, path, name, extver)
	if err != nil {
		return nil, nil, composeArtifactDescriptor{}, err
	}
	preview, minV, maxV, err := composeLargePreview(d.Path)
	if err != nil {
		_, _ = store.RemoveSlotIfCurrent(d)
		return nil, nil, composeArtifactDescriptor{}, err
	}
	img := &models.LoadedImage{Path: path, Primary: primary, HDU: hdu, Mode: stretch.Linear, Black: float64(minV), White: float64(maxV), Peak: float64(maxV), ScaledPeak: 10, ShowClip: true}
	img.HDU.Data.Pixels = nil
	return img, preview, d, nil
}

// stageComposeLargeReset restores source pixels while retaining the current
// stretch/alignment state. Every source and preview is prepared before the
// store publishes any replacement.
func stageComposeLargeReset(ctx context.Context, store *composeLargeStore, imgs []*models.LoadedImage, expected []composeArtifactDescriptor) ([]composeArtifactDescriptor, []*image.RGBA, []fitsio.HDU, []fitsio.Header, error) {
	if len(expected) != len(imgs) {
		return nil, nil, nil, nil, errors.New("reset channel count mismatch")
	}
	sizes := make([][2]int, len(imgs))
	hdus := make([]fitsio.HDU, len(imgs))
	primaries := make([]fitsio.Header, len(imgs))
	writes := make([]func(*fitsio.Float32Artifact) error, len(imgs))
	for i, img := range imgs {
		if img == nil {
			return nil, nil, nil, nil, fmt.Errorf("missing channel %d", i+1)
		}
		name := img.HDU.ExtName
		extver := fitsio.HeaderString(img.HDU.Header, "EXTVER")
		primary, hdu, err := fitsio.InspectSelectedHDU(img.Path, name, extver)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		turns := ((img.Rotation90 % 4) + 4) % 4
		w, h := hdu.Data.Width, hdu.Data.Height
		if turns%2 != 0 {
			w, h = h, w
		}
		sizes[i] = [2]int{w, h}
		hdus[i], primaries[i] = hdu, primary
		slot := expected[i].Slot
		writes[i] = func(out *fitsio.Float32Artifact) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			raw := filepath.Join(store.root, fmt.Sprintf("%s-reset-source.tmp", slot))
			defer os.Remove(raw)
			if err := fitsio.CopySelectedHDUToRawFloat32Artifact(img.Path, name, extver, raw); err != nil {
				return err
			}
			src, err := fitsio.OpenFloat32ArtifactReadOnly(raw)
			if err != nil {
				return err
			}
			defer src.Close()
			current := src
			var intermediates []*fitsio.Float32Artifact
			var intermediatePaths []string
			defer func() {
				for _, a := range intermediates {
					_ = a.Close()
				}
				for _, p := range intermediatePaths {
					_ = os.Remove(p)
				}
			}()
			for turn := 0; turn < turns; turn++ {
				if err := ctx.Err(); err != nil {
					return err
				}
				last := turn == turns-1
				if last {
					row := make([]float32, current.Height)
					for y := 0; y < current.Width; y++ {
						if err := current.ReadRowRotatedCW(y, row); err != nil {
							return err
						}
						if err := out.WriteRow(y, row); err != nil {
							return err
						}
					}
					continue
				}
				p := filepath.Join(store.root, fmt.Sprintf("%s-reset-turn-%d.tmp", slot, turn))
				_ = os.Remove(p)
				intermediatePaths = append(intermediatePaths, p)
				next, err := fitsio.CreateFloat32Artifact(p, current.Height, current.Width)
				if err != nil {
					return err
				}
				intermediates = append(intermediates, next)
				row := make([]float32, current.Height)
				for y := 0; y < current.Width; y++ {
					if err := current.ReadRowRotatedCW(y, row); err != nil {
						return err
					}
					if err := next.WriteRow(y, row); err != nil {
						return err
					}
				}
				if err := next.Sync(); err != nil {
					return err
				}
				current = next
			}
			if turns == 0 {
				row := make([]float32, current.Width)
				for y := 0; y < current.Height; y++ {
					if err := current.ReadRow(y, row); err != nil {
						return err
					}
					if err := out.WriteRow(y, row); err != nil {
						return err
					}
				}
			}
			return nil
		}
	}
	var previews []*image.RGBA
	descs, err := store.ReplaceManyIfCurrentSized(expected, sizes, writes, func(staged []composeArtifactDescriptor) error {
		previews = make([]*image.RGBA, len(staged))
		for i, d := range staged {
			if err := ctx.Err(); err != nil {
				return err
			}
			clone := *imgs[i]
			clone.HDU.Data.Width, clone.HDU.Data.Height = d.Width, d.Height
			clone.HDU.Data.Pixels = nil
			var e error
			previews[i], _, _, e = composeLargeStretchedPreview(d.Path, &clone)
			if e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return descs, previews, hdus, primaries, nil
}
