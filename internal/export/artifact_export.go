package export

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/stretch"
)

// FromFloat32Artifacts exports three disk-backed float32 artifacts in RGB
// order. Only one row per source is retained while the encoder runs.
func FromFloat32Artifacts(ctx context.Context, path string, artifacts [3]string, width, height int, format Format, opt Options) error {
	return FromFloat32ArtifactsWithLevels(ctx, path, artifacts, width, height, format, opt, nil)
}

// FromFloat32Artifact exports one disk-backed channel as a grayscale image.
// The stretch is evaluated row-by-row; HistEq performs a bounded histogram
// prepass before encoding.
func FromFloat32Artifact(ctx context.Context, path, artifact string, width, height int, format Format, opt Options, meta *models.LoadedImage) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if artifact == "" || width <= 0 || height <= 0 {
		return errors.New("invalid artifact dimensions")
	}
	img, err := newGrayArtifactImage(ctx, artifact, width, height, format == PNG && opt.BitDepth == 16, meta)
	if err != nil {
		return err
	}
	defer img.close()
	tmp, err := os.CreateTemp(filepath.Dir(path), ".export-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	defer os.Remove(tmpPath)
	if err := saveImage(tmpPath, img, format, opt); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	backup := path + ".export-backup"
	_ = os.Remove(backup)
	hadOld := false
	if _, err := os.Stat(path); err == nil {
		if err := os.Rename(path, backup); err != nil {
			return err
		}
		hadOld = true
	}
	if err := os.Rename(tmpPath, path); err != nil {
		if hadOld {
			_ = os.Rename(backup, path)
		}
		return err
	}
	if hadOld {
		_ = os.Remove(backup)
	}
	return nil
}

type grayArtifactImage struct {
	ctx    context.Context
	art    *fitsio.Float32Artifact
	bounds image.Rectangle
	wide   bool
	meta   *models.LoadedImage
	cdf    []float32
	row    int
	vals   []float32
}

func newGrayArtifactImage(ctx context.Context, path string, width, height int, wide bool, meta *models.LoadedImage) (*grayArtifactImage, error) {
	a, err := fitsio.OpenFloat32ArtifactReadOnly(path)
	if err != nil {
		return nil, err
	}
	if a.Width != width || a.Height != height {
		_ = a.Close()
		return nil, errors.New("artifact dimensions mismatch")
	}
	g := &grayArtifactImage{ctx: ctx, art: a, bounds: image.Rect(0, 0, width, height), wide: wide, meta: meta, row: -1, vals: make([]float32, width)}
	if meta != nil && meta.Mode == stretch.HistEq {
		g.cdf = make([]float32, 256)
		hist := make([]int, 256)
		row := make([]float32, width)
		for y := 0; y < height; y++ {
			if err := ctx.Err(); err != nil {
				g.close()
				return nil, err
			}
			if err := a.ReadRow(y, row); err != nil {
				g.close()
				return nil, err
			}
			for _, v := range row {
				// Match stretch.Apply's binning: truncate the normalized value
				// after scaling, rather than clamping the scaled value to [0,1].
				idx := int(clamp01(float64(stretchScalar(v, *meta))) * 255)
				hist[idx]++
			}
		}
		total, sum := 0, 0
		for _, n := range hist {
			total += n
		}
		for i, n := range hist {
			sum += n
			if total > 0 {
				g.cdf[i] = float32(float64(sum) / float64(total))
			}
		}
	}
	return g, nil
}
func (g *grayArtifactImage) close() {
	if g.art != nil {
		_ = g.art.Close()
		g.art = nil
	}
}
func (g *grayArtifactImage) ColorModel() color.Model {
	if g.wide {
		return color.Gray16Model
	}
	return color.GrayModel
}
func (g *grayArtifactImage) Bounds() image.Rectangle { return g.bounds }
func (g *grayArtifactImage) At(x, y int) color.Color {
	if x < 0 || y < 0 || x >= g.bounds.Dx() || y >= g.bounds.Dy() || g.ctx.Err() != nil {
		return color.Gray16{}
	}
	if g.row != y {
		if g.art.ReadRow(y, g.vals) != nil {
			return color.Gray16{}
		}
		g.row = y
	}
	v := stretchScalar(g.vals[x], func() models.LoadedImage {
		if g.meta != nil {
			return *g.meta
		}
		return models.LoadedImage{}
	}())
	if len(g.cdf) == 256 {
		v = g.cdf[int(clamp01(float64(v))*255)]
	}
	if g.wide {
		q := clampToUint16(v)
		return color.Gray16{Y: q}
	}
	// The 8-bit path must use the same floor(v*255) quantization as the
	// normal in-memory export path. Converting a 16-bit sample with >> 8
	// instead produces subtly different values (notably for Log stretch).
	return color.Gray{Y: uint8(clamp01(float64(v)) * 255)}
}

func stretchScalar(v float32, img models.LoadedImage) float32 {
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

// FromFloat32ArtifactsWithLevels is the RGB-level aware variant used by
// Compose. Levels are in the persisted 0..255 scale.
func FromFloat32ArtifactsWithLevels(ctx context.Context, path string, artifacts [3]string, width, height int, format Format, opt Options, levels *models.RgbLevels) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if width <= 0 || height <= 0 {
		return errors.New("invalid artifact dimensions")
	}
	for _, p := range artifacts {
		if p == "" {
			return errors.New("missing artifact path")
		}
	}
	img, err := newArtifactImage(ctx, artifacts, width, height, format == PNG && opt.BitDepth == 16, levels)
	if err != nil {
		return err
	}
	defer img.close()
	if err := ctx.Err(); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".export-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	defer os.Remove(tmpPath)
	if err := saveImage(tmpPath, img, format, opt); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	backup := path + ".export-backup"
	_ = os.Remove(backup)
	hadOld := false
	if _, err := os.Stat(path); err == nil {
		if err := os.Rename(path, backup); err != nil {
			return err
		}
		hadOld = true
	}
	if err := os.Rename(tmpPath, path); err != nil {
		if hadOld {
			_ = os.Rename(backup, path)
		}
		return err
	}
	if hadOld {
		_ = os.Remove(backup)
	}
	return nil
}

type artifactImage struct {
	ctx    context.Context
	arts   [3]*fitsio.Float32Artifact
	bounds image.Rectangle
	wide   bool
	levels *models.RgbLevels
	row    int
	vals   [3][]float32
}

func newArtifactImage(ctx context.Context, paths [3]string, width, height int, wide bool, levels *models.RgbLevels) (*artifactImage, error) {
	a := &artifactImage{ctx: ctx, bounds: image.Rect(0, 0, width, height), wide: wide, row: -1, levels: levels}
	for i, p := range paths {
		f, err := fitsio.OpenFloat32ArtifactReadOnly(p)
		if err != nil {
			a.close()
			return nil, err
		}
		if f.Width != width || f.Height != height {
			_ = f.Close()
			a.close()
			return nil, fmt.Errorf("artifact dimensions mismatch")
		}
		a.arts[i] = f
		a.vals[i] = make([]float32, width)
	}
	return a, nil
}
func (a *artifactImage) close() {
	for i := range a.arts {
		if a.arts[i] != nil {
			_ = a.arts[i].Close()
			a.arts[i] = nil
		}
	}
}
func (a *artifactImage) ColorModel() color.Model {
	if a.wide {
		return color.RGBA64Model
	}
	return color.NRGBAModel
}
func (a *artifactImage) Bounds() image.Rectangle { return a.bounds }
func (a *artifactImage) At(x, y int) color.Color {
	if x < 0 || y < 0 || x >= a.bounds.Dx() || y >= a.bounds.Dy() || a.ctx.Err() != nil {
		return color.NRGBA{}
	}
	if a.row != y {
		for i := range a.arts {
			if a.arts[i].ReadRow(y, a.vals[i]) != nil {
				return color.NRGBA{}
			}
		}
		a.row = y
	}
	r := clampToUint16(a.vals[0][x])
	g := clampToUint16(a.vals[1][x])
	b := clampToUint16(a.vals[2][x])
	if a.levels != nil {
		r = applyLevel(r, a.levels.Min[0], a.levels.Max[0])
		g = applyLevel(g, a.levels.Min[1], a.levels.Max[1])
		b = applyLevel(b, a.levels.Min[2], a.levels.Max[2])
	}
	if a.wide {
		return color.RGBA64{R: r, G: g, B: b, A: 0xffff}
	}
	return color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: 0xff}
}

func applyLevel(v uint16, min, max float64) uint16 {
	min16, max16 := uint16(min*257), uint16(max*257)
	if max16 <= min16 {
		return 0
	}
	if v <= min16 {
		return 0
	}
	if v >= max16 {
		return 0xffff
	}
	return uint16((uint32(v) - uint32(min16)) * 65535 / uint32(max16-min16))
}

func clamp01(v float64) float64 {
	if math.IsNaN(v) || v <= 0 {
		return 0
	}
	if v >= 1 {
		return 1
	}
	return v
}
