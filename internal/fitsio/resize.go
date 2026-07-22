package fitsio

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ResizeOutputPath returns the non-destructive output name for a reduced FITS.
func ResizeOutputPath(path string, factor int) string {
	ext := filepath.Ext(path)
	return strings.TrimSuffix(path, ext) + "_x" + strconv.Itoa(factor) + ".fits"
}

// ValidateResizeInputs verifies that files share a raster size and can be
// reduced by factor. Partial blocks on the right and bottom edges are discarded.
func ValidateResizeInputs(paths []string, factor int) (int, int, error) {
	if !validReductionFactor(factor) {
		return 0, 0, fmt.Errorf("reduction factor must be a power of two greater than one")
	}
	if len(paths) == 0 {
		return 0, 0, fmt.Errorf("select at least one FITS file")
	}
	width, height := 0, 0
	var expectedLayout []resizePlaneLayout
	for _, path := range paths {
		file, err := LoadFileMetadata(path)
		if err != nil {
			return 0, 0, fmt.Errorf("read %s: %w", filepath.Base(path), err)
		}
		w, h := firstImageSize(file)
		if w == 0 || h == 0 {
			return 0, 0, fmt.Errorf("%s contains no 2-D image", filepath.Base(path))
		}
		if width == 0 {
			width, height = w, h
			expectedLayout, err = resizeLayout(file, factor)
			if err != nil {
				return 0, 0, fmt.Errorf("%s: %w", filepath.Base(path), err)
			}
		} else if w != width || h != height {
			return 0, 0, fmt.Errorf("%s is %dx%d, expected %dx%d", filepath.Base(path), w, h, width, height)
		} else if layout, layoutErr := resizeLayout(file, factor); layoutErr != nil {
			return 0, 0, fmt.Errorf("%s: %w", filepath.Base(path), layoutErr)
		} else if !sameResizeLayout(expectedLayout, layout) {
			return 0, 0, fmt.Errorf("%s has a different image-plane layout", filepath.Base(path))
		}
	}
	if width/factor < 1 || height/factor < 1 {
		return 0, 0, fmt.Errorf("x%d would reduce %dx%d below one pixel", factor, width, height)
	}
	return width, height, nil
}

type resizePlaneLayout struct {
	extName       string
	width, height int
}

func resizeLayout(file *File, factor int) ([]resizePlaneLayout, error) {
	layout := make([]resizePlaneLayout, 0, len(file.HDUs))
	for i, hdu := range file.HDUs {
		if hdu.Data.Width == 0 || hdu.Data.Height == 0 {
			if i == 0 {
				continue
			} // metadata-only primary HDU
			return nil, fmt.Errorf("unsupported non-image extension %q", hdu.ExtName)
		}
		if hdu.Data.Width/factor < 1 || hdu.Data.Height/factor < 1 {
			return nil, fmt.Errorf("plane %q would reduce below one pixel at x%d", hdu.ExtName, factor)
		}
		mode := strings.ToUpper(strings.TrimSpace(hdu.ExtName))
		if (strings.HasPrefix(mode, "DQ") || strings.HasPrefix(mode, "CTX")) && parseInt(hdu.Header.Cards["BITPIX"]) != 32 {
			return nil, fmt.Errorf("mask plane %q uses BITPIX=%s; only signed 32-bit masks can be preserved exactly", hdu.ExtName, hdu.Header.Cards["BITPIX"])
		}
		layout = append(layout, resizePlaneLayout{hdu.ExtName, hdu.Data.Width, hdu.Data.Height})
	}
	return layout, nil
}

func sameResizeLayout(a, b []resizePlaneLayout) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func validReductionFactor(factor int) bool { return factor > 1 && factor&(factor-1) == 0 }

func firstImageSize(file *File) (int, int) {
	for _, hdu := range file.HDUs {
		if hdu.Data.Width > 0 && hdu.Data.Height > 0 {
			return hdu.Data.Width, hdu.Data.Height
		}
	}
	return 0, 0
}

// ResizeFITSFiles reduces every 2-D image plane in paths. Existing outputs are
// rejected before work starts, so a batch never overwrites sources or outputs.
func ResizeFITSFiles(ctx context.Context, paths []string, factor int, progress func(done, total int)) ([]string, error) {
	if _, _, err := ValidateResizeInputs(paths, factor); err != nil {
		return nil, err
	}
	outputs := make([]string, len(paths))
	for i, path := range paths {
		outputs[i] = ResizeOutputPath(path, factor)
		if samePath(path, outputs[i]) {
			return nil, fmt.Errorf("refusing to overwrite source %s", filepath.Base(path))
		}
		if _, err := os.Stat(outputs[i]); err == nil {
			return nil, fmt.Errorf("output already exists: %s", outputs[i])
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("check output %s: %w", outputs[i], err)
		}
	}
	for i, path := range paths {
		if err := ctx.Err(); err != nil {
			return outputs[:i], err
		}
		if err := resizeFITSFile(ctx, path, outputs[i], factor); err != nil {
			return outputs[:i], err
		}
		if progress != nil {
			progress(i+1, len(paths))
		}
	}
	return outputs, nil
}

func samePath(a, b string) bool {
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	return errA == nil && errB == nil && strings.EqualFold(aa, bb)
}

func resizeFITSFile(ctx context.Context, input, output string, factor int) error {
	file, err := LoadFile(input)
	if err != nil {
		return fmt.Errorf("read %s: %w", filepath.Base(input), err)
	}
	if len(file.HDUs) == 0 {
		return fmt.Errorf("%s contains no HDUs", filepath.Base(input))
	}
	resized := make([]HDU, len(file.HDUs))
	for i, hdu := range file.HDUs {
		if err := ctx.Err(); err != nil {
			return err
		}
		resized[i] = hdu
		if hdu.Data.Width == 0 || hdu.Data.Height == 0 {
			continue
		}
		if hdu.Data.Width/factor < 1 || hdu.Data.Height/factor < 1 {
			return fmt.Errorf("%s plane %q would reduce below one pixel at x%d", filepath.Base(input), hdu.ExtName, factor)
		}
		data, err := reducePlane(ctx, hdu.Data, hdu.ExtName, factor)
		if err != nil {
			return err
		}
		resized[i].Data = data
		resized[i].Header = resizeWCS(hdu.Header, factor)
	}
	primary := resized[0]
	extensions := make([]ImageExtension, 0, len(resized)-1)
	for _, hdu := range resized[1:] {
		if hdu.Data.Width == 0 || hdu.Data.Height == 0 {
			return fmt.Errorf("%s has unsupported non-image extension %q", filepath.Base(input), hdu.ExtName)
		}
		extensions = append(extensions, ImageExtension{ExtName: hdu.ExtName, Header: hdu.Header, Data: hdu.Data})
	}
	temp, err := os.CreateTemp(filepath.Dir(output), "."+filepath.Base(output)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary output for %s: %w", filepath.Base(output), err)
	}
	tempPath := temp.Name()
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	defer os.Remove(tempPath)
	if err := WriteFloat32ImageWithExtensions(tempPath, primary.Header, primary.Data, extensions...); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(output), err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Link atomically creates the final name and fails if another process won
	// the race. Unlike Rename, it never replaces an existing output.
	if err := os.Link(tempPath, output); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("output already exists: %s", output)
		}
		return fmt.Errorf("publish %s: %w", filepath.Base(output), err)
	}
	return nil
}

func reducePlane(ctx context.Context, data ImageData, extName string, factor int) (ImageData, error) {
	out := ImageData{Width: data.Width / factor, Height: data.Height / factor, Pixels: make([]float32, (data.Width/factor)*(data.Height/factor))}
	mode := strings.ToUpper(strings.TrimSpace(extName))
	mask := strings.HasPrefix(mode, "DQ") || strings.HasPrefix(mode, "CTX")
	if mask {
		if len(data.Int32Pixels) != data.Width*data.Height {
			return ImageData{}, fmt.Errorf("mask plane %q has no exact 32-bit samples", extName)
		}
		out.Int32Pixels = make([]int32, len(out.Pixels))
	}
	for y := 0; y < out.Height; y++ {
		if err := ctx.Err(); err != nil {
			return ImageData{}, err
		}
		for x := 0; x < out.Width; x++ {
			start := y*factor*data.Width + x*factor
			switch {
			case strings.HasPrefix(mode, "WHT"):
				out.Pixels[y*out.Width+x] = blockSum(data.Pixels, data.Width, start, factor)
			case strings.HasPrefix(mode, "ERR"):
				out.Pixels[y*out.Width+x] = blockError(data.Pixels, data.Width, start, factor)
			case mask:
				value := blockOr(data.Int32Pixels, data.Width, start, factor)
				out.Int32Pixels[y*out.Width+x] = value
				out.Pixels[y*out.Width+x] = float32(value)
			default:
				out.Pixels[y*out.Width+x] = blockMean(data.Pixels, data.Width, start, factor)
			}
		}
	}
	return out, nil
}

func blockMean(p []float32, width, start, factor int) float32 {
	var sum float64
	count := 0
	for y := 0; y < factor; y++ {
		for x := 0; x < factor; x++ {
			v := p[start+y*width+x]
			if !math.IsNaN(float64(v)) {
				sum += float64(v)
				count++
			}
		}
	}
	if count == 0 {
		return float32(math.NaN())
	}
	return float32(sum / float64(count))
}

func blockSum(p []float32, width, start, factor int) float32 {
	var sum float64
	for y := 0; y < factor; y++ {
		for x := 0; x < factor; x++ {
			v := p[start+y*width+x]
			if !math.IsNaN(float64(v)) {
				sum += float64(v)
			}
		}
	}
	return float32(sum)
}

func blockError(p []float32, width, start, factor int) float32 {
	var sum float64
	count := 0
	for y := 0; y < factor; y++ {
		for x := 0; x < factor; x++ {
			v := p[start+y*width+x]
			if !math.IsNaN(float64(v)) {
				sum += float64(v) * float64(v)
				count++
			}
		}
	}
	if count == 0 {
		return float32(math.NaN())
	}
	return float32(math.Sqrt(sum) / float64(count))
}

func blockOr(p []int32, width, start, factor int) int32 {
	var value int32
	for y := 0; y < factor; y++ {
		for x := 0; x < factor; x++ {
			value |= p[start+y*width+x]
		}
	}
	return value
}

func resizeWCS(header Header, factor int) Header {
	resized := CloneHeader(header)
	for _, key := range []string{"CRPIX1", "CRPIX2"} {
		if value, ok := HeaderFloat(resized, key); ok {
			resized.Cards[key] = strconv.FormatFloat((value+float64(factor-1)/2)/float64(factor), 'g', -1, 64)
		}
	}
	for _, key := range []string{"CD1_1", "CD1_2", "CD2_1", "CD2_2", "CDELT1", "CDELT2"} {
		if value, ok := HeaderFloat(resized, key); ok {
			resized.Cards[key] = strconv.FormatFloat(value*float64(factor), 'g', -1, 64)
		}
	}
	return resized
}
