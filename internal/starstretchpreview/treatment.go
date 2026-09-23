package starstretchpreview

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/mosaic"
	"gofitsv3/internal/processing"
)

// TreatmentFits holds the stretch-independent star fits for one original
// science file and its reviewed map. Compose keeps one per source and
// rebuilds only the stretch-dependent model when settings change.
type TreatmentFits struct {
	SciencePath, MapPath string
	Width, Height        int
	mapSize              int64
	mapModTime           time.Time
	fieldFWHM            float64
	Fits                 []processing.StarTreatmentFit
	Sources              []processing.StarMapSource
	Usable               int
}

// Current reports whether the fits still describe the given file, map and
// grid: same paths, the map file unchanged on disk, and the same size (a
// zero size means the caller has no in-memory grid to compare).
func (f *TreatmentFits) Current(sciencePath, mapPath string, w, h int) bool {
	if f == nil || f.SciencePath != sciencePath || f.MapPath != mapPath || (w != 0 && (f.Width != w || f.Height != h)) {
		return false
	}
	st, err := os.Stat(mapPath)
	return err == nil && st.Size() == f.mapSize && st.ModTime().Equal(f.mapModTime)
}

// ResolveMapPath returns the reviewed map path for a science file, using the
// default working location when none is given.
func ResolveMapPath(sciencePath, mapPath string) (string, error) {
	path, err := filepath.Abs(sciencePath)
	if err != nil {
		return "", err
	}
	if mapPath == "" {
		mapPath = mosaic.StarMapWorkingPath(path)
	}
	return filepath.Abs(mapPath)
}

// loadTreatmentScience reopens the original science file and validates the
// reviewed map against it. It is the same gate the preview uses.
func loadTreatmentScience(ctx context.Context, sciencePath, mapPath string) (fitsio.HDU, *mosaic.StarMapProduct, os.FileInfo, error) {
	mapStat, err := os.Stat(mapPath)
	if err != nil {
		return fitsio.HDU{}, nil, nil, fmt.Errorf("open reviewed star map (create it first in Mosaic): %w", err)
	}
	file, err := fitsio.LoadFile(sciencePath)
	if err != nil {
		return fitsio.HDU{}, nil, nil, err
	}
	if err := ctx.Err(); err != nil {
		return fitsio.HDU{}, nil, nil, err
	}
	if len(file.HDUs) == 0 {
		return fitsio.HDU{}, nil, nil, fmt.Errorf("empty science file")
	}
	hdu := file.HDUs[0]
	if hdu.Data.Width < 25 || hdu.Data.Height < 25 || fitsio.HeaderString(hdu.Header, "PRODUCT") != "" {
		return fitsio.HDU{}, nil, nil, fmt.Errorf("select an original primary-image science mosaic, not a derived mask")
	}
	product, err := mosaic.LoadStarMapFITS(ctx, mapPath, hdu.Data, hdu.Header)
	if err != nil {
		return fitsio.HDU{}, nil, nil, fmt.Errorf("validate reviewed star map: %w", err)
	}
	return hdu, product, mapStat, nil
}

// FitTreatment fits every accepted map source of one science file. When
// pixels is nil the original file is reloaded (disk-backed Compose); when
// given, it must be that file's unmodified linear data on its own grid.
func FitTreatment(ctx context.Context, sciencePath, mapPath string, pixels []float32, w, h int, progress func(string, int, int)) (*TreatmentFits, error) {
	sciencePath, err := filepath.Abs(sciencePath)
	if err != nil {
		return nil, err
	}
	mapPath, err = ResolveMapPath(sciencePath, mapPath)
	if err != nil {
		return nil, err
	}
	if progress != nil {
		progress("Reading original linear source and star map", 0, 0)
	}
	hdu, product, mapStat, err := loadTreatmentScience(ctx, sciencePath, mapPath)
	if err != nil {
		return nil, err
	}
	if pixels == nil {
		pixels, w, h = hdu.Data.Pixels, hdu.Data.Width, hdu.Data.Height
	} else if w != hdu.Data.Width || h != hdu.Data.Height || len(pixels) != w*h {
		return nil, fmt.Errorf("loaded source is %dx%d but its original file is %dx%d; the star map applies to the original grid", w, h, hdu.Data.Width, hdu.Data.Height)
	}
	fits, err := processing.FitStarTreatment(ctx, product.Map, pixels, w, h, processing.StarTreatmentOptions{Progress: progress})
	if err != nil {
		return nil, err
	}
	out := &TreatmentFits{SciencePath: sciencePath, MapPath: mapPath, Width: w, Height: h, mapSize: mapStat.Size(), mapModTime: mapStat.ModTime(),
		fieldFWHM: product.Map.FWHM, Fits: fits, Sources: product.Map.Sources}
	for _, f := range fits {
		if f.Usable {
			out.Usable++
		}
	}
	return out, nil
}

// Model prepares the fits for the given scalar stretch settings and strength.
// When pixels is nil the original file is reloaded.
func (f *TreatmentFits) Model(ctx context.Context, meta models.LoadedImage, strength float64, pixels []float32, progress func(string, int, int)) (*processing.StarTreatmentModel, int, error) {
	if f == nil {
		return nil, 0, fmt.Errorf("no star fits")
	}
	if pixels == nil {
		if progress != nil {
			progress("Reading original linear source", 0, 0)
		}
		file, err := fitsio.LoadFile(f.SciencePath)
		if err != nil {
			return nil, 0, err
		}
		if len(file.HDUs) == 0 || file.HDUs[0].Data.Width != f.Width || file.HDUs[0].Data.Height != f.Height {
			return nil, 0, fmt.Errorf("original source %s changed size", f.SciencePath)
		}
		pixels = file.HDUs[0].Data.Pixels
	}
	if len(pixels) != f.Width*f.Height {
		return nil, 0, fmt.Errorf("source pixels do not match the fitted %dx%d grid", f.Width, f.Height)
	}
	if progress != nil {
		progress("Preparing stellar footprints for the current stretch", 0, 0)
	}
	prepared, err := processing.PrepareStarStretchFits(ctx, pixels, f.Width, f.Height, scalarMetadata(meta), f.Fits, f.Sources)
	if err != nil {
		return nil, 0, err
	}
	model, err := processing.NewStarTreatmentModel(prepared, f.Width, f.Height, scalarMetadata(meta), strength)
	if err != nil {
		return nil, 0, err
	}
	return model, len(model.Fits()), nil
}

// DeriveTreatmentModel builds a source's model from a reference source's
// prepared geometry (see processing.DeriveStarTreatmentFits). The target must
// be on the reference's grid. When pixels is nil the target file is reloaded.
func DeriveTreatmentModel(ctx context.Context, ref *TreatmentFits, refModel *processing.StarTreatmentModel, sciencePath string, meta models.LoadedImage, strength float64, pixels []float32, progress func(string, int, int)) (*processing.StarTreatmentModel, int, error) {
	if ref == nil || refModel == nil {
		return nil, 0, fmt.Errorf("reference star geometry is not prepared")
	}
	w, h := refModel.SourceSize()
	if pixels == nil {
		if progress != nil {
			progress("Reading original linear source", 0, 0)
		}
		file, err := fitsio.LoadFile(sciencePath)
		if err != nil {
			return nil, 0, err
		}
		if len(file.HDUs) == 0 {
			return nil, 0, fmt.Errorf("empty science file")
		}
		if file.HDUs[0].Data.Width != w || file.HDUs[0].Data.Height != h {
			return nil, 0, fmt.Errorf("%s is %dx%d; shared star geometry needs the reference grid %dx%d", sciencePath, file.HDUs[0].Data.Width, file.HDUs[0].Data.Height, w, h)
		}
		pixels = file.HDUs[0].Data.Pixels
	}
	if len(pixels) != w*h {
		return nil, 0, fmt.Errorf("source pixels do not match the reference %dx%d grid; shared star geometry needs sources on one grid", w, h)
	}
	if progress != nil {
		progress("Measuring star backgrounds in this source", 0, 0)
	}
	derived, err := processing.DeriveStarTreatmentFits(ctx, refModel.Fits(), ref.Sources, pixels, w, h, processing.StarTreatmentOptions{Progress: progress})
	if err != nil {
		return nil, 0, err
	}
	model, err := processing.NewStarTreatmentModel(derived, w, h, scalarMetadata(meta), strength)
	if err != nil {
		return nil, 0, err
	}
	return model, len(model.Fits()), nil
}
