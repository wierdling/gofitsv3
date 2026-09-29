package starbench

import (
	"encoding/json"
	"math"
	"testing"

	"gofitsv3/internal/fitsio"
)

func centerFixture() (fitsio.ImageData, Region) {
	return fitsio.ImageData{Width: 96, Height: 96, Pixels: make([]float32, 96*96)}, Region{ID: "r", Name: "Center", Width: 96, Height: 96}
}
func addProfile(im fitsio.ImageData, x, y, amplitude, sigma float64) {
	for iy := 0; iy < im.Height; iy++ {
		for ix := 0; ix < im.Width; ix++ {
			im.Pixels[iy*im.Width+ix] += float32(amplitude * math.Exp(-(math.Pow(float64(ix)-x, 2)+math.Pow(float64(iy)-y, 2))/(2*sigma*sigma)))
		}
	}
}

func TestCenterPreviewRecoversSubpixelCenterFromOffsetClick(t *testing.T) {
	im, r := centerFixture()
	for y := 0; y < im.Height; y++ {
		for x := 0; x < im.Width; x++ {
			im.Pixels[y*im.Width+x] = float32(25 + .015*float64(x) + .02*float64(y))
		}
	}
	addProfile(im, 45.3, 49.7, 200, 1.2)
	preview, err := suggestCenter(im, r, 47, 57, 10)
	if err != nil {
		t.Fatal(err)
	}
	if math.Hypot(preview.X-45.3, preview.Y-49.7) > .12 {
		t.Fatalf("center shifted from known science peak: %+v", preview)
	}
	if math.Abs(preview.Shift-math.Hypot(preview.X-47, preview.Y-57)) > 1e-9 {
		t.Fatal("incorrect preview displacement")
	}
}
func TestCenterPreviewSearchRadiusCanExcludeBrighterNeighbor(t *testing.T) {
	im, r := centerFixture()
	addProfile(im, 40, 45, 100, 1)
	addProfile(im, 47, 45, 500, 1)
	close, err := suggestCenter(im, r, 40, 45, 3)
	if err != nil {
		t.Fatal(err)
	}
	wide, err := suggestCenter(im, r, 40, 45, 10)
	if err != nil {
		t.Fatal(err)
	}
	if math.Hypot(close.X-40, close.Y-45) > .15 || math.Hypot(wide.X-47, wide.Y-45) > .15 {
		t.Fatalf("search radius did not isolate peak: close=%+v wide=%+v", close, wide)
	}
}
func TestCenterPreviewClippedSymmetricCore(t *testing.T) {
	im, r := centerFixture()
	addProfile(im, 45, 49, 200, 1.8)
	for i, v := range im.Pixels {
		im.Pixels[i] = min(v, 35)
	}
	preview, err := suggestCenter(im, r, 48, 51, 6)
	if err != nil {
		t.Fatal(err)
	}
	if math.Hypot(preview.X-45, preview.Y-49) > .15 {
		t.Fatalf("clipped-core centroid: %+v", preview)
	}
}
func TestCenterPreviewRejectsUnsupportedScienceAndInvalidRequests(t *testing.T) {
	for _, name := range []string{"flat", "ramp", "nan", "edge", "dimensions", "point", "radius", "bounds"} {
		t.Run(name, func(t *testing.T) {
			im, r := centerFixture()
			x, y, radius := 45., 49., 10.
			switch name {
			case "flat":
			case "ramp":
				for i := range im.Pixels {
					im.Pixels[i] = float32(i % 96)
				}
			case "nan":
				for i := range im.Pixels {
					im.Pixels[i] = float32(math.NaN())
				}
			case "edge":
				addProfile(im, 1, 1, 200, 1)
				x, y = 1, 1
			case "dimensions":
				im.Pixels = im.Pixels[:1]
			case "point":
				x = math.NaN()
			case "radius":
				radius = 100
			case "bounds":
				r.Width = 1000
			}
			if _, err := suggestCenter(im, r, x, y, radius); err == nil {
				t.Fatal("unsupported preview accepted")
			}
		})
	}
}
func TestCenterEndpointDoesNotChangeLabelsOrRevealCatalog(t *testing.T) {
	s := testServer(t)
	s.runs = nil
	s.project.Regions[0].Reviewed = false
	s.project.Labels = []Label{label("star", "star", 32, 38)}
	addProfile(s.image, 30.2, 31.8, 200, 1.2)
	before, _ := json.Marshal(s.project)
	w := request(s, "POST", "/api/center-preview", map[string]any{"regionId": "r", "x": 32, "y": 38, "radius": 10})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var preview centerPreview
	if err := json.Unmarshal(w.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if math.Hypot(preview.X-30.2, preview.Y-31.8) > .15 {
		t.Fatal(preview)
	}
	after, _ := json.Marshal(s.project)
	if string(before) != string(after) {
		t.Fatal("preview changed labels or exposure history")
	}
}
