package ui

import (
	"fmt"
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/processing"
	"math"
	"reflect"
	"testing"
)

func TestStarMapCutoutHandlesEdgesAndInvalidSamples(t *testing.T) {
	d := fitsio.ImageData{Width: 31, Height: 31, Pixels: make([]float32, 31*31)}
	d.Pixels[0] = float32(math.NaN())
	s := processing.StarMapSource{X: 1, Y: 1, Radius: 4, Status: "accepted"}
	im := starMapCutout(d, s)
	if im.Bounds().Dx() != 97 || im.Bounds().Dy() != 97 {
		t.Fatal(im.Bounds())
	}
	green := false
	for y := 0; y < 97; y++ {
		for x := 0; x < 97; x++ {
			c := im.RGBAAt(x, y)
			if c.G == 255 && c.R == 0 {
				green = true
			}
		}
	}
	if !green {
		t.Fatal("no selected footprint overlay")
	}
}

func TestParseStarMapOptions(t *testing.T) {
	for _, tc := range []struct {
		snr, residual, fwhm string
		valid               bool
	}{
		{" 5 ", "0.36", "0", true}, {"3", "1", "8", true}, {"100", "0.01", "2.6", true},
		{"2", ".36", "0", false}, {"101", ".36", "0", false}, {"5", "0", "0", false}, {"5", "1.1", "0", false},
		{"5", ".36", "-1", false}, {"5", ".36", "8.1", false}, {"NaN", ".36", "0", false}, {"5", "Inf", "0", false}, {"5", ".36", "NaN", false}, {"", ".36", "0", false},
	} {
		opt, err := parseStarMapOptions(tc.snr, tc.residual, tc.fwhm)
		if (err == nil) != tc.valid {
			t.Fatalf("%+v: %v", tc, err)
		}
		if tc.valid && tc.snr == " 5 " && (opt.MinSNR != 5 || opt.MaxResidual != .36 || opt.FWHM != 0) {
			t.Fatalf("settings not preserved: %+v", opt)
		}
	}
}

func TestStarReviewOrderAndNavigation(t *testing.T) {
	sources := []processing.StarMapSource{
		{ID: 30, Amplitude: 10, Status: "uncertain", Override: "reject"},
		{ID: 10, Amplitude: 5, Status: "uncertain"},
		{ID: 20, Amplitude: 20, Status: "accepted", Saturated: true},
	}
	for _, tc := range []struct {
		order string
		want  []int
	}{
		{"Brightness (brightest first)", []int{2, 0, 1}},
		{"Source ID", []int{1, 2, 0}},
		{"Uncertain first", []int{1, 2, 0}},
		{"Saturated first", []int{2, 0, 1}},
	} {
		if got := starReviewOrder(sources, tc.order); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: %v", tc.order, got)
		}
	}
	indices := []int{2, 0, 1}
	for _, tc := range []struct {
		selected int
		down     bool
		want     int
	}{{2, false, 0}, {2, true, 1}, {0, true, 2}, {1, true, 2}, {1, false, 1}, {-1, true, 0}} {
		if got := starReviewNext(indices, tc.selected, tc.down); got != tc.want {
			t.Fatalf("%+v: %d", tc, got)
		}
	}
	if starReviewNext(nil, -1, true) != -1 {
		t.Fatal("empty list navigation")
	}
	if sources[0].ID != 30 || sources[1].ID != 10 {
		t.Fatal("sort changed underlying catalog")
	}
}
func TestStarReviewListDispatchesAllArrowKeys(t *testing.T) {
	for _, key := range []fyne.KeyName{fyne.KeyUp, fyne.KeyDown, fyne.KeyLeft, fyne.KeyRight} {
		calls := 0
		list := &starReviewList{reviewKey: func(e *fyne.KeyEvent) {
			calls++
			if e.Name != key {
				t.Fatal(e.Name)
			}
		}}
		list.TypedKey(&fyne.KeyEvent{Name: key})
		if calls != 1 {
			t.Fatalf("key %s dispatched %d times", key, calls)
		}
	}
}

func TestStarReviewListUsesOneRendererAfterResizeAndRefresh(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	revision := 0
	var labels []*widget.Label
	list := newStarReviewList(func() int { return 100 }, func() fyne.CanvasObject {
		label := widget.NewLabel("initial")
		labels = append(labels, label)
		return label
	}, func(id widget.ListItemID, obj fyne.CanvasObject) {
		obj.(*widget.Label).SetText(fmt.Sprintf("%d:%d", revision, id))
	})
	renderer := test.WidgetRenderer(list)
	list.Resize(fyne.NewSize(250, 300))
	list.Select(0)
	list.Refresh()
	// Capture labels belonging to the renderer displayed on the canvas.
	displayed := append([]*widget.Label(nil), labels...)
	revision = 1
	list.Select(1)
	list.Refresh()
	updated := false
	for _, label := range displayed {
		if label.Text == "1:1" {
			updated = true
		}
	}
	if !updated {
		t.Fatal("selection refresh did not reach displayed rows")
	}
	size := fyne.NewSize(380, 500)
	list.Resize(size)
	if got := renderer.Objects()[0].Size(); got != size {
		t.Fatalf("displayed scroller did not resize: %v, want %v", got, size)
	}
	list.ScrollTo(70)
	list.Resize(fyne.NewSize(210, 250))
	list.Select(71)
	list.Refresh()
	if renderer != test.WidgetRenderer(list) {
		t.Fatal("renderer identity changed")
	}
}
