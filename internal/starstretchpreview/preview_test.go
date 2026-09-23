package starstretchpreview

import (
	"bytes"
	"context"
	"errors"
	"image"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/mosaic"
	"gofitsv3/internal/processing"
	"gofitsv3/internal/stretch"
)

func TestPreviewSelectionHonorsReviewAndDoesNotMutateCatalog(t *testing.T) {
	sources := []processing.StarMapSource{
		{ID: 1, Status: "accepted", Amplitude: 99, Override: "reject"},
		{ID: 2, Status: "uncertain", Amplitude: 20, Override: "accept"},
		{ID: 3, Status: "accepted", Amplitude: 30},
		{ID: 4, Status: "uncertain", Amplitude: 100},
	}
	before := append([]processing.StarMapSource(nil), sources...)
	got := selectSources(sources, 0, 2)
	if len(got) != 2 || got[0].ID != 3 || got[1].ID != 2 {
		t.Fatalf("selection = %+v", got)
	}
	if len(selectSources(sources, 1, 2)) != 0 {
		t.Fatal("excluded star selected by ID")
	}
	if !reflect.DeepEqual(before, sources) {
		t.Fatal("catalog modified")
	}
}

func TestPreviewRejectsInvalidSettingsBeforeFileRead(t *testing.T) {
	base := Request{SciencePath: "absent.fits", Limit: 1, Metadata: models.LoadedImage{Peak: 1}}
	for name, change := range map[string]func(*Request){
		"nan strength":      func(r *Request) { r.Strength = math.NaN() },
		"negative strength": func(r *Request) { r.Strength = -.1 },
		"large limit":       func(r *Request) { r.Limit = 25 },
		"invalid levels":    func(r *Request) { r.Metadata.Peak = 0 },
		"histogram":         func(r *Request) { r.Metadata.Mode = stretch.HistEq },
		"nonfinite curve":   func(r *Request) { r.Metadata.AsinhScale = math.Inf(1) },
	} {
		t.Run(name, func(t *testing.T) {
			req := base
			change(&req)
			_, err := Generate(context.Background(), req, nil)
			var pathErr *os.PathError
			if err == nil || errors.As(err, &pathErr) {
				t.Fatalf("expected validation error, got %v", err)
			}
		})
	}
}

func TestPreviewZeroStrengthPreservesFilesAndRejectsStaleMap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "science.fits")
	mapPath := filepath.Join(dir, "map.fits")
	header := fitsio.Header{Cards: map[string]string{"FILTER": "'F673N'"}}
	pixels := make([]float32, 81*81)
	for y := 0; y < 81; y++ {
		for x := 0; x < 81; x++ {
			dx, dy := float64(x-40), float64(y-40)
			pixels[y*81+x] = float32(.02 + .2*math.Exp(-(dx*dx+dy*dy)/8))
		}
	}
	data := fitsio.ImageData{Width: 81, Height: 81, Pixels: pixels}
	if err := fitsio.WriteFloat32Image(path, header, data); err != nil {
		t.Fatal(err)
	}
	product := &mosaic.StarMapProduct{Header: header, EvidenceMode: "mosaic-only", Map: &processing.StarMap{Width: 81, Height: 81, Sources: []processing.StarMapSource{{ID: 1, X: 40, Y: 40, FWHM: 4.71, Radius: 10, Amplitude: .2, Status: "accepted"}}}}
	if err := mosaic.SaveStarMapFITS(context.Background(), mapPath, product, pixels); err != nil {
		t.Fatal(err)
	}
	beforeScience, _ := os.ReadFile(path)
	beforeMap, _ := os.ReadFile(mapPath)
	req := Request{SciencePath: path, MapPath: mapPath, Limit: 1, Metadata: models.LoadedImage{Mode: stretch.Asinh, Peak: 1, ScaledPeak: 10}}
	report, err := Generate(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Stars) != 1 {
		t.Fatal("missing selected star")
	}
	s := report.Stars[0]
	if !reflect.DeepEqual(s.Normal, s.Gentle) {
		t.Fatal("zero strength differs from normal")
	}
	afterScience, _ := os.ReadFile(path)
	afterMap, _ := os.ReadFile(mapPath)
	if !bytes.Equal(beforeScience, afterScience) || !bytes.Equal(beforeMap, afterMap) {
		t.Fatal("preview changed source files")
	}
	pixels[0] += .01
	if err := fitsio.WriteFloat32Image(path, header, data); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(context.Background(), req, nil); err == nil || !strings.Contains(err.Error(), "science pixels changed") {
		t.Fatalf("stale map accepted: %v", err)
	}
}

func TestPreviewReportDoesNotOverwriteAndEscapesText(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "report")
	im := image.NewGray(image.Rect(0, 0, 1, 1))
	report := &Report{SciencePath: "<script>alert(1)</script>", Stars: []StarPreview{{SourceID: 1, Normal: im, Gentle: im, Mask: im}}}
	if err := Save(context.Background(), dir, report); err != nil {
		t.Fatal(err)
	}
	page, err := os.ReadFile(filepath.Join(dir, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(page, []byte("<script>")) {
		t.Fatal("unescaped report text")
	}
	if err := Save(context.Background(), dir, report); err == nil {
		t.Fatal("existing report overwritten")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "index.html"))
	if !bytes.Equal(page, after) {
		t.Fatal("existing report changed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	other := filepath.Join(t.TempDir(), "cancelled")
	if err := Save(ctx, other, report); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Fatal("cancelled save created report")
	}
}

func TestPreviewLeavesInvalidCoreSampleUntouched(t *testing.T) {
	const w = 81
	p := make([]float32, w*w)
	for y := 0; y < w; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x-40), float64(y-40)
			p[y*w+x] = float32(.01 + .4*math.Exp(-(dx*dx+dy*dy)/8))
		}
	}
	s := processing.StarMapSource{ID: 1, X: 40, Y: 40, FWHM: 4.71, Radius: 8, Status: "accepted", Saturated: true}
	// An invalid sample is never repaired or reconstructed; the rest of the
	// star is still treated.
	p[40*w+40] = float32(math.NaN())
	m := &processing.StarMap{Width: w, Height: w, Sources: []processing.StarMapSource{s}}
	got, err := previewSource(context.Background(), fitsio.ImageData{Width: w, Height: w, Pixels: p}, m, s, models.LoadedImage{Mode: stretch.Asinh, Peak: 1, ScaledPeak: 10}, .35)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "preview" || got.Normal == nil || got.Gentle == nil {
		t.Fatalf("invalid core sample blocked the whole preview: %+v", got)
	}
	if got.Gentle.At(40, 40) != got.Normal.At(40, 40) {
		t.Fatal("invalid sample was altered by the gentler stretch")
	}
	if got.Gentle.At(41, 40) == got.Normal.At(41, 40) {
		t.Fatal("valid core sample next to the invalid one was not treated")
	}
}

func TestPreviewSaturatedCoreProducesTreatmentWithoutChangingScience(t *testing.T) {
	const w = 81
	p := make([]float32, w*w)
	for y := 0; y < w; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x-40), float64(y-40)
			p[y*w+x] = float32(.01 + math.Min(.8, 4*math.Exp(-(dx*dx+dy*dy)/8)))
		}
	}
	before := append([]float32(nil), p...)
	s := processing.StarMapSource{ID: 1, X: 40, Y: 40, FWHM: 4.71, Radius: 10, Status: "accepted", Saturated: true}
	m := &processing.StarMap{Width: w, Height: w, Sources: []processing.StarMapSource{s}}
	meta := models.LoadedImage{Mode: stretch.MTF, Background: -.0219, Peak: .173, ScaledPeak: 10, MTFMidtone: .5}
	got, err := previewSource(context.Background(), fitsio.ImageData{Width: w, Height: w, Pixels: p}, m, s, meta, .75)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "preview" || got.Normal == nil || got.Gentle == nil || got.Mask == nil {
		t.Fatalf("saturated core has no usable preview: %s", got.Reason)
	}
	x, y := 40-got.CutoutX, 40-got.CutoutY
	n, _, _, _ := got.Normal.At(x, y).RGBA()
	g, _, _, _ := got.Gentle.At(x, y).RGBA()
	if g >= n {
		t.Fatalf("core did not become gentler: %d >= %d", g, n)
	}
	if !reflect.DeepEqual(p, before) || m.Sources[0] != s {
		t.Fatal("preview changed science pixels or catalog")
	}
	zero, err := previewSource(context.Background(), fitsio.ImageData{Width: w, Height: w, Pixels: p}, m, s, meta, 0)
	if err != nil || zero.Status != "preview" || !reflect.DeepEqual(zero.Normal, zero.Gentle) {
		t.Fatalf("zero strength lost normal rendering: status=%s err=%v", zero.Status, err)
	}
}
