package processing

import (
	"image"
	"math"
	"testing"
)

func TestDownsampleRejectsInvalidAndIgnoresInfinity(t *testing.T) {
	if got, w, h := Downsample([]float32{1}, 2, 2, 2); got != nil || w != 0 || h != 0 {
		t.Fatalf("short input result = %v %dx%d", got, w, h)
	}
	got, w, h := Downsample([]float32{1, float32(math.Inf(1)), 3, 5}, 2, 2, 2)
	if w != 1 || h != 1 || len(got) != 1 || got[0] != 3 {
		t.Fatalf("finite average = %v %dx%d, want [3] 1x1", got, w, h)
	}
}

func TestResizeChannelRejectsInvalidShape(t *testing.T) {
	if got := ResizeChannel([]float32{1}, 2, 2, 1, 1); got != nil {
		t.Fatalf("short source returned %v", got)
	}
	if got := ResizeChannel([]float32{1}, 0, 1, 1, 1); got != nil {
		t.Fatalf("invalid dimensions returned %v", got)
	}
	got := ResizeChannel([]float32{1, float32(math.Inf(1)), 3, 4}, 2, 2, 2, 2)
	if !math.IsNaN(float64(got[1])) {
		t.Fatalf("infinite contributor produced %v, want invalid", got[1])
	}
}

func TestComposeRGBShortInputsAreSafe(t *testing.T) {
	if got, w, h, _ := ComposeRGB(nil, nil); got != nil || w != 0 || h != 0 {
		t.Fatalf("short ComposeRGB result = %v %dx%d", got, w, h)
	}
	if r, g, b, w, h := ComposeRGBFloat32(nil); r != nil || g != nil || b != nil || w != 0 || h != 0 {
		t.Fatalf("short ComposeRGBFloat32 result = %v %v %v %dx%d", r, g, b, w, h)
	}
}

func TestMagicClipHighIsStrictAndIgnoresZeroNeighbors(t *testing.T) {
	if got := len([]float64{1, 2, 3}) - countAtMost([]float64{1, 2, 3}, 2); got != 1 {
		t.Fatalf("strict high clip count = %d, want 1", got)
	}
	res := MagicLevels([]float32{1, 2, 3, 4}, 2, 2, nil, MagicBalanced)
	if res.ClipHighPercent < 0 || res.ClipHighPercent > 100 {
		t.Fatalf("invalid clip percentage %v", res.ClipHighPercent)
	}
	if isCompactPeak([]float32{0, 0, 0, 0, 10, 0, 0, 0, 0}, 3, 3, 1, 1, 10, 0, nil) {
		t.Fatal("zero-valued ring must not qualify as compact peak")
	}
}

func TestEditHelpersSupportSubimages(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for i := range base.Pix {
		base.Pix[i] = byte(i)
	}
	sub := base.SubImage(image.Rect(1, 1, 3, 3)).(*image.RGBA)
	got := ApplyGammaRGBA(sub, 1, 1, 1)
	if got.Bounds() != sub.Bounds() || got.PixOffset(1, 1) >= len(got.Pix) {
		t.Fatalf("subimage result bounds/pix invalid: %v", got.Bounds())
	}
	for y := 1; y < 3; y++ {
		for x := 1; x < 3; x++ {
			si, gi := sub.PixOffset(x, y), got.PixOffset(x, y)
			for c := 0; c < 3; c++ {
				if got.Pix[gi+c] != sub.Pix[si+c] {
					t.Fatalf("RGB changed at %d,%d channel %d", x, y, c)
				}
			}
			if got.Pix[gi+3] != sub.Pix[si+3] {
				t.Fatalf("alpha channel changed at %d,%d", x, y)
			}
		}
	}
	_ = SharpenRGBA(sub, 1, 1)
}
