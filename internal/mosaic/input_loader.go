package mosaic

import (
	"fmt"
	"math"
	"strings"

	"gofitsv3/internal/badpix"
	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/instrument"
	"gofitsv3/internal/processing"
)

// LoadInputsFromPath loads one or more mosaic inputs from a FITS file.
// Each SCI extension is returned as its own input so drizzle can treat
// multi-chip files the same way AstroDrizzle does.
func LoadInputsFromPath(path string) ([]Input, error) {
	file, err := fitsio.LoadFile(path)
	if err != nil {
		return nil, err
	}
	if len(file.HDUs) == 0 {
		return nil, fmt.Errorf("no HDUs found in %s", path)
	}

	primary := file.HDUs[0].Header
	inst, _ := instrument.FromHeader(primary)
	sci := file.SelectSCI()
	if len(sci) == 0 {
		excluded, repaired := diagnosticDQMasks(file.HDUs[0], file, inst)
		hdu := cleanSCIWithMatchingDQ(file.HDUs[0], file, inst)
		return []Input{{
			Path:          path,
			PrimaryHeader: primary,
			HDU:           hdu,
			ExposureTime:  loadExposureTime(primary, hdu.Header),
			DateObs:       loadDateObs(primary, hdu.Header),
			BUnit:         loadBUnit(hdu.Header, primary),
			WeightPixels:  loadWHTPixels(file),
			DQExcluded:    excluded, DQRepaired: repaired,
		}}, nil
	}

	inputs := make([]Input, 0, len(sci))
	for i := range sci {
		excluded, repaired := diagnosticDQMasks(sci[i], file, inst)
		hdu := cleanSCIWithMatchingDQ(sci[i], file, inst)
		extver := sciExtNumber(hdu.Header, i+1)
		d2iX, d2iY := loadD2ITables(file, extver)
		inputs = append(inputs, Input{
			Path:          path,
			SCIExt:        extver,
			PrimaryHeader: primary,
			HDU:           hdu,
			ExposureTime:  loadExposureTime(primary, hdu.Header),
			DateObs:       loadDateObs(primary, hdu.Header),
			BUnit:         loadBUnit(hdu.Header, primary),
			D2IX:          d2iX,
			D2IY:          d2iY,
			ERRPixels:     loadERRPixels(file, extver, fitsio.HeaderString(hdu.Header, "EXTVER") == ""),
			DQExcluded:    excluded, DQRepaired: repaired,
		})
	}
	return inputs, nil
}

// LoadInputsMetadataFromPath builds mosaic Inputs from a FITS file's headers and
// distortion tables only, without decoding the large SCI/ERR pixel arrays. Each
// returned Input carries its dimensions (HDU.Data.Width/Height), WCS header,
// exposure/date/BUnit metadata, and D2IMARR distortion tables, but HDU.Data.Pixels
// and ERRPixels are nil.
//
// This is the lazy "load by filter" path: pixels are streamed back on demand at
// align time (ensureInputPixelsLoaded) and build time (loadFrameFromDisk), both
// of which re-run LoadInputsFromPath so DQ cleaning/repair is applied identically.
// D2IMARR tables are loaded here because the build planner reads them from the
// in-memory Input and does not reload them per frame.
func LoadInputsMetadataFromPath(path string) ([]Input, error) {
	file, err := fitsio.LoadFileMetadata(path)
	if err != nil {
		return nil, err
	}
	if len(file.HDUs) == 0 {
		return nil, fmt.Errorf("no HDUs found in %s", path)
	}

	primary := file.HDUs[0].Header
	sci := file.SelectSCI()
	if len(sci) == 0 {
		hdu := file.HDUs[0]
		return []Input{{
			Path:          path,
			PrimaryHeader: primary,
			HDU:           hdu,
			ExposureTime:  loadExposureTime(primary, hdu.Header),
			DateObs:       loadDateObs(primary, hdu.Header),
			BUnit:         loadBUnit(hdu.Header, primary),
		}}, nil
	}

	inputs := make([]Input, 0, len(sci))
	for i := range sci {
		hdu := sci[i]
		extver := sciExtNumber(hdu.Header, i+1)
		d2iX, d2iY := loadD2ITables(file, extver)
		inputs = append(inputs, Input{
			Path:          path,
			SCIExt:        extver,
			PrimaryHeader: primary,
			HDU:           hdu,
			ExposureTime:  loadExposureTime(primary, hdu.Header),
			DateObs:       loadDateObs(primary, hdu.Header),
			BUnit:         loadBUnit(hdu.Header, primary),
			D2IX:          d2iX,
			D2IY:          d2iY,
		})
	}
	return inputs, nil
}

// chipDecodePredicate selects which HDUs to decode when reading a single chip:
// the SCI extension matching sciExtVer (and any SCI lacking an EXTVER), all DQ
// extensions (small, and matchingDQHDU may match by size rather than EXTVER, so
// loading all reproduces the full-load cleaning exactly), the matching ERR (and
// the WHT plane of a combined working file) when needAux is set, and the primary
// image of a single-HDU file. Skipped extensions are not decoded, so multi-chip
// exposures are not re-decoded in full per chip.
func chipDecodePredicate(sciExtVer int, needAux bool) func(fitsio.Header) bool {
	want := fmt.Sprintf("%d", sciExtVer)
	matchesChip := func(hdr fitsio.Header) bool {
		ev := fitsio.HeaderString(hdr, "EXTVER")
		return ev == "" || ev == want
	}
	return func(hdr fitsio.Header) bool {
		switch strings.ToUpper(strings.TrimSpace(fitsio.HeaderString(hdr, "EXTNAME"))) {
		case "DQ":
			return true
		case "SCI":
			return matchesChip(hdr)
		case "ERR":
			return needAux && matchesChip(hdr)
		case "WHT":
			return needAux
		case "":
			// Primary image of a single-HDU file (multi-extension primaries have
			// NAXIS=0 and are filtered out by the data-size guard in fitsio).
			return true
		}
		return false
	}
}

// loadChipFromDisk loads only the one SCI chip matching in.SCIExt (plus the DQ
// extensions needed to clean it, and — when needAux is set — the matching ERR
// plus the WHT plane of a combined working file), skipping the other chips. It
// returns the cleaned SCI pixels (and aux planes when requested) byte-identical
// to a full LoadInputsFromPath for that chip, using a fraction of the transient
// memory and I/O and without re-decoding the whole file once per chip for
// multi-chip exposures.
func loadChipFromDisk(in Input, needAux bool) (sci, errPix, whtPix []float32, w, h int, err error) {
	file, err := fitsio.LoadFileSelective(in.Path, chipDecodePredicate(in.SCIExt, needAux))
	if err != nil {
		return nil, nil, nil, 0, 0, err
	}
	if len(file.HDUs) == 0 {
		return nil, nil, nil, 0, 0, fmt.Errorf("no HDUs found in %s", in.Path)
	}

	inst, _ := instrument.FromHeader(file.HDUs[0].Header)
	sciHDUs := file.SelectSCI()
	if len(sciHDUs) == 0 {
		hdu := cleanSCIWithMatchingDQ(file.HDUs[0], file, inst)
		if needAux {
			whtPix = loadWHTPixels(file)
		}
		return hdu.Data.Pixels, nil, whtPix, hdu.Data.Width, hdu.Data.Height, nil
	}

	target := sciHDUs[0]
	matched := false
	for i := range sciHDUs {
		if sciExtNumber(sciHDUs[i].Header, i+1) == in.SCIExt {
			target = sciHDUs[i]
			matched = true
			break
		}
	}
	if !matched && len(sciHDUs) > 1 {
		return nil, nil, nil, 0, 0, fmt.Errorf("frame loader: no SCI ext %d in %s", in.SCIExt, in.Path)
	}
	if target.Data.Pixels == nil {
		return nil, nil, nil, 0, 0, fmt.Errorf("SCI ext %d not loaded from %s", in.SCIExt, in.Path)
	}
	hdu := cleanSCIWithMatchingDQ(target, file, inst)
	if needAux {
		errPix = loadERRPixels(file, in.SCIExt, fitsio.HeaderString(target.Header, "EXTVER") == "")
	}
	return hdu.Data.Pixels, errPix, whtPix, hdu.Data.Width, hdu.Data.Height, nil
}

// loadCleanedSCIForExtraction loads just the cleaned SCI pixels (no aux planes)
// for the chip in.SCIExt — the minimal read for building an alignment star
// catalog.
func loadCleanedSCIForExtraction(in Input) ([]float32, int, int, error) {
	sci, _, _, w, h, err := loadChipFromDisk(in, false)
	return sci, w, h, err
}

// LoadInputFromPath is retained for callers that expect exactly one input.
func LoadInputFromPath(path string) (Input, error) {
	inputs, err := LoadInputsFromPath(path)
	if err != nil {
		return Input{}, err
	}
	if len(inputs) == 0 {
		return Input{}, fmt.Errorf("no mosaic inputs found in %s", path)
	}
	return inputs[0], nil
}

func sciExtNumber(header fitsio.Header, fallback int) int {
	if extver, ok := fitsio.HeaderFloat(header, "EXTVER"); ok {
		return int(extver)
	}
	return fallback
}

// loadD2ITables extracts the D2IMARR lookup-table corrections for a given SCI
// chip (identified by its 1-based EXTVER) from an HST calibrated FITS file.
// HST pipeline convention: for SCI EXTVER N, the x-axis correction is in
// D2IMARR EXTVER 2N-1 and the y-axis correction is in D2IMARR EXTVER 2N.
// Returns nil tables (no error) if the file has no D2IMARR extensions.
func loadD2ITables(file *fitsio.File, sciExtver int) (d2iX, d2iY *processing.D2ITable) {
	xExtver := fmt.Sprintf("%d", 2*sciExtver-1)
	yExtver := fmt.Sprintf("%d", 2*sciExtver)
	if hdu := file.GetHDUByExtVer("D2IMARR", xExtver); hdu != nil {
		if t, err := processing.ParseD2ITableFromHDU(*hdu); err == nil {
			d2iX = t
		}
	}
	if hdu := file.GetHDUByExtVer("D2IMARR", yExtver); hdu != nil {
		if t, err := processing.ParseD2ITableFromHDU(*hdu); err == nil {
			d2iY = t
		}
	}
	return
}

// loadERRPixels returns the pixel data from the ERR extension matching
// sciExtver, or nil if no ERR extension exists. ERR EXTVER matches SCI EXTVER.
func loadExposureTime(headers ...fitsio.Header) float64 {
	for _, header := range headers {
		for _, key := range []string{"EXPTIME", "TEXPTIME", "EFFEXPTM", "EXPOSURE"} {
			if v, ok := fitsio.HeaderFloat(header, key); ok && v > 0 {
				return v
			}
		}
	}
	return 0
}

// loadBUnit returns the BUNIT header value (data unit) from the first header
// that has one, normally the SCI extension header. Returns "" when absent.
func loadBUnit(headers ...fitsio.Header) string {
	for _, header := range headers {
		if v := fitsio.HeaderString(header, "BUNIT"); v != "" {
			return v
		}
	}
	return ""
}

func loadDateObs(headers ...fitsio.Header) string {
	for _, header := range headers {
		raw := fitsio.HeaderString(header, "DATE-OBS", "DATEOBS")
		if raw == "" {
			continue
		}
		if len(raw) >= 10 {
			return raw[:10]
		}
		return strings.TrimSpace(raw)
	}
	return ""
}

func loadERRPixels(file *fitsio.File, sciExtver int, unversioned ...bool) []float32 {
	sciUnversioned := len(unversioned) > 0 && unversioned[0]
	extver := fmt.Sprintf("%d", sciExtver)
	hdu := file.GetHDUByExtVer("ERR", extver)
	if hdu == nil && sciUnversioned {
		hdu = file.GetHDU("ERR")
	}
	if hdu == nil || len(hdu.Data.Pixels) == 0 {
		return nil
	}
	pixels := make([]float32, len(hdu.Data.Pixels))
	copy(pixels, hdu.Data.Pixels)
	return pixels
}

// loadWHTPixels reads the WHT weight plane of a combined working file. Working
// files are single-image (one WHT extension, no EXTVER), so a plain lookup by
// name is sufficient. Returns nil when the file has no WHT extension.
func loadWHTPixels(file *fitsio.File) []float32 {
	hdu := file.GetHDU("WHT")
	if hdu == nil || len(hdu.Data.Pixels) == 0 {
		return nil
	}
	pixels := make([]float32, len(hdu.Data.Pixels))
	copy(pixels, hdu.Data.Pixels)
	return pixels
}

func cleanSCIWithMatchingDQ(hdu fitsio.HDU, file *fitsio.File, inst instrument.Info) fitsio.HDU {
	dq := matchingDQHDU(file, hdu)
	if dq == nil {
		return hdu
	}
	mask, err := badpix.MaskFromDQ(hdu, *dq, inst.BadDQBits)
	if err != nil {
		return hdu
	}
	if inst.DQAction == instrument.DQActionExclude {
		return excludeMaskedPixels(hdu, mask)
	}
	edgeMask := dqEdgeNoDataMask(mask, hdu.Data.Width, hdu.Data.Height, 0.75)
	if edgeMask != nil {
		data := hdu.Data
		pixels := make([]float32, len(data.Pixels))
		copy(pixels, data.Pixels)
		for i, edge := range edgeMask {
			if edge {
				pixels[i] = float32(math.NaN())
				mask[i] = false
			}
		}
		hdu.Data = fitsio.ImageData{Width: data.Width, Height: data.Height, Pixels: pixels}
	}
	hdu.Data = badpix.RepairMaskedPixels(hdu.Data, mask)
	return hdu
}

func diagnosticDQMasks(hdu fitsio.HDU, file *fitsio.File, inst instrument.Info) (excluded, repaired []bool) {
	dq := matchingDQHDU(file, hdu)
	if dq == nil {
		return nil, nil
	}
	mask, err := badpix.MaskFromDQ(hdu, *dq, inst.BadDQBits)
	if err != nil {
		return nil, nil
	}
	if inst.DQAction == instrument.DQActionExclude {
		excluded = append([]bool(nil), mask...)
	} else {
		repaired = append([]bool(nil), mask...)
		edge := dqEdgeNoDataMask(mask, hdu.Data.Width, hdu.Data.Height, 0.75)
		for i, isEdge := range edge {
			if isEdge {
				repaired[i] = false
				if excluded == nil {
					excluded = make([]bool, len(mask))
				}
				excluded[i] = true
			}
		}
	}
	return excluded, repaired
}

// LoadDiagnosticMasks loads only the matching SCI/DQ pair for a streamed
// input, preserving the same DQ interpretation used by normal input loading.
func LoadDiagnosticMasks(in Input) (excluded, repaired []bool, err error) {
	file, err := fitsio.LoadFile(in.Path)
	if err != nil {
		return nil, nil, err
	}
	primary := file.HDUs[0].Header
	inst, _ := instrument.FromHeader(primary)
	var target fitsio.HDU
	if in.SCIExt > 0 {
		for _, h := range file.SelectSCI() {
			if sciExtNumber(h.Header, 0) == in.SCIExt {
				target = h
				break
			}
		}
	}
	if target.Data.Width == 0 {
		sci := file.SelectSCI()
		if len(sci) > 0 {
			target = sci[0]
		} else {
			target = file.HDUs[0]
		}
	}
	excluded, repaired = diagnosticDQMasks(target, file, inst)
	return excluded, repaired, nil
}

func excludeMaskedPixels(hdu fitsio.HDU, mask []bool) fitsio.HDU {
	if len(mask) != len(hdu.Data.Pixels) {
		return hdu
	}
	data := hdu.Data
	pixels := make([]float32, len(data.Pixels))
	copy(pixels, data.Pixels)
	for i, bad := range mask {
		if bad {
			pixels[i] = float32(math.NaN())
		}
	}
	hdu.Data = fitsio.ImageData{Width: data.Width, Height: data.Height, Pixels: pixels}
	return hdu
}

func dqEdgeNoDataMask(mask []bool, width, height int, flaggedFraction float64) []bool {
	if width <= 0 || height <= 0 || len(mask) < width*height {
		return nil
	}
	if flaggedFraction <= 0 || flaggedFraction > 1 {
		flaggedFraction = 0.75
	}
	edge := make([]bool, width*height)
	any := false
	markRow := func(y int) {
		for x := 0; x < width; x++ {
			edge[y*width+x] = true
		}
		any = true
	}
	markCol := func(x int) {
		for y := 0; y < height; y++ {
			edge[y*width+x] = true
		}
		any = true
	}
	for y := 0; y < height; y++ {
		count := 0
		for x := 0; x < width; x++ {
			if mask[y*width+x] {
				count++
			}
		}
		if float64(count)/float64(width) >= flaggedFraction {
			markRow(y)
		}
	}
	for x := 0; x < width; x++ {
		count := 0
		for y := 0; y < height; y++ {
			if mask[y*width+x] {
				count++
			}
		}
		if float64(count)/float64(height) >= flaggedFraction {
			markCol(x)
		}
	}
	if !any {
		return nil
	}
	return edge
}

func matchingDQHDU(file *fitsio.File, sci fitsio.HDU) *fitsio.HDU {
	sciExtver := fitsio.HeaderString(sci.Header, "EXTVER")
	var sizeMatch *fitsio.HDU
	for i := range file.HDUs {
		hdu := &file.HDUs[i]
		if hdu.ExtName != "DQ" {
			continue
		}
		if sciExtver != "" && sciExtver == fitsio.HeaderString(hdu.Header, "EXTVER") {
			if hdu.Data.Width == sci.Data.Width && hdu.Data.Height == sci.Data.Height {
				return hdu
			}
		}
		if sciExtver == "" && sizeMatch == nil && hdu.Data.Width == sci.Data.Width && hdu.Data.Height == sci.Data.Height {
			sizeMatch = hdu
		}
	}
	return sizeMatch
}
