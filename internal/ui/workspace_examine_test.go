package ui

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gofitsv3/internal/astroio"
	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/stretch"
)

func examineASDFTestFile(t *testing.T) string {
	t.Helper()
	yaml := "#ASDF 1.0.0\n#ASDF_STANDARD 1.6.0\ndata: !core/ndarray-1.0.0 {source: 0, datatype: float32, byteorder: little, shape: [1, 2]}\ndq: !core/ndarray-1.0.0 {source: 1, datatype: uint32, byteorder: little, shape: [1, 2]}\n...\n"
	block := func(payload []byte) []byte {
		b := make([]byte, 54+len(payload))
		binary.BigEndian.PutUint32(b, 0xd3424c4b)
		binary.BigEndian.PutUint16(b[4:], 48)
		binary.BigEndian.PutUint64(b[14:], uint64(len(payload)))
		binary.BigEndian.PutUint64(b[22:], uint64(len(payload)))
		binary.BigEndian.PutUint64(b[30:], uint64(len(payload)))
		copy(b[54:], payload)
		return b
	}
	data := make([]byte, 8)
	binary.LittleEndian.PutUint32(data, math.Float32bits(2))
	binary.LittleEndian.PutUint32(data[4:], math.Float32bits(8))
	dq := make([]byte, 8)
	binary.LittleEndian.PutUint32(dq, 1)
	binary.LittleEndian.PutUint32(dq[4:], 256)
	path := filepath.Join(t.TempDir(), "planes.asdf")
	if err := os.WriteFile(path, append(append([]byte(yaml), block(data)...), block(dq)...), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func examineTestImage(extver string) *models.LoadedImage {
	h := fitsio.Header{Cards: map[string]string{}}
	if extver != "" {
		h.Cards["EXTVER"] = "'" + extver + "'"
	}
	return &models.LoadedImage{HDU: fitsio.HDU{Header: h}}
}

func TestExaminePlaneLabelsUseStableFormatNeutralIDs(t *testing.T) {
	images := []*models.LoadedImage{{HDU: fitsio.HDU{ExtName: "data"}}, {HDU: fitsio.HDU{ExtName: "ERR"}}}
	ids := []astroio.PlaneID{"asdf:data", "asdf:err"}
	want := []string{"data 1 (asdf:data)", "ERR 2 (asdf:err)"}
	if got := examinePlaneLabels(images, ids); !reflect.DeepEqual(got, want) {
		t.Fatalf("labels = %#v, want %#v", got, want)
	}
}

func TestLoadExaminePlanesFromPathLoadsAllASDFPlanes(t *testing.T) {
	images, ids, err := loadExaminePlanesFromPath(examineASDFTestFile(t))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids, []astroio.PlaneID{"asdf:data", "asdf:dq"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("plane IDs = %#v, want %#v", got, want)
	}
	if len(images) != 2 || images[0].HDU.ExtName != "data" || images[1].HDU.ExtName != "dq" {
		t.Fatalf("images = %#v", images)
	}
	if got := images[0].HDU.Data.Pixels; !reflect.DeepEqual(got, []float32{2, 8}) {
		t.Fatalf("data pixels = %#v", got)
	}
	if got := images[1].HDU.Data.Pixels; !reflect.DeepEqual(got, []float32{1, 256}) {
		t.Fatalf("DQ pixels = %#v", got)
	}
	if images[0].White != 8 || images[0].Black != 2 || images[1].White != 256 {
		t.Fatalf("plane stretch defaults were not derived: data=%#v dq=%#v", images[0], images[1])
	}
}

func TestLoadExaminePlanesFromPathLoadsAllFITSPlanesAndCleansBeforeLevels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "multi.fits")
	primaryHeader := fitsio.Header{Cards: map[string]string{"OBJECT": "'primary'", "INSTRUME": "'WFC3'", "DETECTOR": "'IR'", "EXTVER": "1"}}
	primary := fitsio.ImageData{Width: 2, Height: 2, Pixels: []float32{1, 2, 3, 4}}
	exts := []fitsio.ImageExtension{
		{ExtName: "SCI", Header: fitsio.Header{Cards: map[string]string{"EXTVER": "2"}}, Data: fitsio.ImageData{Width: 2, Height: 2, Pixels: []float32{1, 2, 1000, 4}}},
		{ExtName: "DQ", Header: fitsio.Header{Cards: map[string]string{"EXTVER": "2"}}, Data: fitsio.ImageData{Width: 2, Height: 2, Pixels: []float32{0, 0, 256, 0}}},
	}
	if err := fitsio.WriteFloat32ImageWithExtensions(path, primaryHeader, primary, exts...); err != nil {
		t.Fatal(err)
	}
	images, ids, err := loadExaminePlanesFromPath(path)
	if err != nil {
		t.Fatal(err)
	}
	wantIDs := []astroio.PlaneID{"fits:hdu:0", "fits:hdu:1", "fits:hdu:2"}
	if !reflect.DeepEqual(ids, wantIDs) {
		t.Fatalf("plane IDs = %#v, want %#v", ids, wantIDs)
	}
	if len(images) != 3 || images[0].HDU.ExtName != "" || images[1].HDU.ExtName != "SCI" || images[2].HDU.ExtName != "DQ" {
		t.Fatalf("plane identity/order = %#v", images)
	}
	if got := fitsio.HeaderString(images[0].Primary, "OBJECT"); got != "primary" {
		t.Fatalf("primary header = %q", got)
	}
	if got := fitsio.HeaderString(images[1].HDU.Header, "EXTVER"); got != "2" {
		t.Fatalf("SCI header EXTVER = %q", got)
	}
	if !reflect.DeepEqual(images[0].HDU.Data.Pixels, primary.Pixels) {
		t.Fatalf("primary display pixels = %#v, want %#v", images[0].HDU.Data.Pixels, primary.Pixels)
	}
	if got := images[1].HDU.Data.Pixels; reflect.DeepEqual(got, exts[0].Data.Pixels) || got[2] >= 1000 {
		t.Fatalf("SCI was not DQ-cleaned before display/levels: %#v", got)
	}
	if images[1].White >= 1000 {
		t.Fatalf("SCI white level used masked outlier: %v", images[1].White)
	}
	if !reflect.DeepEqual(images[2].HDU.Data.Pixels, exts[1].Data.Pixels) {
		t.Fatalf("DQ display pixels = %#v, want %#v", images[2].HDU.Data.Pixels, exts[1].Data.Pixels)
	}
}

func TestLoadExamineDiagnosticsIgnoresNonFITS(t *testing.T) {
	if got := loadExamineDiagnosticsFromPath(examineASDFTestFile(t)); len(got) != 0 {
		t.Fatalf("ASDF diagnostics = %#v, want none", got)
	}
}

func TestApplyExamineChannelStateRestoresPerPlaneStretch(t *testing.T) {
	img := &models.LoadedImage{}
	applyExamineChannelState(img, models.ChannelState{Mode: "Asinh", Black: 2, White: 9, Background: 3, Peak: 8, ScaledPeak: 4, MTFMidtone: .3, ShowClip: false})
	if img.Mode != stretch.Asinh || img.Black != 2 || img.White != 9 || img.Background != 3 || img.Peak != 8 || img.ScaledPeak != 4 || img.MTFMidtone != .3 || img.ShowClip {
		t.Fatalf("state was not restored: %#v", img)
	}
}

func TestExamineChipLabels(t *testing.T) {
	images := []*models.LoadedImage{examineTestImage("1"), examineTestImage("2"), examineTestImage("")}
	want := []string{"SCI 1 (EXTVER 1)", "SCI 2 (EXTVER 2)", "SCI 3 (EXTVER 3)"}
	if got := examineChipLabels(images); !reflect.DeepEqual(got, want) {
		t.Fatalf("labels = %#v, want %#v", got, want)
	}
}

func TestExamineChipIndexPrefersEXTVERAndFallsBackSafely(t *testing.T) {
	images := []*models.LoadedImage{examineTestImage("2"), examineTestImage("4")}
	tests := []struct {
		name     string
		extver   string
		fallback int
		want     int
	}{
		{"matching extver", "4", 0, 1},
		{"missing extver uses valid fallback", "3", 0, 0},
		{"invalid fallback uses first", "3", 9, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := examineChipIndex(images, tt.extver, tt.fallback); got != tt.want {
				t.Fatalf("index = %d, want %d", got, tt.want)
			}
		})
	}
	if got := examineChipIndex(nil, "", 0); got != -1 {
		t.Fatalf("empty index = %d, want -1", got)
	}
}

func TestExamineChipIndexNewLoadDefaultsToFirstChip(t *testing.T) {
	images := []*models.LoadedImage{examineTestImage("1"), examineTestImage("2")}
	if got := examineChipIndex(images, "", 0); got != 0 {
		t.Fatalf("new-load index = %d, want first chip (0)", got)
	}
	if got := examineChipIndex(images, "2", 1); got != 1 {
		t.Fatalf("reload index = %d, want matching EXTVER chip (1)", got)
	}
}
