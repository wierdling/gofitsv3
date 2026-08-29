package processing

import (
	"context"
	"errors"
	"image"
	"math"
	"os"
	"path/filepath"

	"gofitsv3/internal/fitsio"
)

// DiskHealStroke describes one full-resolution heal stroke. Destinations are
// applied in order, but every sample is read from the source artifact.
type DiskHealStroke struct {
	Source       image.Point
	Destinations []image.Point
	Radius       int
}

// HealDisk applies a stroke one output row at a time. It never reads from the
// output while computing a stroke, so overlapping destinations are ordered and
// deterministic without materializing the source raster.
func HealDisk(ctx context.Context, source, output [3]string, width, height int, stroke DiskHealStroke) (err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateDiskTransformPaths(source, output); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			for _, p := range output {
				if p != "" {
					_ = os.Remove(p)
				}
			}
		}
	}()
	if width <= 0 || height <= 0 || stroke.Radius < 1 || len(stroke.Destinations) == 0 {
		return errors.New("invalid disk heal request")
	}
	for i := range source {
		if source[i] == "" || output[i] == "" {
			return errors.New("missing disk heal artifact")
		}
	}
	txs := make([]*fitsio.Float32ArtifactTransaction, 3)
	abort := func() {
		for _, tx := range txs {
			if tx != nil {
				_ = tx.Abort()
			}
		}
	}
	for c := 0; c < 3; c++ {
		src, err := fitsio.OpenFloat32ArtifactReadOnly(source[c])
		if err != nil {
			abort()
			return err
		}
		if src.Width != width || src.Height != height {
			_ = src.Close()
			abort()
			return errors.New("disk heal dimensions mismatch")
		}
		tx, err := fitsio.BeginFloat32ArtifactTransaction(output[c], width, height)
		if err != nil {
			_ = src.Close()
			abort()
			return err
		}
		txs[c] = tx
		target, sample := make([]float32, width), make([]float32, width)
		for y := 0; y < height; y++ {
			if err := ctx.Err(); err != nil {
				_ = src.Close()
				abort()
				return err
			}
			if err := src.ReadRow(y, target); err != nil {
				_ = src.Close()
				abort()
				return err
			}
			for _, dst := range stroke.Destinations {
				if y < dst.Y-stroke.Radius || y > dst.Y+stroke.Radius {
					continue
				}
				for dy := -stroke.Radius; dy <= stroke.Radius; dy++ {
					if dst.Y+dy != y {
						continue
					}
					sy := stroke.Source.Y + dy
					if sy < 0 || sy >= height {
						break
					}
					if err := src.ReadRow(sy, sample); err != nil {
						_ = src.Close()
						abort()
						return err
					}
					for dx := -stroke.Radius; dx <= stroke.Radius; dx++ {
						dist := math.Hypot(float64(dx), float64(dy))
						if dist > float64(stroke.Radius) {
							continue
						}
						x, sx := dst.X+dx, stroke.Source.X+dx
						if x < 0 || x >= width || sx < 0 || sx >= width {
							continue
						}
						feather := stroke.Radius / 4
						if feather < 1 {
							feather = 1
						}
						inner := float64(stroke.Radius - feather)
						w := 1.0
						if dist > inner {
							w = .5 * (1 + math.Cos((dist-inner)/float64(feather)*math.Pi))
						}
						target[x] = float32(float64(sample[sx])*w + float64(target[x])*(1-w))
					}
					break
				}
			}
			if err := tx.Artifact().WriteRow(y, target); err != nil {
				_ = src.Close()
				abort()
				return err
			}
		}
		_ = src.Close()
	}
	for _, tx := range txs {
		if err := tx.Artifact().Sync(); err != nil {
			abort()
			return err
		}
	}
	for _, tx := range txs {
		if err := tx.Commit(); err != nil {
			abort()
			return err
		}
	}
	return nil
}

// CropDisk copies a rectangular region using bounded row buffers.
func CropDisk(ctx context.Context, source, output [3]string, width, height int, rect image.Rectangle) (w, h int, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateDiskTransformPaths(source, output); err != nil {
		return 0, 0, err
	}
	defer func() {
		if err != nil {
			for _, p := range output {
				if p != "" {
					_ = os.Remove(p)
				}
			}
		}
	}()
	rect = rect.Intersect(image.Rect(0, 0, width, height))
	if rect.Empty() {
		return 0, 0, errors.New("empty disk crop")
	}
	w, h = rect.Dx(), rect.Dy()
	txs := make([]*fitsio.Float32ArtifactTransaction, 3)
	abort := func() {
		for _, tx := range txs {
			if tx != nil {
				_ = tx.Abort()
			}
		}
	}
	for c := 0; c < 3; c++ {
		src, err := fitsio.OpenFloat32ArtifactReadOnly(source[c])
		if err != nil {
			abort()
			return 0, 0, err
		}
		if src.Width != width || src.Height != height {
			_ = src.Close()
			abort()
			return 0, 0, errors.New("disk crop dimensions mismatch")
		}
		tx, err := fitsio.BeginFloat32ArtifactTransaction(output[c], w, h)
		if err != nil {
			_ = src.Close()
			abort()
			return 0, 0, err
		}
		txs[c] = tx
		row, out := make([]float32, width), make([]float32, w)
		for y := 0; y < h; y++ {
			if err := ctx.Err(); err != nil {
				_ = src.Close()
				abort()
				return 0, 0, err
			}
			if err := src.ReadRow(rect.Min.Y+y, row); err != nil {
				_ = src.Close()
				abort()
				return 0, 0, err
			}
			copy(out, row[rect.Min.X:rect.Max.X])
			if err := tx.Artifact().WriteRow(y, out); err != nil {
				_ = src.Close()
				abort()
				return 0, 0, err
			}
		}
		_ = src.Close()
	}
	for _, tx := range txs {
		if err := tx.Artifact().Sync(); err != nil {
			abort()
			return 0, 0, err
		}
	}
	for _, tx := range txs {
		if err := tx.Commit(); err != nil {
			abort()
			return 0, 0, err
		}
	}
	return w, h, nil
}

// validateDiskTransformPaths runs before the failure cleanup hook is armed.
// Output destinations are intentionally required to be new: rollback removes
// published output names after a later plane fails, so replacing a caller's
// pre-existing artifact would make failure destructive.
func validateDiskTransformPaths(source, output [3]string) error {
	seen := make(map[string]struct{}, len(output))
	sources := make(map[string]struct{}, len(source))
	for _, p := range source {
		if p == "" {
			return errors.New("missing disk transform source")
		}
		abs, err := filepath.Abs(filepath.Clean(p))
		if err != nil {
			return err
		}
		sources[abs] = struct{}{}
	}
	for _, p := range output {
		if p == "" {
			return errors.New("missing disk transform output")
		}
		abs, err := filepath.Abs(filepath.Clean(p))
		if err != nil {
			return err
		}
		if _, ok := sources[abs]; ok {
			return errors.New("disk transform output overlaps source")
		}
		if _, ok := seen[abs]; ok {
			return errors.New("duplicate disk transform output")
		}
		seen[abs] = struct{}{}
		if _, err := os.Stat(abs); err == nil {
			return errors.New("disk transform output already exists")
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
