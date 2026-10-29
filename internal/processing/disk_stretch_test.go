package processing

import (
	"math"
	"testing"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/stretch"
)

// BenchmarkDiskStretch measures the per-sample scalar stretch the disk
// compositor runs for every output pixel (diskStretchFunc's closure).
func BenchmarkDiskStretch(b *testing.B) {
	row := make([]float32, 4096)
	for i := range row {
		row[i] = float32(i) / float32(len(row)) * 1200
	}
	for _, m := range []stretch.Mode{stretch.Asinh, stretch.MTF, stretch.GHS} {
		meta := models.LoadedImage{Mode: m, Background: 10, Peak: 1000, ScaledPeak: 20,
			GHSStretch: 3, GHSLocal: -0.5, GHSSymmetry: 0.2, MTFMidtone: 0.2}
		b.Run(m.String(), func(b *testing.B) {
			at, err := diskStretchFunc(DiskChannel{Image: meta}, meta, 0, 0)
			if err != nil {
				b.Fatal(err)
			}
			b.SetBytes(int64(len(row) * 4))
			var sink float32
			for b.Loop() {
				for _, v := range row {
					sink += at(v, 0, 0)
				}
			}
			_ = sink
		})
	}
}

// TestApplyStretchParallelMatchesDiskStretch pins the in-memory stretch to
// the disk compositor's scalar stretch for every mode, including HistEq
// (scalar normalisation followed by the 256-bin CDF).
func TestApplyStretchParallelMatchesDiskStretch(t *testing.T) {
	pixels := make([]float32, 0, 2100)
	for i := 0; i <= 2000; i++ {
		pixels = append(pixels, float32(i)*0.6-100)
	}
	pixels = append(pixels, float32(math.NaN()), float32(math.Inf(1)), 1e30)
	for _, meta := range []models.LoadedImage{
		{Mode: stretch.Linear, Background: 10, Peak: 1000, ScaledPeak: 50},
		{Mode: stretch.Log, Background: 10, Peak: 1000},
		{Mode: stretch.Asinh, Background: 0, Peak: 900, ScaledPeak: 20, AsinhScale: 0.1},
		{Mode: stretch.Sqrt, Background: 0, Peak: 900, ScaledPeak: 1},
		{Mode: stretch.MTF, Background: 0, Peak: 900, ScaledPeak: 10, MTFMidtone: 0.1},
		{Mode: stretch.GHS, Background: 0, Peak: 900, ScaledPeak: 10, GHSStretch: 3, GHSLocal: -0.5, GHSSymmetry: 0.2},
		{Mode: stretch.GHS, Background: 0, Peak: 900, ScaledPeak: 10, GHSStretch: 40, GHSLocal: -1, GHSSymmetry: 0.5},
		{Mode: stretch.HistEq, Background: 10, Peak: 1000, ScaledPeak: 100},
	} {
		img := meta
		img.ShowClip = true
		img.HDU = fitsio.HDU{Data: fitsio.ImageData{Width: len(pixels), Height: 1, Pixels: pixels}}
		got, mask := ApplyStretchParallel(&img)
		want := make([]float32, len(pixels))
		for i, v := range pixels {
			want[i] = DiskStretchPreviewValue(v, meta)
		}
		if meta.Mode == stretch.HistEq {
			want = stretch.Apply(want, stretch.HistEq)
		}
		for i := range want {
			if math.Abs(float64(got.Pixels[i]-want[i])) > 1e-6 {
				t.Fatalf("%v: pixel %d (%g) = %g, disk %g", meta.Mode, i, pixels[i], got.Pixels[i], want[i])
			}
		}
		if meta.Mode <= stretch.Sqrt && mask[len(pixels)-1] != 2 { // MTF and GHS saturate at exactly 1
			t.Fatalf("%v: highlight clip not flagged", meta.Mode)
		}
		if mask[len(pixels)-3] != 3 {
			t.Fatalf("%v: NaN not flagged", meta.Mode)
		}
	}
}
