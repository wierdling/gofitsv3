package mosaic

import (
	"context"
	"errors"
	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/processing"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestStarMapFITSIsPaddedAndRoundTripsMask(t *testing.T) {
	h := nativeMIRIHeader()
	h.Cards["EXPTIME"] = "600"
	h.Cards["SRCFILE"] = "'wrong.fits'"
	h.Cards["CD1_1"] = "-1.123456789012345E-05"
	p := &StarMapProduct{Header: h, EvidenceMode: "mosaic-only", Map: &processing.StarMap{Width: 31, Height: 31, FWHM: 2.6, Options: processing.DefaultStarMapOptions(), Sources: []processing.StarMapSource{{ID: 1, X: 15, Y: 16, Radius: 4, Status: "accepted"}}}}
	path := filepath.Join(t.TempDir(), "map.fits")
	data := make([]float32, 31*31)
	if err := SaveStarMapFITS(context.Background(), path, p, data); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw)%2880 != 0 {
		t.Fatalf("size %d not block-aligned", len(raw))
	}
	if string(raw[:30]) != "SIMPLE  =                    T" {
		t.Fatalf("nonstandard SIMPLE %q", raw[:30])
	}
	f, err := fitsio.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.HDUs) != 5 {
		t.Fatalf("HDUs %d", len(f.HDUs))
	}
	if f.HDUs[0].Data.Pixels[16*31+15] != 1 {
		t.Fatal("mask shifted")
	}
	if f.GetHDU("LABELS").Data.Int32Pixels[16*31+15] != 1 {
		t.Fatal("label lost")
	}
	if _, ok := f.HDUs[0].Header.Cards["SRCFILE"]; ok {
		t.Fatal("source header leaked")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := SaveStarMapFITS(ctx, path, p, data); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(raw) {
		t.Fatal("cancelled save replaced previous result")
	}
}
func TestStarMapInvertsDistortedPlacement(t *testing.T) {
	h := nativeMIRIHeader()
	h.Cards["CTYPE1"] = "'RA---TAN-SIP'"
	h.Cards["CTYPE2"] = "'DEC--TAN-SIP'"
	h.Cards["A_ORDER"] = "2"
	h.Cards["B_ORDER"] = "2"
	h.Cards["A_2_0"] = "1e-5"
	h.Cards["B_0_2"] = "-1e-5"
	in := makeInput("raw.fits", 100, 100, nil, h)
	in.OffsetX = 3
	in.OffsetY = -2
	in.HasManualTransform = true
	in.ManualTransform = processing.IdentityTransform()
	in.ManualTransform.C = .4
	ref := makeInput("ref.fits", 100, 100, nil, nativeMIRIHeader())
	m, err := NewDetectorOutputMapper(in, ref, MaskOutputGeometry{Width: 130, Height: 130, OriginX: -4, OriginY: -7, Scale: 1.2})
	if err != nil {
		t.Fatal(err)
	}
	ox, oy := m.MapDetectorToOutput(38.3, 52.7)
	x, y, ok := invertStarMapPlacement(m, ox, oy, 100, 100)
	if !ok || math.Hypot(x-38.3, y-52.7) > .03 {
		t.Fatalf("inverse %v %v %v", x, y, ok)
	}
}
func TestStarMapWorkingPathAndObservationIdentity(t *testing.T) {
	if got := StarMapWorkingPath(filepath.Join("data", "F673N_drizzle.fits")); got != filepath.Join("data", "working", "F673N_starmap.fits") {
		t.Fatal(got)
	}
	if starObservationKey("a_flc.fits") != starObservationKey("a_flt.fits") {
		t.Fatal("double-counted observation variants")
	}
}
func TestStarMapReviewRoundTripAndRejectsChangedScience(t *testing.T) {
	data := fitsio.ImageData{Width: 31, Height: 31, Pixels: make([]float32, 31*31)}
	p := &StarMapProduct{Header: nativeMIRIHeader(), EvidenceMode: "mosaic-only", Map: &processing.StarMap{Width: 31, Height: 31, FWHM: 2.6, Options: processing.DefaultStarMapOptions(), Sources: []processing.StarMapSource{{ID: 1, X: 15.25, Y: 16.5, Radius: 4, Status: "uncertain", Reason: "profile", Override: "accept", Saturated: true, Usable: 6, Confirmed: 3}}}}
	path := filepath.Join(t.TempDir(), "map.fits")
	if err := SaveStarMapFITS(context.Background(), path, p, data.Pixels); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadStarMapFITS(context.Background(), path, data, p.Header)
	if err != nil {
		t.Fatal(err)
	}
	s := loaded.Map.Sources[0]
	if s.X != 15.25 || s.Y != 16.5 || s.Override != "accept" || s.Confirmed != 3 || !s.Saturated {
		t.Fatalf("review changed: %+v", s)
	}
	if _, err := LoadInputsFromPath(path); err == nil {
		t.Fatal("star map loaded as science")
	}
	if _, err := LoadInputsMetadataFromPath(path); err == nil {
		t.Fatal("star map metadata loaded as science")
	}
	data.Pixels[0] = 1
	if _, err := LoadStarMapFITS(context.Background(), path, data, p.Header); err == nil {
		t.Fatal("changed science accepted")
	}
}
func TestStarMapRawDQUsesPairedSCIGeometryAndExcludesArtifacts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "raw_flc.fits")
	h := nativeMIRIHeader()
	h.Cards["EXTVER"] = "1"
	dqh := fitsio.CloneHeader(h)
	dqh.Cards["CRVAL1"] = "110" // deliberately misleading DQ WCS
	primary := fitsio.Header{Cards: map[string]string{"INSTRUME": "'WFC3'", "DETECTOR": "'UVIS'", "FILTER": "'F673N'", "EXPTIME": "800"}}
	pixels := make([]float32, 41*41)
	dq := make([]int32, len(pixels))
	dq[20*41+20] = 256
	dq[21*41+21] = 2048
	dq[22*41+22] = 8192
	for i := range pixels {
		pixels[i] = 100
	}
	if err := fitsio.WriteFloat32ImageWithExtensions(path, primary, fitsio.ImageData{}, fitsio.ImageExtension{ExtName: "SCI", Header: h, Data: fitsio.ImageData{Width: 41, Height: 41, Pixels: pixels}}, fitsio.ImageExtension{ExtName: "DQ", Header: dqh, Data: fitsio.ImageData{Width: 41, Height: 41, Int32Pixels: dq}}); err != nil {
		t.Fatal(err)
	}
	ref := makeInput("ref.fits", 41, 41, nil, nativeMIRIHeader())
	e := &StarMapEvidence{Inputs: []Input{{Path: path}, {Path: path}}, Reference: ref, Geometry: MaskOutputGeometry{Width: 41, Height: 41, Scale: 1}}
	calls := 0
	err := visitStarMapEvidence(context.Background(), e, nil, func(in, raw Input, sat []bool, m *DetectorOutputMapper) error {
		calls++
		if !sat[20*41+20] || !sat[21*41+21] || sat[22*41+22] {
			t.Fatal("wrong saturation bits")
		}
		if !math.IsNaN(float64(raw.HDU.Data.Pixels[22*41+22])) {
			t.Fatal("cosmic ray not excluded")
		}
		x, y := m.MapDetectorToOutput(20, 20)
		if math.Hypot(x-20, y-20) > .001 {
			t.Fatalf("DQ WCS used: %f %f", x, y)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("duplicate observation counted", calls)
	}
}
func TestStarMapProjectionMatchesCombinedPlacement(t *testing.T) {
	ref := makeInput("ref.fits", 100, 100, nil, nativeMIRIHeader())
	chipHeader := nativeMIRIHeader()
	chipHeader.Cards["CRPIX1"] = "5"
	chipHeader.Cards["CTYPE1"] = "'RA---TAN-SIP'"
	chipHeader.Cards["A_ORDER"] = "2"
	chipHeader.Cards["B_ORDER"] = "2"
	chipHeader.Cards["A_2_0"] = "1E-5"
	chip := makeInput("raw", 100, 100, nil, chipHeader)
	combinedHeader := nativeMIRIHeader()
	combinedHeader.Cards["CRPIX1"] = "11"
	combined := makeInput("combined", 100, 100, nil, combinedHeader)
	combined.HasManualTransform = true
	combined.ManualTransform = processing.IdentityTransform()
	combined.ManualTransform.C = .4
	combined.OffsetX = 2
	geom := MaskOutputGeometry{Width: 120, Height: 120, OriginX: -3, OriginY: -4, Scale: 1.1}
	first, err := NewDetectorOutputMapper(chip, combined, MaskOutputGeometry{Width: 100, Height: 100, Scale: 1})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewDetectorOutputMapper(combined, ref, geom)
	if err != nil {
		t.Fatal(err)
	}
	chip.ManualTransform, chip.HasManualTransform, chip.OffsetX = combined.ManualTransform, true, combined.OffsetX
	direct, err := NewDetectorOutputMapper(chip, ref, geom)
	if err != nil {
		t.Fatal(err)
	}
	x, y := first.MapDetectorToOutput(35.5, 42.25)
	ax, ay := second.MapDetectorToOutput(x, y)
	bx, by := direct.MapDetectorToOutput(35.5, 42.25)
	if math.Hypot(ax-bx, ay-by) > 1e-5 {
		t.Fatalf("combined vs direct: %.8f %.8f / %.8f %.8f", ax, ay, bx, by)
	}
}

func TestStarMapSaveRefusesScienceOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "science.fits")
	data := fitsio.ImageData{Width: 31, Height: 31, Pixels: make([]float32, 31*31)}
	if err := fitsio.WriteFloat32Image(path, nativeMIRIHeader(), data); err != nil {
		t.Fatal(err)
	}
	p := &StarMapProduct{Map: &processing.StarMap{Width: 31, Height: 31}}
	if err := SaveStarMapFITS(context.Background(), path, p, data.Pixels); err == nil {
		t.Fatal("overwrote science")
	}
}
func TestStarMapRequiresIndependentExposureConfirmations(t *testing.T) {
	const w = 65
	pixels := make([]float32, w*w)
	for y := 0; y < w; y++ {
		for x := 0; x < w; x++ {
			r2 := math.Pow(float64(x)-32.2, 2) + math.Pow(float64(y)-31.8, 2)
			alpha := 2.6 / (2 * math.Sqrt(math.Pow(2, 1/2.5)-1))
			pixels[y*w+x] = float32(20 + 100*math.Pow(1+r2/(alpha*alpha), -2.5))
		}
	}
	dir := t.TempDir()
	header := nativeMIRIHeader()
	header.Cards["EXTVER"] = "1"
	primary := fitsio.Header{Cards: map[string]string{"INSTRUME": "'WFC3'", "DETECTOR": "'UVIS'", "FILTER": "'F673N'"}}
	paths := []string{filepath.Join(dir, "a_flc.fits"), filepath.Join(dir, "b_flc.fits")}
	for _, path := range paths {
		if err := fitsio.WriteFloat32ImageWithExtensions(path, primary, fitsio.ImageData{}, fitsio.ImageExtension{ExtName: "SCI", Header: header, Data: fitsio.ImageData{Width: w, Height: w, Pixels: pixels}}, fitsio.ImageExtension{ExtName: "DQ", Header: header, Data: fitsio.ImageData{Width: w, Height: w, Int32Pixels: make([]int32, w*w)}}); err != nil {
			t.Fatal(err)
		}
	}
	ref := makeInput("ref", w, w, nil, nativeMIRIHeader())
	image := fitsio.ImageData{Width: w, Height: w, Pixels: pixels}
	for _, independent := range []bool{false, true} {
		second := paths[0]
		if independent {
			second = paths[1]
		}
		e := &StarMapEvidence{Inputs: []Input{{Path: paths[0]}, {Path: second}}, Reference: ref, Geometry: MaskOutputGeometry{Width: w, Height: w, Scale: 1}}
		p, err := CreateStarMap(context.Background(), image, nativeMIRIHeader(), e, processing.DefaultStarMapOptions())
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Map.Sources) != 1 {
			t.Fatalf("sources %+v", p.Map.Sources)
		}
		s := p.Map.Sources[0]
		if s.Accepted() != independent {
			t.Fatalf("independent=%v source=%+v", independent, s)
		}
	}
}
func TestStarMapPerChipPlacementDoesNotReuseFirstChipOffset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "raw_flc.fits")
	primary := fitsio.Header{Cards: map[string]string{"INSTRUME": "'WFC3'", "DETECTOR": "'UVIS'"}}
	exts := []fitsio.ImageExtension{}
	for _, ext := range []string{"1", "2"} {
		h := nativeMIRIHeader()
		h.Cards["EXTVER"] = ext
		exts = append(exts, fitsio.ImageExtension{ExtName: "SCI", Header: h, Data: fitsio.ImageData{Width: 41, Height: 41, Pixels: make([]float32, 41*41)}}, fitsio.ImageExtension{ExtName: "DQ", Header: h, Data: fitsio.ImageData{Width: 41, Height: 41, Int32Pixels: make([]int32, 41*41)}})
	}
	if err := fitsio.WriteFloat32ImageWithExtensions(path, primary, fitsio.ImageData{}, exts...); err != nil {
		t.Fatal(err)
	}
	e := &StarMapEvidence{Inputs: []Input{{Path: path, SCIExt: 1, OffsetX: 2}, {Path: path, SCIExt: 2, OffsetX: 7}}, Reference: makeInput("ref", 41, 41, nil, nativeMIRIHeader()), Geometry: MaskOutputGeometry{Width: 60, Height: 60, Scale: 1}}
	calls := 0
	err := visitStarMapEvidence(context.Background(), e, nil, func(in, raw Input, s []bool, m *DetectorOutputMapper) error {
		calls++
		want := 22.
		if raw.SCIExt == 2 {
			want = 27
		}
		x, _ := m.MapDetectorToOutput(20, 20)
		if math.Abs(x-want) > .001 {
			t.Fatalf("SCI %d x=%v want %v", raw.SCIExt, x, want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("processed %d chips", calls)
	}
}
