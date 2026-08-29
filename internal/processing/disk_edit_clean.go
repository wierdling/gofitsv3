package processing

import (
	"context"
	"fmt"
	"image"
	"os"
	"path/filepath"

	"gofitsv3/internal/fitsio"
)

// DiskColorSpeckCleanRequest describes a bounded RGB artifact clean. Source
// artifacts are never modified; outputs are committed only after all tiles
// have been written successfully.
type DiskColorSpeckCleanRequest struct {
	Source        [3]string
	Output        [3]string
	Width, Height int
	Config        ColorSpeckCleanConfig
	PreviewMax    int
}

type DiskColorSpeckCleanResult struct {
	Preview  *image.RGBA
	Repaired int
}

// diskColorSpeckCleanCommitHook is test-only fault injection for the grouped
// publication boundary. It remains nil in production.
var diskColorSpeckCleanCommitHook func(int) error

// CleanColorSpecksDisk applies the same speck classifier as
// CleanColorSpecksRGBA to bounded overlapping tiles. The overlap is larger
// than the largest repairable component and its local ring, so a component
// crossing a tile boundary is classified from the same neighbourhood. Only
// each tile's interior is copied to the output.
func CleanColorSpecksDisk(ctx context.Context, req DiskColorSpeckCleanRequest) (DiskColorSpeckCleanResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if req.Width <= 0 || req.Height <= 0 {
		return DiskColorSpeckCleanResult{}, fmt.Errorf("invalid disk clean dimensions")
	}
	if req.PreviewMax <= 0 {
		req.PreviewMax = 1600
	}
	cfg := normalizeColorSpeckCleanConfig(req.Config)
	for i := range req.Source {
		if req.Source[i] == "" || req.Output[i] == "" {
			return DiskColorSpeckCleanResult{}, fmt.Errorf("missing disk clean artifact %d", i)
		}
	}
	for i := range req.Source {
		srcPath, _ := filepath.Abs(filepath.Clean(req.Source[i]))
		for j := range req.Output {
			outPath, _ := filepath.Abs(filepath.Clean(req.Output[j]))
			if srcPath == outPath {
				return DiskColorSpeckCleanResult{}, fmt.Errorf("disk clean source/output overlap")
			}
		}
	}
	for i := 0; i < len(req.Output); i++ {
		for j := i + 1; j < len(req.Output); j++ {
			a, _ := filepath.Abs(filepath.Clean(req.Output[i]))
			b, _ := filepath.Abs(filepath.Clean(req.Output[j]))
			if a == b {
				return DiskColorSpeckCleanResult{}, fmt.Errorf("duplicate disk clean outputs")
			}
		}
		if _, err := os.Stat(req.Output[i]); err == nil {
			return DiskColorSpeckCleanResult{}, fmt.Errorf("disk clean output already exists")
		} else if !os.IsNotExist(err) {
			return DiskColorSpeckCleanResult{}, fmt.Errorf("inspect disk clean output: %w", err)
		}
	}
	var src [3]*fitsio.Float32Artifact
	var tx [3]*fitsio.Float32ArtifactTransaction
	abort := func() {
		for i := range tx {
			if tx[i] != nil {
				_ = tx[i].Abort()
			}
		}
		for i := range src {
			if src[i] != nil {
				_ = src[i].Close()
			}
		}
	}
	for i := 0; i < 3; i++ {
		var err error
		src[i], err = fitsio.OpenFloat32ArtifactReadOnly(req.Source[i])
		if err != nil {
			abort()
			return DiskColorSpeckCleanResult{}, err
		}
		if src[i].Width != req.Width || src[i].Height != req.Height {
			abort()
			return DiskColorSpeckCleanResult{}, fmt.Errorf("source %d dimensions mismatch", i)
		}
		tx[i], err = fitsio.BeginFloat32ArtifactTransaction(req.Output[i], req.Width, req.Height)
		if err != nil {
			abort()
			return DiskColorSpeckCleanResult{}, err
		}
	}
	// Keep both tile dimensions bounded. The halo is deliberately generous for
	// the default 25-pixel component limit while remaining independent of image
	// height.
	// A complete scanline is retained so artifact writes remain sequential;
	// height is still tiled and therefore memory stays independent of image
	// height. The halo is the bounded overlap needed for seam safety.
	const tileWidth, tileHeight = 256, 256
	halo := cfg.MaxBlobPixels + cfg.RingRadius + 2
	var preview *image.RGBA
	repairedTotal := 0
	scale := 1.0
	if req.Width > req.PreviewMax || req.Height > req.PreviewMax {
		if float64(req.PreviewMax)/float64(req.Width) < float64(req.PreviewMax)/float64(req.Height) {
			scale = float64(req.PreviewMax) / float64(req.Width)
		} else {
			scale = float64(req.PreviewMax) / float64(req.Height)
		}
	}
	pw, ph := maxEditDim(1, int(float64(req.Width)*scale+0.5)), maxEditDim(1, int(float64(req.Height)*scale+0.5))
	preview = image.NewRGBA(image.Rect(0, 0, pw, ph))
	var row [3][]float32
	for y0 := 0; y0 < req.Height; y0 += tileHeight {
		y1 := y0 + tileHeight
		if y1 > req.Height {
			y1 = req.Height
		}
		for x0 := 0; x0 < req.Width; x0 += tileWidth {
			if err := ctx.Err(); err != nil {
				abort()
				return DiskColorSpeckCleanResult{}, err
			}
			x1 := x0 + tileWidth
			if x1 > req.Width {
				x1 = req.Width
			}
			sx0, sy0 := x0-halo, y0-halo
			sx1, sy1 := x1+halo, y1+halo
			if sx0 < 0 {
				sx0 = 0
			}
			if sy0 < 0 {
				sy0 = 0
			}
			if sx1 > req.Width {
				sx1 = req.Width
			}
			if sy1 > req.Height {
				sy1 = req.Height
			}
			tw, th := sx1-sx0, sy1-sy0
			for c := range row {
				row[c] = make([]float32, tw)
			}
			tile := image.NewRGBA(image.Rect(0, 0, tw, th))
			for y := sy0; y < sy1; y++ {
				for c := 0; c < 3; c++ {
					if err := src[c].ReadRange(y, sx0, sx1, row[c]); err != nil {
						abort()
						return DiskColorSpeckCleanResult{}, err
					}
				}
				for x := sx0; x < sx1; x++ {
					p := tile.PixOffset(x-sx0, y-sy0)
					for c := 0; c < 3; c++ {
						tile.Pix[p+c] = clampEditByte(float64(row[c][x-sx0]) * 255)
					}
					tile.Pix[p+3] = 255
				}
			}
			cleaned, _ := CleanColorSpecksRGBA(tile, cfg)
			changed := make([]bool, tw*th)
			for y := y0; y < y1; y++ {
				for c := 0; c < 3; c++ {
					if err := src[c].ReadRange(y, sx0, sx1, row[c]); err != nil {
						abort()
						return DiskColorSpeckCleanResult{}, err
					}
					out := append([]float32(nil), row[c]...)
					for x := x0; x < x1; x++ {
						v := cleaned.RGBAAt(x-sx0, y-sy0)
						orig := clampEditByte(float64(row[c][x-sx0]) * 255)
						got := []uint8{v.R, v.G, v.B}[c]
						if got != orig {
							out[x-sx0] = float32(got) / 255
							changed[(y-sy0)*tw+x-sx0] = true
						}
					}
					if err := tx[c].Artifact().WriteRange(y, x0, x1, out[x0-sx0:x1-sx0]); err != nil {
						abort()
						return DiskColorSpeckCleanResult{}, err
					}
					if c == 2 {
						for x := x0; x < x1; x++ {
							if changed[(y-sy0)*tw+x-sx0] {
								repairedTotal++
							}
						}
					}
				}
			}
		}
	}
	// Repaired count and preview are derived from committed-sized tiles by a
	// bounded second pass; this also avoids retaining tile images.
	for i := range src {
		_ = src[i].Close()
	}
	for i := range tx {
		if err := tx[i].Artifact().Sync(); err != nil {
			abort()
			return DiskColorSpeckCleanResult{}, err
		}
	}
	for i := range tx {
		if diskColorSpeckCleanCommitHook != nil {
			if err := diskColorSpeckCleanCommitHook(i); err != nil {
				for _, p := range req.Output {
					_ = os.Remove(p)
				}
				abort()
				return DiskColorSpeckCleanResult{}, err
			}
		}
		if err := tx[i].Commit(); err != nil {
			for _, p := range req.Output {
				_ = os.Remove(p)
			}
			abort()
			return DiskColorSpeckCleanResult{}, err
		}
	}
	// Re-open outputs to build the bounded preview and exact repaired count.
	result := DiskColorSpeckCleanResult{Preview: preview, Repaired: repairedTotal}
	for c := 0; c < 3; c++ {
		a, err := fitsio.OpenFloat32ArtifactReadOnly(req.Output[c])
		if err != nil {
			return DiskColorSpeckCleanResult{}, err
		}
		row[c] = make([]float32, tileWidth)
		for y := 0; y < req.Height; y++ {
			for x0 := 0; x0 < req.Width; x0 += tileWidth {
				x1 := x0 + tileWidth
				if x1 > req.Width {
					x1 = req.Width
				}
				if err := a.ReadRange(y, x0, x1, row[c][:x1-x0]); err != nil {
					_ = a.Close()
					return DiskColorSpeckCleanResult{}, err
				}
				py := int(float64(y) * scale)
				if py >= ph {
					py = ph - 1
				}
				for x := x0; x < x1; x++ {
					px := int(float64(x) * scale)
					if px >= pw {
						px = pw - 1
					}
					p := preview.RGBAAt(px, py)
					preview.SetRGBA(px, py, setPreviewChannel(p, c, clampEditByte(float64(row[c][x-x0])*255)))
				}
			}
		}
		_ = a.Close()
	}
	return result, nil
}
