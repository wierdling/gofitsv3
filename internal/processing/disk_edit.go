package processing

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
)

// DiskEditRecipe contains only small, replayable Edit state. Levels use the
// same 0..255 scale as the in-memory Edit controls; a zero LUT means identity.
type DiskEditRecipe struct {
	Min, Max [3]float64
	Curves   [3][256]byte
	Strength float64
	Radius   float64
}

// DiskEditRenderRequest describes a full-resolution render. Source artifacts
// are never modified; each output is built in a sibling transaction.
type DiskEditRenderRequest struct {
	Source        [3]string
	Output        [3]string
	Width, Height int
	Baseline      models.RgbLevels
	Recipe        DiskEditRecipe
	PreviewMax    int
}

type DiskEditRenderResult struct {
	Preview *image.RGBA
	Bins    [3][256]int
}

// RenderDiskEdit replays baseline decoding, Levels, and Curves row by row.
// It keeps one source/output pair and one row per channel in memory, plus the
// bounded preview. All output artifacts are published only after every row
// succeeds and cancellation has been checked.
func RenderDiskEdit(ctx context.Context, req DiskEditRenderRequest) (DiskEditRenderResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if req.Width <= 0 || req.Height <= 0 {
		return DiskEditRenderResult{}, errors.New("invalid disk Edit dimensions")
	}
	if req.PreviewMax <= 0 {
		req.PreviewMax = 1600
	}
	for i := 0; i < 3; i++ {
		if req.Source[i] == "" || req.Output[i] == "" {
			return DiskEditRenderResult{}, errors.New("missing disk Edit artifact")
		}
	}
	scale := math.Min(1, math.Min(float64(req.PreviewMax)/float64(req.Width), float64(req.PreviewMax)/float64(req.Height)))
	pw, ph := maxEditDim(1, int(math.Round(float64(req.Width)*scale))), maxEditDim(1, int(math.Round(float64(req.Height)*scale)))
	preview := image.NewRGBA(image.Rect(0, 0, pw, ph))
	for i := 0; i < 3; i++ {
		for y := 0; y < ph; y++ {
			for x := 0; x < pw; x++ {
				c := preview.PixOffset(x, y)
				preview.Pix[c+i] = 0
				preview.Pix[c+3] = 255
			}
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
	for i := 0; i < 3; i++ {
		if err := ctx.Err(); err != nil {
			abort()
			return DiskEditRenderResult{}, err
		}
		src, err := fitsio.OpenFloat32ArtifactReadOnly(req.Source[i])
		if err != nil {
			abort()
			return DiskEditRenderResult{}, err
		}
		if src.Width != req.Width || src.Height != req.Height {
			_ = src.Close()
			abort()
			return DiskEditRenderResult{}, fmt.Errorf("source %d dimensions mismatch", i)
		}
		tx, err := fitsio.BeginFloat32ArtifactTransaction(req.Output[i], req.Width, req.Height)
		if err != nil {
			_ = src.Close()
			abort()
			return DiskEditRenderResult{}, err
		}
		txs[i] = tx
		if req.Recipe.Strength > 0 && req.Recipe.Radius > 0 {
			if err := renderDiskEditSharpenChannel(ctx, src, tx.Artifact(), req, i, scale, preview); err != nil {
				_ = src.Close()
				abort()
				return DiskEditRenderResult{}, err
			}
			_ = src.Close()
			continue
		}
		row, out := make([]float32, req.Width), make([]float32, req.Width)
		for y := 0; y < req.Height; y++ {
			if err := ctx.Err(); err != nil {
				_ = src.Close()
				abort()
				return DiskEditRenderResult{}, err
			}
			if err := src.ReadRow(y, row); err != nil {
				_ = src.Close()
				abort()
				return DiskEditRenderResult{}, err
			}
			for x, v := range row {
				b := editBaselineByte(v, req.Baseline.Min[i], req.Baseline.Max[i])
				b = editLevelByte(b, req.Recipe.Min[i], req.Recipe.Max[i])
				if lut := req.Recipe.Curves[i]; !isIdentityLUT(lut) {
					b = lut[b]
				}
				out[x] = float32(b) / 255
			}
			// Target-driven sampling: each preview coordinate chooses its source
			// coordinate, so every preview pixel is populated exactly once even
			// for non-integer downscale ratios.
			py := int(math.Ceil(float64(y) * scale))
			if py >= 0 && py < ph && int(float64(py)/scale) == y {
				for px := 0; px < pw; px++ {
					sx := minEditIndex(req.Width-1, int(float64(px)/scale))
					preview.SetRGBA(px, py, setPreviewChannel(preview.RGBAAt(px, py), i, uint8(out[sx]*255+0.5)))
				}
			}
			if err := tx.Artifact().WriteRow(y, out); err != nil {
				_ = src.Close()
				abort()
				return DiskEditRenderResult{}, err
			}
		}
		_ = src.Close()
	}
	// Fill histogram and preview from the already-rendered bounded image. The
	// preview is intentionally the only retained image-sized object.
	var result DiskEditRenderResult
	result.Preview = preview
	for y := 0; y < ph; y++ {
		for x := 0; x < pw; x++ {
			p := preview.RGBAAt(x, y)
			result.Bins[0][p.R], result.Bins[1][p.G], result.Bins[2][p.B] = result.Bins[0][p.R]+1, result.Bins[1][p.G]+1, result.Bins[2][p.B]+1
		}
	}
	for _, tx := range txs {
		if err := tx.Artifact().Sync(); err != nil {
			abort()
			return DiskEditRenderResult{}, err
		}
	}
	for _, tx := range txs {
		if err := tx.Commit(); err != nil {
			abort()
			return DiskEditRenderResult{}, err
		}
	}
	return result, nil
}

// renderDiskEditSharpenChannel applies the same separable, rounded Gaussian
// unsharp mask as SharpenRGBA, but only keeps one bounded band of rows. The
// halo rows are read to compute each tile and only the tile interior is
// written, so adjacent tiles cannot produce seams.
func renderDiskEditSharpenChannel(ctx context.Context, src *fitsio.Float32Artifact, dst *fitsio.Float32Artifact, req DiskEditRenderRequest, channel int, scale float64, preview *image.RGBA) error {
	kernel, sum := gaussianKernel(req.Recipe.Radius)
	halo := len(kernel) / 2
	const tileHeight = 256
	row := make([]float32, req.Width)
	out := make([]float32, req.Width)
	for y0 := 0; y0 < req.Height; y0 += tileHeight {
		y1 := y0 + tileHeight
		if y1 > req.Height {
			y1 = req.Height
		}
		readY0, readY1 := y0-halo, y1+halo
		if readY0 < 0 {
			readY0 = 0
		}
		if readY1 > req.Height {
			readY1 = req.Height
		}
		rows := readY1 - readY0
		values := make([]uint8, rows*req.Width)
		horizontal := make([]uint8, rows*req.Width)
		for ry := 0; ry < rows; ry++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := src.ReadRow(readY0+ry, row); err != nil {
				return err
			}
			for x, v := range row {
				b := editBaselineByte(v, req.Baseline.Min[channel], req.Baseline.Max[channel])
				b = editLevelByte(b, req.Recipe.Min[channel], req.Recipe.Max[channel])
				if lut := req.Recipe.Curves[channel]; !isIdentityLUT(lut) {
					b = lut[b]
				}
				values[ry*req.Width+x] = b
			}
		}
		for ry := 0; ry < rows; ry++ {
			for x := 0; x < req.Width; x++ {
				var acc float64
				for ki, kv := range kernel {
					sx := x + ki - halo
					if sx < 0 {
						sx = 0
					} else if sx >= req.Width {
						sx = req.Width - 1
					}
					acc += float64(values[ry*req.Width+sx]) * kv
				}
				horizontal[ry*req.Width+x] = clampEditByte(acc / sum)
			}
		}
		for y := y0; y < y1; y++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			ry := y - readY0
			for x := 0; x < req.Width; x++ {
				var acc float64
				for ki, kv := range kernel {
					sy := ry + ki - halo
					if sy < 0 {
						sy = 0
					} else if sy >= rows {
						sy = rows - 1
					}
					acc += float64(horizontal[sy*req.Width+x]) * kv
				}
				blur := float64(clampEditByte(acc / sum))
				orig := float64(values[ry*req.Width+x])
				out[x] = float32(clampEditByte(orig+req.Recipe.Strength*(orig-blur))) / 255
			}
			if py := int(math.Ceil(float64(y) * scale)); py >= 0 && py < preview.Bounds().Dy() && int(float64(py)/scale) == y {
				for px := 0; px < preview.Bounds().Dx(); px++ {
					sx := minEditIndex(req.Width-1, int(float64(px)/scale))
					preview.SetRGBA(px, py, setPreviewChannel(preview.RGBAAt(px, py), channel, uint8(out[sx]*255+0.5)))
				}
			}
			if err := dst.WriteRow(y, out); err != nil {
				return err
			}
		}
	}
	return nil
}

func editBaselineByte(v float32, min, max float64) uint8 {
	if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) || max <= min {
		return 0
	}
	x := (float64(v)*255 - min) * 255 / (max - min)
	return clampEditByte(x)
}
func editLevelByte(v uint8, min, max float64) uint8 {
	if max <= min {
		return 0
	}
	return clampEditByte((float64(v) - min) * 255 / (max - min))
}
func clampEditByte(v float64) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return uint8(v + 0.5)
}
func isIdentityLUT(l [256]byte) bool {
	zero := true
	for i, v := range l {
		if v != 0 {
			zero = false
		}
		if v != byte(i) {
			if !zero {
				return false
			}
		}
	}
	return true // an omitted (all-zero) LUT is the identity recipe
}
func setPreviewChannel(c color.RGBA, ch int, v uint8) color.RGBA {
	if ch == 0 {
		c.R = v
	}
	if ch == 1 {
		c.G = v
	}
	if ch == 2 {
		c.B = v
	}
	c.A = 255
	return c
}
func maxEditDim(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func minEditIndex(a, b int) int {
	if a < b {
		return a
	}
	return b
}
