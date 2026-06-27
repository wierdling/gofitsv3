package mosaic

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// stripPixels returns copies of the inputs with their in-memory SCI/ERR arrays
// removed, plus a FrameLoader that serves the original pixels back. This
// exercises Build's streaming loader path without touching disk.
func stripPixels(inputs []Input) ([]Input, func(Input) ([]float32, []float32, error)) {
	type key struct {
		path string
		ext  int
	}
	type entry struct {
		sci, err []float32
	}
	store := map[key]entry{}
	out := make([]Input, len(inputs))
	for i, in := range inputs {
		store[key{in.Path, in.SCIExt}] = entry{in.HDU.Data.Pixels, in.ERRPixels}
		cp := in
		cp.HDU.Data.Pixels = nil
		cp.ERRPixels = nil
		out[i] = cp
	}
	loader := func(in Input) ([]float32, []float32, error) {
		e, ok := store[key{in.Path, in.SCIExt}]
		if !ok {
			return nil, nil, fmt.Errorf("no stored pixels for %s", InputKey(in))
		}
		// Return copies: Build mutates loaded pixels in place (sky/SB) and loads
		// each frame more than once across passes.
		sci := append([]float32(nil), e.sci...)
		var er []float32
		if e.err != nil {
			er = append([]float32(nil), e.err...)
		}
		return sci, er, nil
	}
	return out, loader
}

func equalFloat32(a, b []float32, tol float32) (int, bool) {
	if len(a) != len(b) {
		return -1, false
	}
	for i := range a {
		an := math.IsNaN(float64(a[i]))
		bn := math.IsNaN(float64(b[i]))
		if an || bn {
			if an != bn {
				return i, false
			}
			continue
		}
		if abs32(a[i]-b[i]) > tol {
			return i, false
		}
	}
	return -1, true
}

func buildAndPixels(t *testing.T, inputs []Input, opts Options) *Result {
	t.Helper()
	res, err := Build(inputs, opts)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	return res
}

// TestBuildStreamingLoaderMatchesInMemory verifies the on-demand loader path
// produces bit-for-bit equivalent output to the in-memory path across the
// uniform, exposure-weighted, ERR-weighted, and CR-drizzle settings.
func TestBuildStreamingLoaderMatchesInMemory(t *testing.T) {
	mkInputs := func() []Input {
		ref := makeInput("ref_flc.fits", 4, 4, []float32{
			100, 110, 90, 100,
			105, 95, 100, 102,
			98, 101, 99, 100,
			100, 100, 100, 100,
		}, headerWithCRPIX(10, 10))
		ref.ExposureTime = 100
		ref.ERRPixels = filledPixels(4, 4, 10)
		other := makeInput("other_flc.fits", 4, 4, []float32{
			110, 121, 99, 110,
			115, 104, 110, 112,
			108, 111, 109, 110,
			110, 110, 110, 9000, // injected cosmic ray for the CR case
		}, headerWithCRPIX(10, 10))
		other.ExposureTime = 110
		other.ERRPixels = filledPixels(4, 4, 11)
		third := makeInput("third_flc.fits", 4, 4, []float32{
			102, 112, 92, 103,
			107, 96, 101, 104,
			99, 103, 100, 101,
			101, 101, 101, 101,
		}, headerWithCRPIX(10, 10))
		third.ExposureTime = 105
		third.ERRPixels = filledPixels(4, 4, 10.5)
		return []Input{ref, other, third}
	}

	cases := []struct {
		name string
		opts Options
	}{
		{"uniform", Options{Scale: 1}},
		{"exposure", Options{Scale: 1, WeightingMode: WeightExposure}},
		{"err", Options{Scale: 1, WeightingMode: WeightERR}},
		{"cr-drizzle", Options{Scale: 1, WeightingMode: WeightExposure, CRMethod: CRMethodDrizzle}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			inMem := buildAndPixels(t, mkInputs(), c.opts)

			stripped, loader := stripPixels(mkInputs())
			opts := c.opts
			opts.FrameLoader = loader
			streamed := buildAndPixels(t, stripped, opts)

			if inMem.Width != streamed.Width || inMem.Height != streamed.Height {
				t.Fatalf("size mismatch: in-mem %dx%d, streamed %dx%d",
					inMem.Width, inMem.Height, streamed.Width, streamed.Height)
			}
			if idx, ok := equalFloat32(inMem.Pixels, streamed.Pixels, 1e-5); !ok {
				t.Fatalf("pixel mismatch at %d: in-mem=%v streamed=%v", idx,
					sampleAt(inMem.Pixels, idx), sampleAt(streamed.Pixels, idx))
			}
		})
	}
}

func sampleAt(p []float32, idx int) float32 {
	if idx < 0 || idx >= len(p) {
		return float32(math.NaN())
	}
	return p[idx]
}

// TestBuildReferenceOnlyContributesNoPixels confirms a ReferenceOnly frame
// anchors the grid but never contributes flux, on the streaming loader path.
func TestBuildReferenceOnlyContributesNoPixels(t *testing.T) {
	ref := makeInput("baseline_flc.fits", 3, 3, filledPixels(3, 3, 999), headerWithCRPIX(10, 10))
	ref.ReferenceOnly = true
	data := makeInput("data_flc.fits", 3, 3, filledPixels(3, 3, 5), headerWithCRPIX(10, 10))

	stripped, loader := stripPixels([]Input{ref, data})
	res := buildAndPixels(t, stripped, Options{Scale: 1, FrameLoader: loader})

	for i, px := range res.Pixels {
		if math.IsNaN(float64(px)) {
			continue
		}
		if abs32(px-5) > 1e-6 {
			t.Fatalf("pixel[%d] = %v, want 5 (reference-only must not contribute 999)", i, px)
		}
	}
}

// TestBuildMedianModelFromFilesBandingMatches verifies the band-streamed median
// model is independent of band height: a forced 1-row band must produce the
// identical model to a single full-height band (no seams at band boundaries).
func TestBuildMedianModelFromFilesBandingMatches(t *testing.T) {
	const width, height, n = 5, 7, 4
	dir := t.TempDir()
	paths := make([]string, n)
	for f := 0; f < n; f++ {
		img := make([]float32, width*height)
		for i := range img {
			// Deterministic varied values; sprinkle NaN (uncovered) pixels.
			v := float32((i*7+f*3)%13) - 6
			if (i+f)%5 == 0 {
				v = float32(math.NaN())
			}
			img[i] = v
		}
		p := filepath.Join(dir, fmt.Sprintf("sep_%d.f32", f))
		if err := writeFloat32File(p, img); err != nil {
			t.Fatalf("writeFloat32File: %v", err)
		}
		paths[f] = p
	}

	saved := crModelMemBudget
	defer func() { crModelMemBudget = saved }()

	crModelMemBudget = 1 << 30 // single band
	full, err := buildMedianModelFromFiles(paths, width, height, n)
	if err != nil {
		t.Fatalf("full-band model: %v", err)
	}

	crModelMemBudget = 1 // forces bandRows == 1
	banded, err := buildMedianModelFromFiles(paths, width, height, n)
	if err != nil {
		t.Fatalf("banded model: %v", err)
	}

	if idx, ok := equalFloat32(full, banded, 0); !ok {
		t.Fatalf("band-boundary seam: model differs at %d (full=%v banded=%v)",
			idx, sampleAt(full, idx), sampleAt(banded, idx))
	}
}

// TestFloat32FileRoundTrip checks the raw temp-file codec, including NaN values
// and reads that start partway through the image (band offsets).
func TestFloat32FileRoundTrip(t *testing.T) {
	data := make([]float32, 100)
	for i := range data {
		data[i] = float32(i) * 0.5
	}
	data[10] = float32(math.NaN())
	p := filepath.Join(t.TempDir(), "rt.f32")
	if err := writeFloat32File(p, data); err != nil {
		t.Fatalf("write: %v", err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	got := make([]float32, 40)
	if err := readFloat32At(f, int64(30)*4, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if idx, ok := equalFloat32(got, data[30:70], 0); !ok {
		t.Fatalf("round-trip mismatch at band index %d", idx)
	}
}
