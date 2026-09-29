package mosaic

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/processing"
)

// StarMapEvidence explicitly records the placement of each original observation.
// Geometry and Reference must describe the exact drizzle placement, not merely
// the inherited source header of a saved mosaic.
type StarMapEvidence struct {
	Inputs    []Input
	Reference Input
	Geometry  MaskOutputGeometry
}
type StarMapInputRecord struct {
	Path             string
	Size             int64
	Modified         int64
	Filter           string
	Exposure         float64
	SCIExt           int
	OffsetX, OffsetY float64
	Transform        processing.AffineTransform
	HasTransform     bool
}
type StarMapProduct struct {
	Map             *processing.StarMap
	Header          fitsio.Header
	Inputs          []StarMapInputRecord
	EvidenceMode    string
	Warnings        []string
	ReferencePath   string
	ReferenceHeader fitsio.Header
	Geometry        MaskOutputGeometry
}

// CreateStarMap never modifies the mosaic pixels or alignment catalogs.
func CreateStarMap(ctx context.Context, image fitsio.ImageData, header fitsio.Header, evidence *StarMapEvidence, opt processing.StarMapOptions) (*StarMapProduct, error) {
	if image.Width < 25 || image.Height < 25 || image.Width > int(^uint(0)>>1)/image.Height || len(image.Pixels) != image.Width*image.Height {
		return nil, fmt.Errorf("invalid mosaic science dimensions")
	}
	if strings.Contains(fitsio.HeaderString(header, "CTYPE1"), "SIP") || strings.Contains(fitsio.HeaderString(header, "CTYPE2"), "SIP") {
		return nil, fmt.Errorf("star maps require a distortion-corrected mosaic grid")
	}
	product := &StarMapProduct{Header: fitsio.CloneHeader(header), EvidenceMode: "mosaic-only"}
	var sat []bool
	if evidence != nil && len(evidence.Inputs) > 0 {
		if evidence.Geometry.Width != image.Width || evidence.Geometry.Height != image.Height {
			return nil, fmt.Errorf("evidence grid differs from mosaic")
		}
		product.ReferencePath = evidence.Reference.Path
		product.ReferenceHeader = fitsio.CloneHeader(evidence.Reference.HDU.Header)
		product.Geometry = evidence.Geometry
		sat = make([]bool, len(image.Pixels))
		err := visitStarMapEvidence(ctx, evidence, opt.Progress, func(in Input, raw Input, mask []bool, mapper *DetectorOutputMapper) error {
			st, err := os.Stat(raw.Path)
			if err != nil {
				return err
			}
			product.Inputs = append(product.Inputs, StarMapInputRecord{Path: raw.Path, Size: st.Size(), Modified: st.ModTime().UnixNano(), Filter: fitsio.HeaderString(raw.PrimaryHeader, "FILTER"), Exposure: raw.ExposureTime, SCIExt: raw.SCIExt, OffsetX: in.OffsetX, OffsetY: in.OffsetY, Transform: in.ManualTransform, HasTransform: in.HasManualTransform})
			for i, on := range mask {
				if i%65536 == 0 {
					if err := ctx.Err(); err != nil {
						return err
					}
				}
				if !on {
					continue
				}
				x, y := mapper.MapDetectorToOutput(float64(i%raw.HDU.Data.Width), float64(i/raw.HDU.Data.Width))
				if !isFinite64(x) || !isFinite64(y) {
					continue
				}
				xx, yy := int(math.Round(x)), int(math.Round(y))
				if xx >= 0 && yy >= 0 && xx < image.Width && yy < image.Height {
					sat[yy*image.Width+xx] = true
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		product.EvidenceMode = "native-verified"
	}
	detected, err := processing.DetectStarMap(ctx, image.Pixels, image.Width, image.Height, sat, opt)
	if err != nil {
		return nil, err
	}
	product.Map = detected
	if len(detected.Empirical) == 0 {
		product.Warnings = append(product.Warnings, "Too few isolated PSF seeds: analytic profile used without an empirical radial check.")
	}
	product.Warnings = append(product.Warnings, "The outer 12 pixels are not searched; faint, blended and non-stellar-profile sources remain uncertain.")
	if evidence != nil && len(evidence.Inputs) > 0 {
		// Count one vote per observation, even if overlapping detector chips cover it.
		votes := make(map[string]map[int]bool)
		usable := make(map[string]map[int]bool)
		err = visitStarMapEvidence(ctx, evidence, opt.Progress, func(in Input, raw Input, mask []bool, mapper *DetectorOutputMapper) error {
			key := starObservationKey(raw.Path)
			if votes[key] == nil {
				votes[key] = map[int]bool{}
				usable[key] = map[int]bool{}
			}
			for i, s := range detected.Sources {
				if i%32 == 0 {
					if err := ctx.Err(); err != nil {
						return err
					}
				}
				x, y, ok := invertStarMapPlacement(mapper, s.X, s.Y, raw.HDU.Data.Width, raw.HDU.Data.Height)
				if !ok {
					continue
				}
				measured := processing.MeasureStarMapSource(raw.HDU.Data.Pixels, raw.HDU.Data.Width, raw.HDU.Data.Height, x, y, mask, opt)
				if measured.Reason == "insufficient valid profile samples" {
					continue
				}
				usable[key][i] = true
				mx, my := mapper.MapDetectorToOutput(measured.X, measured.Y)
				if measured.Status == "accepted" && math.Hypot(mx-s.X, my-s.Y) <= 1.5 {
					votes[key][i] = true
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		for i := range detected.Sources {
			s := &detected.Sources[i]
			for key := range usable {
				if usable[key][i] {
					s.Usable++
				}
				if votes[key][i] {
					s.Confirmed++
				}
			}
			if s.Accepted() && s.Confirmed < 2 {
				s.Status = "uncertain"
				s.Reason = "fewer than two independent stellar-profile confirmations"
			}
		}
	} else {
		product.Warnings = append(product.Warnings, "Mosaic-only detection: no independent exposure or detector-saturation verification.")
	}
	return product, nil
}

func starObservationKey(path string) string {
	name := strings.ToLower(filepath.Base(path))
	for _, suffix := range []string{"_flt.fits", "_flc.fits", "_drz.fits", "_drc.fits"} {
		name = strings.TrimSuffix(name, suffix)
	}
	return name
}

func visitStarMapEvidence(ctx context.Context, e *StarMapEvidence, progress func(string, int, int), visit func(Input, Input, []bool, *DetectorOutputMapper) error) error {
	seen := map[string]bool{}
	for n, in := range e.Inputs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if in.Excluded || in.ReferenceOnly {
			continue
		}
		path := in.SourcePath
		if path == "" {
			path = in.Path
		}
		key := starObservationKey(path)
		if in.SourcePath == "" && in.SCIExt > 0 {
			key = fmt.Sprintf("%s#%d", key, in.SCIExt)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		if progress != nil {
			progress("Reading original SCI / DQ evidence", n, len(e.Inputs))
		}
		file, err := fitsio.LoadFile(path)
		if err != nil {
			return fmt.Errorf("star-map evidence %s: %w", path, err)
		}
		primary := file.HDUs[0].Header
		if !strings.EqualFold(fitsio.HeaderString(primary, "INSTRUME"), "WFC3") || !strings.EqualFold(fitsio.HeaderString(primary, "DETECTOR"), "UVIS") {
			return fmt.Errorf("native star-map evidence currently requires WFC3/UVIS: %s", path)
		}
		chips := scienceHDUs(file, primary)
		if len(chips) == 0 {
			return fmt.Errorf("no SCI chips in %s", path)
		}
		matchedChip := false
		for i, hdu := range chips {
			ext := sciExtNumber(hdu.Header, i+1)
			if in.SourcePath == "" && in.SCIExt > 0 && ext != in.SCIExt {
				continue
			}
			matchedChip = true
			dq := matchingDQHDU(file, hdu)
			if dq == nil || len(dq.Data.Pixels) != len(hdu.Data.Pixels) {
				return fmt.Errorf("missing paired SCI/DQ in %s SCI %d", path, ext)
			}
			mask := make([]bool, len(hdu.Data.Pixels))
			for k, v := range dq.Data.Pixels {
				if k%65536 == 0 {
					if err := ctx.Err(); err != nil {
						return err
					}
				}
				bits := int(v)
				mask[k] = bits&(256|2048) != 0
				// Exclude flagged measurements rather than running the normal repair path.
				// Warm/stable-hot (16/64) are left usable; all other non-saturation flags
				// are conservatively excluded from native profile fitting.
				if bits & ^(256|2048|16|64) != 0 {
					hdu.Data.Pixels[k] = float32(math.NaN())
				}
			}
			dx, dy := loadD2ITables(file, ext)
			raw := Input{Path: path, SCIExt: ext, PrimaryHeader: primary, HDU: hdu, D2IX: dx, D2IY: dy, ExposureTime: loadExposureTime(primary, hdu.Header), OffsetX: in.OffsetX, OffsetY: in.OffsetY, ManualTransform: in.ManualTransform, HasManualTransform: in.HasManualTransform}
			mapper, err := NewDetectorOutputMapper(raw, e.Reference, e.Geometry)
			if err != nil {
				return err
			}
			if err = visit(in, raw, mask, mapper); err != nil {
				return err
			}
		}
		if !matchedChip {
			return fmt.Errorf("SCI %d is missing in %s", in.SCIExt, path)
		}
	}
	if len(seen) == 0 {
		return fmt.Errorf("no usable original observations selected")
	}
	return nil
}

// Invert the actual forward mapping, including distortion and manual placement.
// Newton iteration is checked by reprojection; nonconvergence is unavailable
// evidence rather than a guessed detector location.
func invertStarMapPlacement(m *DetectorOutputMapper, ox, oy float64, w, h int) (x, y float64, ok bool) {
	x, y = float64(w)/2, float64(h)/2
	for i := 0; i < 15; i++ {
		a, b := m.MapDetectorToOutput(x, y)
		ex, ey := a-ox, b-oy
		if !isFinite64(ex) || !isFinite64(ey) {
			return 0, 0, false
		}
		if math.Hypot(ex, ey) < .02 {
			return x, y, x >= 12 && y >= 12 && x < float64(w-12) && y < float64(h-12)
		}
		ax, bx := m.MapDetectorToOutput(x+.1, y)
		ay, by := m.MapDetectorToOutput(x, y+.1)
		j00, j10, j01, j11 := (ax-a)*10, (bx-b)*10, (ay-a)*10, (by-b)*10
		det := j00*j11 - j01*j10
		if math.Abs(det) < 1e-12 {
			return 0, 0, false
		}
		x -= (j11*ex - j01*ey) / det
		y -= (-j10*ex + j00*ey) / det
	}
	return 0, 0, false
}

// StarMapWorkingPath keeps derived FITS products beside the science workspace.
func StarMapWorkingPath(sciencePath string) string {
	dir := filepath.Dir(sciencePath)
	if strings.EqualFold(filepath.Base(dir), WorkingDirName) {
		dir = filepath.Dir(dir)
	}
	stem := strings.TrimSuffix(filepath.Base(sciencePath), filepath.Ext(sciencePath))
	stem = strings.TrimSuffix(stem, "_drizzle")
	return filepath.Join(dir, WorkingDirName, stem+"_starmap.fits")
}

// StarMapEvidenceForResult snapshots only inputs that contributed to this build.
// Call immediately after Build with the SAME input slice used by Build.
func StarMapEvidenceForResult(result *Result, inputs []Input) *StarMapEvidence {
	if result == nil || len(inputs) == 0 {
		return nil
	}
	e := &StarMapEvidence{Reference: wcsReferenceInput(inputs), Geometry: MaskOutputGeometry{Width: result.Width, Height: result.Height, OriginX: result.OriginX, OriginY: result.OriginY, Scale: result.Scale}}
	for _, in := range inputs {
		if in.ReferenceOnly || in.Excluded {
			continue
		}
		included := false
		for _, s := range result.Inputs {
			if s.Path == in.Path && s.Included {
				included = true
			}
		}
		if !included {
			continue
		}
		if !strings.EqualFold(fitsio.HeaderString(in.PrimaryHeader, "INSTRUME"), "WFC3") || !strings.EqualFold(fitsio.HeaderString(in.PrimaryHeader, "DETECTOR"), "UVIS") {
			return nil
		}
		e.Inputs = append(e.Inputs, in)
	}
	if len(e.Inputs) == 0 {
		return nil
	}
	return e
}

// LoadStarMapEvidence validates saved working-image sidecars and the reference
// grid. The caller explicitly selects originals; no SRCFILE inference is used.
func LoadStarMapEvidence(image fitsio.ImageData, header fitsio.Header, paths []string, referencePath string) (*StarMapEvidence, error) {
	refPath, err := filepath.Abs(referencePath)
	if err != nil {
		return nil, err
	}
	refs, err := LoadInputsMetadataFromPath(refPath)
	if err != nil {
		return nil, err
	}
	ref := refs[0]
	if image.Width != ref.HDU.Data.Width || image.Height != ref.HDU.Data.Height {
		return nil, fmt.Errorf("alignment reference and mosaic dimensions differ")
	}
	for _, k := range []string{"CTYPE1", "CTYPE2"} {
		if fitsio.HeaderString(header, k) != fitsio.HeaderString(ref.HDU.Header, k) {
			return nil, fmt.Errorf("alignment grid differs at %s", k)
		}
	}
	for _, k := range []string{"CRPIX1", "CRPIX2", "CRVAL1", "CRVAL2", "CD1_1", "CD1_2", "CD2_1", "CD2_2"} {
		a, ok := fitsio.HeaderFloat(header, k)
		b, bok := fitsio.HeaderFloat(ref.HDU.Header, k)
		if !ok || !bok || a != b {
			return nil, fmt.Errorf("alignment grid differs at %s", k)
		}
	}
	e := &StarMapEvidence{Reference: ref, Geometry: MaskOutputGeometry{Width: image.Width, Height: image.Height, Scale: 1}}
	for _, path := range paths {
		path, err = filepath.Abs(strings.TrimSpace(path))
		if err != nil {
			return nil, err
		}
		orig, err := fitsio.LoadFileMetadata(path)
		if err != nil {
			return nil, err
		}
		if fitsio.HeaderString(orig.HDUs[0].Header, "FILTER") != fitsio.HeaderString(header, "FILTER") {
			return nil, fmt.Errorf("original filter differs from mosaic: %s", path)
		}
		ins, err := LoadInputsMetadataFromPath(WorkingPathFor(path))
		if err != nil {
			return nil, err
		}
		in := ins[0]
		in.SourcePath = path
		loaded := LoadValidatedAlignmentSidecar(in, ref)
		if loaded.Status != AlignmentSidecarLoaded {
			return nil, fmt.Errorf("valid alignment sidecar required for %s (status %d): %v", path, loaded.Status, loaded.Err)
		}
		a := loaded.Entry
		in.OffsetX, in.OffsetY = a.OffsetX, a.OffsetY
		in.ManualTransform, in.HasManualTransform = a.ManualTransform, a.HasManualTransform
		e.Inputs = append(e.Inputs, in)
	}
	return e, nil
}
