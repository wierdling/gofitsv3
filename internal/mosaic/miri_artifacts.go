package mosaic

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"gofitsv3/internal/debuglog"
	"gofitsv3/internal/fitsio"
)

func isMIRIFrame(in Input) bool {
	return strings.EqualFold(fitsio.HeaderString(in.PrimaryHeader, "INSTRUME"), "MIRI")
}

func loadMIRIArtifactMask(input Input, options SkysubOptions) ([]bool, string, error) {
	if !options.MIRIArtifactMask {
		return nil, "", nil
	}
	if input.ReferenceOnly {
		debuglog.Log(fmt.Sprintf("MIRIARTIFACT input=%s skipped: reference-only input", InputKey(input)))
		return nil, "", nil
	}
	if !isMIRIFrame(input) {
		debuglog.Log(fmt.Sprintf("MIRIARTIFACT input=%s skipped: non-MIRI input", InputKey(input)))
		return nil, "", nil
	}
	path, ok, err := miriArtifactMaskPath(input, options)
	if err != nil || !ok {
		return nil, "", err
	}
	mask, w, h, err := loadBinaryMaskFITS(path)
	if err != nil {
		return nil, path, err
	}
	if w != input.HDU.Data.Width || h != input.HDU.Data.Height {
		return nil, path, fmt.Errorf("MIRI artifact mask dimension mismatch: mask %dx%d vs input %dx%d", w, h, input.HDU.Data.Width, input.HDU.Data.Height)
	}
	return mask, path, nil
}

func miriArtifactMaskPath(input Input, options SkysubOptions) (string, bool, error) {
	if path := strings.TrimSpace(options.MIRIArtifactMaskPath); path != "" {
		return path, true, nil
	}
	dir := strings.TrimSpace(options.MIRIArtifactMaskDir)
	if dir == "" {
		debuglog.Log(fmt.Sprintf("MIRIARTIFACT input=%s skipped: mask path/directory is blank", InputKey(input)))
		return "", false, nil
	}
	name := miriArtifactMaskName(input)
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err == nil {
		return path, true, nil
	} else if os.IsNotExist(err) {
		debuglog.Log(fmt.Sprintf("MIRIARTIFACT input=%s skipped: mask not found %s", InputKey(input), name))
		return "", false, nil
	} else {
		return path, false, err
	}
}

func miriArtifactMaskName(input Input) string {
	base := filepath.Base(input.Path)
	if input.SourcePath != "" {
		base = filepath.Base(input.SourcePath)
	}
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if stem == "" {
		stem = base
	}
	return stem + "_miri_mask.fits"
}

func applyMIRIArtifactMask(p plannedInput, sci []float32, mask []bool, maskPath string) {
	if len(mask) == 0 {
		return
	}
	n := minInt(len(sci), len(mask))
	masked := 0
	valid := 0
	for i := 0; i < n; i++ {
		if mask[i] {
			if isFinite32(sci[i]) {
				masked++
			}
			sci[i] = float32(math.NaN())
		} else if isFinite32(sci[i]) {
			valid++
		}
	}
	debuglog.Log(fmt.Sprintf("MIRIARTIFACT input=%s mask=%s valid=%d masked=%d total=%d", InputKey(p.input), filepath.Base(maskPath), valid, masked, n))
}
