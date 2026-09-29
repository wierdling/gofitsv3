package mosaic

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"gofitsv3/internal/fitsio"
)

func TestBuildRealMIRIAsdfPairUsesNativeGWCS(t *testing.T) {
	fixtureDir := filepath.Join("..", "..", "TestImages", "asdf")
	paths := []string{
		filepath.Join(fixtureDir, "jw09548001001_02101_00001_mirimage_cal.asdf"),
		filepath.Join(fixtureDir, "jw09548001001_02101_00002_mirimage_cal.asdf"),
	}
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			t.Skipf("real ASDF fixture unavailable: %v", err)
		}
	}

	inputs := make([]Input, 0, len(paths))
	for _, path := range paths {
		loaded, err := LoadInputsFromPath(path)
		if err != nil {
			t.Fatalf("LoadInputsFromPath(%s): %v", filepath.Base(path), err)
		}
		if len(loaded) != 1 {
			t.Fatalf("LoadInputsFromPath(%s) returned %d inputs, want one", filepath.Base(path), len(loaded))
		}
		in := loaded[0]
		if in.NativeGWCS == nil {
			t.Fatalf("%s did not retain native GWCS", filepath.Base(path))
		}
		if in.HDU.Data.Width <= 0 || in.HDU.Data.Height <= 0 || len(in.HDU.Data.Pixels) != in.HDU.Data.Width*in.HDU.Data.Height {
			t.Fatalf("%s has invalid science plane dimensions=%dx%d pixels=%d", filepath.Base(path), in.HDU.Data.Width, in.HDU.Data.Height, len(in.HDU.Data.Pixels))
		}
		if len(in.ERRPixels) != len(in.HDU.Data.Pixels) {
			t.Fatalf("%s ERR pixels=%d, want %d for ERR-weighted drizzle", filepath.Base(path), len(in.ERRPixels), len(in.HDU.Data.Pixels))
		}
		if len(in.DQExcluded) != 0 && len(in.DQExcluded) != len(in.HDU.Data.Pixels) {
			t.Fatalf("%s DQ mask length=%d, want zero or %d", filepath.Base(path), len(in.DQExcluded), len(in.HDU.Data.Pixels))
		}
		inputs = append(inputs, in)
	}

	mapper, err := newInputMapper(inputs[1], inputs[0])
	if err != nil {
		t.Fatalf("newInputMapper: %v", err)
	}
	for _, point := range [][2]float64{{0, 0}, {512, 512}, {1031, 1023}} {
		x, y := mapper.MapPixel(point[0], point[1])
		if !isFiniteRealASDF(x) || !isFiniteRealASDF(y) {
			t.Fatalf("native mapper returned non-finite output for pixel (%v,%v): (%v,%v)", point[0], point[1], x, y)
		}
	}

	result, err := Build(inputs, Options{
		Scale:              1,
		PixFrac:            0.8,
		WeightingMode:      WeightERR,
		CRMethod:           CRMethodNone,
		DiagnosticProducts: true,
	})
	if err != nil {
		t.Fatalf("Build real ASDF pair: %v", err)
	}
	if result.Width <= 0 || result.Height <= 0 || result.Width > 3000 || result.Height > 3000 {
		t.Fatalf("result dimensions=%dx%d, want finite positive bounded mosaic", result.Width, result.Height)
	}
	if got := fitsio.HeaderString(result.OutputHeader, "CTYPE1"); got != "RA---TAN" {
		t.Fatalf("output CTYPE1 = %q, want RA---TAN", got)
	}
	if got := fitsio.HeaderString(result.OutputHeader, "CTYPE2"); got != "DEC--TAN" {
		t.Fatalf("output CTYPE2 = %q, want DEC--TAN", got)
	}
	if _, ok := result.OutputHeader.Cards["GWCSMODEL"]; ok {
		t.Fatal("output header retained input-only GWCSMODEL marker")
	}
	if len(result.Pixels) != result.Width*result.Height || len(result.Weights) != len(result.Pixels) {
		t.Fatalf("result planes pixels=%d weights=%d dimensions=%dx%d", len(result.Pixels), len(result.Weights), result.Width, result.Height)
	}
	if result.DiagnosticProducts && len(result.DQ) != len(result.Pixels) {
		t.Fatalf("diagnostic DQ pixels=%d, want %d", len(result.DQ), len(result.Pixels))
	}
	finiteScience, covered := 0, 0
	for i, pixel := range result.Pixels {
		if math.IsNaN(float64(pixel)) || math.IsInf(float64(pixel), 0) {
			continue
		}
		finiteScience++
		if result.Weights[i] > 0 && !math.IsNaN(float64(result.Weights[i])) && !math.IsInf(float64(result.Weights[i]), 0) {
			covered++
		}
	}
	if finiteScience == 0 || covered == 0 {
		t.Fatalf("mosaic has no finite science/positive weight output: science=%d covered=%d", finiteScience, covered)
	}
	for i, status := range result.Inputs {
		if !status.Included || status.Status == "failed" || status.Error != "" {
			t.Fatalf("input status[%d]=%+v, want included successful drizzle", i, status)
		}
	}
}

func TestBuildRealNIRCamAsdfShortAndLongPairsUsesNativeGWCS(t *testing.T) {
	base := filepath.Join("..", "..", "TestImages", "asdf", "nircam")
	pairs := [][2]string{
		{filepath.Join(base, "jw09548002001_02101_00001_nrcb1_cal.asdf"), filepath.Join(base, "jw09548002001_02101_00002_nrcb1_cal.asdf")},
		{filepath.Join(base, "jw09548002001_02101_00001_nrcblong_cal.asdf"), filepath.Join(base, "jw09548002001_02101_00002_nrcblong_cal.asdf")},
	}
	for _, pair := range pairs {
		inputs := make([]Input, 0, 2)
		for _, path := range pair {
			if _, err := os.Stat(path); err != nil {
				t.Skipf("real NIRCam fixture unavailable: %v", err)
			}
			loaded, err := LoadInputsFromPath(path)
			if err != nil {
				t.Fatalf("LoadInputsFromPath(%s): %v", filepath.Base(path), err)
			}
			if len(loaded) != 1 || loaded[0].NativeGWCS == nil || loaded[0].NativeGWCSProfile == "" {
				t.Fatalf("%s did not retain one native profile input", filepath.Base(path))
			}
			inputs = append(inputs, loaded[0])
		}
		result, err := Build(inputs, Options{Scale: 1, PixFrac: 0.8, WeightingMode: WeightERR, CRMethod: CRMethodNone})
		if err != nil {
			t.Fatalf("Build NIRCam pair %s: %v", filepath.Base(pair[0]), err)
		}
		if result.Width <= 0 || result.Height <= 0 || len(result.Pixels) != result.Width*result.Height {
			t.Fatalf("NIRCam result dimensions=%dx%d pixels=%d", result.Width, result.Height, len(result.Pixels))
		}
		if fitsio.HeaderString(result.OutputHeader, "CTYPE1") != "RA---TAN" || fitsio.HeaderString(result.OutputHeader, "CTYPE2") != "DEC--TAN" {
			t.Fatalf("NIRCam output is not interoperable TAN WCS")
		}
		covered := 0
		for i, p := range result.Pixels {
			if isFiniteRealASDF(float64(p)) && result.Weights[i] > 0 {
				covered++
			}
		}
		if covered == 0 {
			t.Fatal("NIRCam mosaic has no covered finite output")
		}
	}
}

func isFiniteRealASDF(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
