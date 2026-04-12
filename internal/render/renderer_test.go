package render

import (
	"testing"

	"gofitsv3/internal/stretch"
)

func TestComposeRGBBuildsRGBAOutput(t *testing.T) {
	buf := ComposeRGB(
		[]float32{0, 0.5},
		[]float32{0.25, 1},
		[]float32{1, 0.75},
		2, 1,
		stretch.Linear, stretch.Linear, stretch.Linear,
	)

	want := []byte{
		0, 63, 255, 255,
		127, 255, 191, 255,
	}
	if len(buf) != len(want) {
		t.Fatalf("len(buf) = %d, want %d", len(buf), len(want))
	}
	for i := range want {
		if buf[i] != want[i] {
			t.Fatalf("buf[%d] = %d, want %d", i, buf[i], want[i])
		}
	}
}

func TestComposeRGBClampsAndZeroFillsShortChannels(t *testing.T) {
	buf := ComposeRGB(
		[]float32{-1, 2},
		[]float32{0.5},
		nil,
		2, 1,
		stretch.Log, stretch.Asinh, stretch.Sqrt,
	)

	want := []byte{
		0, 127, 0, 255,
		255, 0, 0, 255,
	}
	for i := range want {
		if buf[i] != want[i] {
			t.Fatalf("buf[%d] = %d, want %d", i, buf[i], want[i])
		}
	}
}

func TestApplyHandlesBoundsAndClamping(t *testing.T) {
	tests := []struct {
		name string
		arr  []float32
		idx  int
		want float32
	}{
		{name: "in-range", arr: []float32{0.4}, idx: 0, want: 0.4},
		{name: "short-array", arr: []float32{0.4}, idx: 1, want: 0},
		{name: "negative", arr: []float32{-0.1}, idx: 0, want: 0},
		{name: "over-one", arr: []float32{1.5}, idx: 0, want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := apply(tt.arr, tt.idx); got != tt.want {
				t.Fatalf("apply(%v, %d) = %v, want %v", tt.arr, tt.idx, got, tt.want)
			}
		})
	}
}

func TestToByteClampsInput(t *testing.T) {
	tests := []struct {
		input float32
		want  byte
	}{
		{input: -1, want: 0},
		{input: 0.5, want: 127},
		{input: 2, want: 255},
	}

	for _, tt := range tests {
		if got := toByte(tt.input); got != tt.want {
			t.Fatalf("toByte(%v) = %d, want %d", tt.input, got, tt.want)
		}
	}
}
