package mosaic

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gofitsv3/internal/fitsio"
)

type MaskOperationMode string

const (
	MaskOperationAdd   MaskOperationMode = "add"
	MaskOperationErase MaskOperationMode = "erase"
)

type RasterMaskOperation struct {
	Mode   MaskOperationMode
	Width  int
	Height int
	Pixels []bool
}

func NewZeroArtifactMask(width, height int) ([]bool, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("artifact mask dimensions must be positive: %dx%d", width, height)
	}
	return make([]bool, width*height), nil
}

func ApplyRasterMaskOperations(width, height int, ops []RasterMaskOperation) ([]bool, error) {
	mask, err := NewZeroArtifactMask(width, height)
	if err != nil {
		return nil, err
	}
	for i, op := range ops {
		if op.Width != width || op.Height != height || len(op.Pixels) != width*height {
			return nil, fmt.Errorf("mask operation %d dimensions mismatch: operation %dx%d len=%d target %dx%d", i, op.Width, op.Height, len(op.Pixels), width, height)
		}
		switch op.Mode {
		case MaskOperationAdd:
			for j, v := range op.Pixels {
				if v {
					mask[j] = true
				}
			}
		case MaskOperationErase:
			for j, v := range op.Pixels {
				if v {
					mask[j] = false
				}
			}
		default:
			return nil, fmt.Errorf("mask operation %d has unknown mode %q", i, op.Mode)
		}
	}
	return mask, nil
}

func DilateArtifactMask(mask []bool, width, height, radius int) ([]bool, error) {
	if err := validateArtifactMaskDimensions(mask, width, height); err != nil {
		return nil, err
	}
	if radius <= 0 {
		return append([]bool(nil), mask...), nil
	}
	out := make([]bool, len(mask))
	r2 := radius * radius
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if !mask[y*width+x] {
				continue
			}
			for dy := -radius; dy <= radius; dy++ {
				yy := y + dy
				if yy < 0 || yy >= height {
					continue
				}
				for dx := -radius; dx <= radius; dx++ {
					if dx*dx+dy*dy > r2 {
						continue
					}
					xx := x + dx
					if xx < 0 || xx >= width {
						continue
					}
					out[yy*width+xx] = true
				}
			}
		}
	}
	return out, nil
}

func ErodeArtifactMask(mask []bool, width, height, radius int) ([]bool, error) {
	if err := validateArtifactMaskDimensions(mask, width, height); err != nil {
		return nil, err
	}
	if radius <= 0 {
		return append([]bool(nil), mask...), nil
	}
	out := make([]bool, len(mask))
	r2 := radius * radius
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			keep := true
			for dy := -radius; dy <= radius && keep; dy++ {
				yy := y + dy
				for dx := -radius; dx <= radius; dx++ {
					if dx*dx+dy*dy > r2 {
						continue
					}
					xx := x + dx
					if xx < 0 || xx >= width || yy < 0 || yy >= height || !mask[yy*width+xx] {
						keep = false
						break
					}
				}
			}
			out[y*width+x] = keep
		}
	}
	return out, nil
}

func MIRIArtifactMaskFilename(input Input) string {
	return miriArtifactMaskName(input)
}

func RowDestripeMaskFilename(input Input) string {
	return rowDestripeMaskName(input)
}

func ExportMIRIArtifactMaskAtomic(input Input, dir string, mask []bool, overwrite bool) (string, error) {
	if input.HDU.Data.Width <= 0 || input.HDU.Data.Height <= 0 {
		return "", fmt.Errorf("cannot export MIRI artifact mask for %s: input dimensions are %dx%d", InputKey(input), input.HDU.Data.Width, input.HDU.Data.Height)
	}
	if err := validateArtifactMaskDimensions(mask, input.HDU.Data.Width, input.HDU.Data.Height); err != nil {
		return "", err
	}
	if !isMIRIFrame(input) {
		return "", fmt.Errorf("cannot export MIRI artifact mask for non-MIRI input %s", InputKey(input))
	}
	if dir == "" {
		return "", errors.New("MIRI artifact mask export directory is blank")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}

	path := filepath.Join(dir, miriArtifactMaskName(input))
	if !overwrite {
		if _, err := os.Stat(path); err == nil {
			return "", fmt.Errorf("MIRI artifact mask already exists: %s", path)
		} else if err != nil && !os.IsNotExist(err) {
			return "", err
		}
	}

	pixels := make([]float32, len(mask))
	for i, v := range mask {
		if v {
			pixels[i] = 1
		}
	}
	header := fitsio.CloneHeader(input.HDU.Header)
	if header.Cards == nil {
		header.Cards = map[string]string{}
	}
	header.Cards["MASKTYPE"] = "'MIRIART'"
	header.Cards["MASKVER"] = "1"
	header.Cards["MASKSRC"] = "'GOFITSV3'"
	if input.SCIExt > 0 {
		header.Cards["SCIEXT"] = fmt.Sprintf("%d", input.SCIExt)
	}
	header.Cards["DATE"] = "'" + time.Now().UTC().Format(time.RFC3339) + "'"

	temp, err := os.CreateTemp(dir, "."+miriArtifactMaskName(input)+".tmp-*")
	if err != nil {
		return "", err
	}
	tempPath := temp.Name()
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return "", err
	}
	_ = os.Remove(tempPath)
	if err := fitsio.WriteFloat32Image(tempPath, header, fitsio.ImageData{
		Width:  input.HDU.Data.Width,
		Height: input.HDU.Data.Height,
		Pixels: pixels,
	}); err != nil {
		_ = os.Remove(tempPath)
		return "", err
	}
	if err := replaceFileAtomically(tempPath, path, overwrite); err != nil {
		_ = os.Remove(tempPath)
		return "", err
	}
	return path, nil
}

func ExportRowDestripeMaskAtomic(input Input, dir string, mask []bool, overwrite bool) (string, error) {
	if input.HDU.Data.Width <= 0 || input.HDU.Data.Height <= 0 {
		return "", fmt.Errorf("cannot export NIRCam row mask for %s: input dimensions are %dx%d", InputKey(input), input.HDU.Data.Width, input.HDU.Data.Height)
	}
	if err := validateArtifactMaskDimensions(mask, input.HDU.Data.Width, input.HDU.Data.Height); err != nil {
		return "", err
	}
	if !isNircamFrame(input) {
		return "", fmt.Errorf("cannot export NIRCam row mask for non-NIRCam input %s", InputKey(input))
	}
	if dir == "" {
		return "", errors.New("NIRCam row mask export directory is blank")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}

	name := rowDestripeMaskName(input)
	path := filepath.Join(dir, name)
	if !overwrite {
		if _, err := os.Stat(path); err == nil {
			return "", fmt.Errorf("NIRCam row mask already exists: %s", path)
		} else if err != nil && !os.IsNotExist(err) {
			return "", err
		}
	}

	pixels := make([]float32, len(mask))
	for i, v := range mask {
		if v {
			pixels[i] = 1
		}
	}
	header := fitsio.CloneHeader(input.HDU.Header)
	if header.Cards == nil {
		header.Cards = map[string]string{}
	}
	header.Cards["MASKTYPE"] = "'ROWDSTRP'"
	header.Cards["MASKVER"] = "1"
	header.Cards["MASKSRC"] = "'GOFITSV3'"
	if input.SCIExt > 0 {
		header.Cards["SCIEXT"] = fmt.Sprintf("%d", input.SCIExt)
	}
	header.Cards["DATE"] = "'" + time.Now().UTC().Format(time.RFC3339) + "'"

	temp, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return "", err
	}
	tempPath := temp.Name()
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return "", err
	}
	_ = os.Remove(tempPath)
	if err := fitsio.WriteFloat32Image(tempPath, header, fitsio.ImageData{
		Width:  input.HDU.Data.Width,
		Height: input.HDU.Data.Height,
		Pixels: pixels,
	}); err != nil {
		_ = os.Remove(tempPath)
		return "", err
	}
	if err := replaceFileAtomically(tempPath, path, overwrite); err != nil {
		_ = os.Remove(tempPath)
		return "", err
	}
	return path, nil
}

func validateArtifactMaskDimensions(mask []bool, width, height int) error {
	if width <= 0 || height <= 0 {
		return fmt.Errorf("artifact mask dimensions must be positive: %dx%d", width, height)
	}
	if len(mask) != width*height {
		return fmt.Errorf("artifact mask dimension mismatch: mask len=%d vs target %dx%d", len(mask), width, height)
	}
	return nil
}

func replaceFileAtomically(tempPath, path string, overwrite bool) error {
	if !overwrite {
		return os.Rename(tempPath, path)
	}
	backup := path + ".bak"
	_ = os.Remove(backup)
	hadExisting := false
	if _, err := os.Stat(path); err == nil {
		hadExisting = true
		if err := os.Rename(path, backup); err != nil {
			return err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		if hadExisting {
			_ = os.Rename(backup, path)
		}
		return err
	}
	if hadExisting {
		_ = os.Remove(backup)
	}
	return nil
}
