package mosaic

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gofitsv3/internal/fitsio"
)

// combineFormatVersion is stamped into each working file's COMBVER card. Bump it
// when the combine output format changes so stale caches are rejected.
const combineFormatVersion = 1

// WorkingDirName is the subdirectory (next to the source images) that holds the
// per-exposure combined working files.
const WorkingDirName = "working"

// CombineOptions carries optional progress reporting and cancellation for the
// per-exposure combine. The zero value disables both.
type CombineOptions struct {
	// Progress, when non-nil, is forwarded to the underlying Build so the caller
	// sees the combine's drizzle stages. It may be called off the UI thread.
	Progress func(stage string, done, total int)
	// Ctx, when non-nil, cancels the combine; EnsureCombinedExposure then returns
	// ErrCancelled.
	Ctx context.Context
}

// WorkingPathFor returns the working-file path for a source exposure:
// <srcdir>/working/<srcbase without extension>_comb.fits.
func WorkingPathFor(srcPath string) string {
	dir := filepath.Dir(srcPath)
	base := filepath.Base(srcPath)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	return filepath.Join(dir, WorkingDirName, stem+"_comb.fits")
}

// EnsureCombinedExposure drizzles the SCI chips of srcPath into a single working
// FITS (image primary HDU plus a WHT weight extension), caching it next to the
// source images. When a valid cached working file already exists it is reused and
// cached is true. The chips are placed by WCS alone (no star alignment) since they
// were exposed simultaneously and share a rigid geometry.
func EnsureCombinedExposure(srcPath string, opts CombineOptions) (workingPath string, cached bool, err error) {
	workingPath = WorkingPathFor(srcPath)
	if combinedWorkingFileValid(srcPath, workingPath) {
		return workingPath, true, nil
	}
	if opts.Ctx != nil && opts.Ctx.Err() != nil {
		return "", false, ErrCancelled
	}

	srcInfo, err := os.Stat(srcPath)
	if err != nil {
		return "", false, fmt.Errorf("combine %s: %w", filepath.Base(srcPath), err)
	}
	chips, err := LoadInputsFromPath(srcPath)
	if err != nil {
		return "", false, fmt.Errorf("combine %s: %w", filepath.Base(srcPath), err)
	}

	result, err := Build(chips, Options{
		Scale:         1,
		PixFrac:       1,
		SepKernel:     KernelSquare,
		WeightingMode: WeightERR,
		CRMethod:      CRMethodNone,
		KeepWeights:   true,
		Progress:      opts.Progress,
		Ctx:           opts.Ctx,
	})
	if err != nil {
		// Propagates ErrCancelled unchanged for the caller to detect.
		return "", false, err
	}

	// Build's WeightERR path divides count-based frames by EXPTIME, leaving the
	// combined pixels in rate units. Restore them to the source units so BUNIT
	// stays truthful; the WHT plane is left as the rate-space inverse variance,
	// which is exactly the per-pixel weight the final drizzle expects. The
	// condition mirrors drizzlePixelValue's divide (chips are not exposure-
	// normalized, so framePixelsAreRate reduces to bunitIsAlreadyRate).
	exptime := chips[0].ExposureTime
	if !bunitIsAlreadyRate(chips[0].BUnit) && exptime > 0 {
		restoreCountsInPlace(result.Pixels, exptime)
	}

	stampCombinedHeader(&result.OutputHeader, srcPath, srcInfo, len(chips))

	if err := writeCombinedWorkingFile(workingPath, result); err != nil {
		return "", false, err
	}
	return workingPath, false, nil
}

// LoadInputsForPipeline is the single load entry point for the mosaic UI and
// project loader. It metadata-loads path; if the file has more than one SCI chip
// it combines them into a working file (reusing a valid cache) and returns one
// combined Input with SourcePath set to the original file. Single-chip files pass
// through unchanged. If the combine fails for a reason other than cancellation,
// the per-chip inputs are returned with fallbackErr set so the pipeline can carry
// on in the legacy per-chip mode.
func LoadInputsForPipeline(path string, opts CombineOptions) (inputs []Input, combined bool, fallbackErr error, err error) {
	meta, err := LoadInputsMetadataFromPath(path)
	if err != nil {
		return nil, false, nil, err
	}
	if len(meta) <= 1 {
		return meta, false, nil, nil
	}

	workingPath, _, cerr := EnsureCombinedExposure(path, opts)
	if cerr != nil {
		if cerr == ErrCancelled {
			return nil, false, nil, cerr
		}
		return meta, false, cerr, nil
	}

	combinedInputs, lerr := LoadInputsMetadataFromPath(workingPath)
	if lerr != nil {
		return meta, false, lerr, nil
	}
	for i := range combinedInputs {
		combinedInputs[i].SourcePath = path
	}
	return combinedInputs, true, nil, nil
}

// combinedWorkingFileValid reports whether workingPath is a combined file whose
// stamped source identity (COMBVER + source mtime + size) matches the current
// srcPath, so it can be reused without recombining.
func combinedWorkingFileValid(srcPath, workingPath string) bool {
	srcInfo, err := os.Stat(srcPath)
	if err != nil {
		return false
	}
	hdr, err := fitsio.LoadPrimaryHeader(workingPath)
	if err != nil {
		return false
	}
	if ver, ok := fitsio.HeaderFloat(hdr, "COMBVER"); !ok || int(ver) != combineFormatVersion {
		return false
	}
	if mtime, ok := fitsio.HeaderFloat(hdr, "SRCMTIME"); !ok || int64(mtime) != srcInfo.ModTime().Unix() {
		return false
	}
	if size, ok := fitsio.HeaderFloat(hdr, "SRCSIZE"); !ok || int64(size) != srcInfo.Size() {
		return false
	}
	return true
}

// stampCombinedHeader records the combined-file marker and source-identity cards
// (used for cache validation and display) on a combined working file's header.
func stampCombinedHeader(header *fitsio.Header, srcPath string, srcInfo os.FileInfo, nChips int) {
	header.Cards["IMAGETYP"] = quotedString("SCI-COMB")
	header.Cards["NCHIPS"] = strconv.Itoa(nChips)
	header.Cards["COMBVER"] = strconv.Itoa(combineFormatVersion)
	header.Cards["SRCFILE"] = quotedString(filepath.Base(srcPath))
	header.Cards["SRCMTIME"] = strconv.FormatInt(srcInfo.ModTime().Unix(), 10)
	header.Cards["SRCSIZE"] = strconv.FormatInt(srcInfo.Size(), 10)
}

// restoreCountsInPlace multiplies finite pixels by exptime, undoing the rate
// conversion Build applied to a count-based frame.
func restoreCountsInPlace(pixels []float32, exptime float64) {
	s := float32(exptime)
	for i, v := range pixels {
		if isFinite32(v) {
			pixels[i] = v * s
		}
	}
}

// writeCombinedWorkingFile writes result as a combined working FITS (SCI primary
// + WHT extension) to workingPath, creating the working directory as needed and
// swapping the file in atomically via a temp file so a crash mid-write never
// leaves a half-written cache in place.
func writeCombinedWorkingFile(workingPath string, result *Result) error {
	if err := os.MkdirAll(filepath.Dir(workingPath), 0o755); err != nil {
		return fmt.Errorf("create working dir: %w", err)
	}
	tmp := workingPath + ".tmp"
	err := fitsio.WriteFloat32ImageWithExtensions(tmp, result.OutputHeader,
		fitsio.ImageData{Width: result.Width, Height: result.Height, Pixels: result.Pixels},
		fitsio.ImageExtension{
			ExtName: "WHT",
			Data:    fitsio.ImageData{Width: result.Width, Height: result.Height, Pixels: result.Weights},
		},
	)
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("write working file: %w", err)
	}
	// Windows rename will not overwrite an existing target, so clear it first.
	os.Remove(workingPath)
	if err := os.Rename(tmp, workingPath); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("finalize working file: %w", err)
	}
	return nil
}
