package processing

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/stretch"
)

func TestCombineDiskChannelsRenameFailureRestoresDestinations(t *testing.T) {
	d := t.TempDir()
	var src, dst [3]string
	for i := range src {
		src[i] = filepath.Join(d, fmt.Sprintf("src%d.bin", i))
		dst[i] = filepath.Join(d, fmt.Sprintf("dst%d.bin", i))
		a, err := fitsio.CreateFloat32Artifact(src[i], 2, 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.WriteRow(0, []float32{float32(i + 1), 0}); err != nil {
			t.Fatal(err)
		}
		_ = a.Close()
		if err := os.WriteFile(dst[i], []byte{byte('A' + i)}, 0600); err != nil {
			t.Fatal(err)
		}
	}
	oldRename := diskComposeRename
	diskComposeRename = func(old, new string) error {
		if strings.Contains(old, ".render-") && !strings.Contains(old, ".render-backup") && new == dst[1] {
			return errors.New("injected publish rename failure")
		}
		return oldRename(old, new)
	}
	t.Cleanup(func() { diskComposeRename = oldRename })
	if err := combineDiskChannels(context.Background(), src, dst, 2, 1); err == nil {
		t.Fatal("expected injected rename failure")
	}
	for i := range dst {
		got, err := os.ReadFile(dst[i])
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, []byte{byte('A' + i)}) {
			t.Fatalf("destination %d changed after rollback: %q", i, got)
		}
	}
}

func writeDiskComposeFixture(t *testing.T, path string, value float32) {
	t.Helper()
	a, err := fitsio.CreateFloat32Artifact(path, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	row := []float32{value, value, value, value}
	for y := 0; y < 2; y++ {
		if err := a.WriteRow(y, row); err != nil {
			a.Close()
			t.Fatal(err)
		}
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
}

func diskComposeTestChannel(path string) DiskChannel {
	img := models.LoadedImage{}
	img.HDU.Data.Width, img.HDU.Data.Height = 2, 2
	img.Background, img.Peak, img.ScaledPeak = 0, 1, 1
	return DiskChannel{ArtifactPath: path, Image: img}
}

func readDiskComposePixel(t *testing.T, path string) float32 {
	t.Helper()
	a, err := fitsio.OpenFloat32ArtifactReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	row := make([]float32, 2)
	if err := a.ReadRow(0, row); err != nil {
		t.Fatal(err)
	}
	return row[0]
}

func TestComposeDiskRejectsOutputInputOverlap(t *testing.T) {
	dir := t.TempDir()
	paths := [3]string{filepath.Join(dir, "b.bin"), filepath.Join(dir, "g.bin"), filepath.Join(dir, "r.bin")}
	for _, p := range paths {
		writeDiskComposeFixture(t, p, .5)
	}
	ch := [3]DiskChannel{diskComposeTestChannel(paths[0]), diskComposeTestChannel(paths[1]), diskComposeTestChannel(paths[2])}
	_, err := ComposeDisk(context.Background(), DiskComposeRequest{Channels: ch, Output: [3]string{filepath.Join(dir, "out.bin"), filepath.Join(dir, "g.bin"), filepath.Join(dir, "out2.bin")}})
	if err == nil {
		t.Fatal("expected output/input overlap rejection")
	}
}

func TestComposeDiskPreviewUsesRGBOutputOrderAndByteScaleLevels(t *testing.T) {
	dir := t.TempDir()
	paths := [3]string{filepath.Join(dir, "blue.bin"), filepath.Join(dir, "green.bin"), filepath.Join(dir, "red.bin")}
	for i, value := range []float32{.25, .5, .75} {
		writeDiskComposeFixture(t, paths[i], value)
	}
	channels := [3]DiskChannel{
		diskComposeTestChannel(paths[0]), // B
		diskComposeTestChannel(paths[1]), // G
		diskComposeTestChannel(paths[2]), // R
	}
	out := [3]string{filepath.Join(dir, "out-r.bin"), filepath.Join(dir, "out-g.bin"), filepath.Join(dir, "out-b.bin")}
	levels := &models.RgbLevels{Max: [3]float64{255, 255, 255}}
	got, err := ComposeDisk(context.Background(), DiskComposeRequest{Channels: channels, Output: out, PreviewMax: 1600, RGBLevels: levels})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Preview) != 16 {
		t.Fatalf("preview length = %d, want 16", len(got.Preview))
	}
	want := [3]uint8{191, 128, 64}
	for c := range want {
		if delta := int(got.Preview[c]) - int(want[c]); delta < -1 || delta > 1 {
			t.Fatalf("preview channel %d = %d, want about %d", c, got.Preview[c], want[c])
		}
	}
}

func TestComposeDiskCalibratedOverlayIsLinearBeforeSharedStretch(t *testing.T) {
	dir := t.TempDir()
	base, overlay := filepath.Join(dir, "base.bin"), filepath.Join(dir, "overlay.bin")
	writeDiskComposeFixture(t, base, .2)
	writeDiskComposeFixture(t, overlay, .4)
	out := [3]string{filepath.Join(dir, "r.bin"), filepath.Join(dir, "g.bin"), filepath.Join(dir, "b.bin")}
	state := &models.ColorCalibrationState{
		Status:         models.CalibrationValid,
		BaseTransforms: [3]models.LinearTransform{{Gain: 1}, {Gain: 1}, {Gain: 1}},
		Overlays:       []models.OverlayCalibrationState{{Mode: models.OverlayCalibratedLinear, Status: models.CalibrationValid, Transform: models.LinearTransform{Gain: 1}, Strength: 1}},
	}
	ch := [3]DiskChannel{diskComposeTestChannel(base), diskComposeTestChannel(base), diskComposeTestChannel(base)}
	got, err := ComposeDisk(context.Background(), DiskComposeRequest{Channels: ch, Overlays: []DiskOverlay{{Channel: diskComposeTestChannel(overlay), Settings: models.OrangeLayerState{ColorR: 255}}}, Calibration: state, Output: out, PreviewMax: 1600})
	if err != nil {
		t.Fatal(err)
	}
	if got.Width != 2 || got.Height != 2 {
		t.Fatalf("dimensions = %dx%d", got.Width, got.Height)
	}
	if v := readDiskComposePixel(t, out[0]); math.Abs(float64(v-.6)) > 1e-5 {
		t.Fatalf("calibrated red pixel = %v, want 0.6 (linear add then stretch)", v)
	}
	if v := readDiskComposePixel(t, out[1]); math.Abs(float64(v-.2)) > 1e-5 {
		t.Fatalf("calibrated green pixel = %v, want 0.2", v)
	}
}

func TestComposeDiskArtisticOverlayRemainsPostStretchBlend(t *testing.T) {
	dir := t.TempDir()
	base, overlay := filepath.Join(dir, "base.bin"), filepath.Join(dir, "overlay.bin")
	writeDiskComposeFixture(t, base, .2)
	writeDiskComposeFixture(t, overlay, .4)
	out := [3]string{filepath.Join(dir, "r.bin"), filepath.Join(dir, "g.bin"), filepath.Join(dir, "b.bin")}
	state := &models.ColorCalibrationState{Status: models.CalibrationValid, BaseTransforms: [3]models.LinearTransform{{Gain: 1}, {Gain: 1}, {Gain: 1}}, Overlays: []models.OverlayCalibrationState{{Mode: models.OverlayArtistic, Status: models.CalibrationValid}}}
	ch := [3]DiskChannel{diskComposeTestChannel(base), diskComposeTestChannel(base), diskComposeTestChannel(base)}
	_, err := ComposeDisk(context.Background(), DiskComposeRequest{Channels: ch, Overlays: []DiskOverlay{{Channel: diskComposeTestChannel(overlay), Settings: models.OrangeLayerState{ColorR: 255, Opacity: .5}}}, Calibration: state, Output: out, PreviewMax: 1600})
	if err != nil {
		t.Fatal(err)
	}
	if v := readDiskComposePixel(t, out[0]); math.Abs(float64(v-.4)) > 1e-5 {
		t.Fatalf("artistic red pixel = %v, want 0.4 (post-stretch blend)", v)
	}
}

func TestComposeDiskArtisticOverlayUsesItsOwnNonlinearStretch(t *testing.T) {
	dir := t.TempDir()
	base, overlay := filepath.Join(dir, "base.bin"), filepath.Join(dir, "overlay.bin")
	writeDiskComposeFixture(t, base, .2)
	writeDiskComposeFixture(t, overlay, .4)
	out := [3]string{filepath.Join(dir, "r.bin"), filepath.Join(dir, "g.bin"), filepath.Join(dir, "b.bin")}
	baseCh := diskComposeTestChannel(base)
	baseCh.Image.Mode = stretch.Asinh
	baseCh.Image.AsinhScale = 1
	overlayCh := diskComposeTestChannel(overlay)
	overlayCh.Image.Mode = stretch.Linear
	overlayCh.Image.Background, overlayCh.Image.Peak, overlayCh.Image.ScaledPeak = .2, .6, 1
	state := &models.ColorCalibrationState{Status: models.CalibrationValid, BaseTransforms: [3]models.LinearTransform{{Gain: 1}, {Gain: 1}, {Gain: 1}}, Overlays: []models.OverlayCalibrationState{{Mode: models.OverlayArtistic, Status: models.CalibrationValid}}}
	ch := [3]DiskChannel{baseCh, baseCh, baseCh}
	if _, err := ComposeDisk(context.Background(), DiskComposeRequest{Channels: ch, Overlays: []DiskOverlay{{Channel: overlayCh, Settings: models.OrangeLayerState{ColorR: 255, Opacity: 1}}}, Calibration: state, Output: out, PreviewMax: 1600}); err != nil {
		t.Fatal(err)
	}
	// Overlay normalization is (0.4-0.2)/(0.6-0.2)=0.5, then blended onto
	// the asinh-stretched base value (~0.225). Using Channel 2 metadata for
	// the overlay would produce a different result, guarding ownership/order.
	if v := readDiskComposePixel(t, out[0]); math.Abs(float64(v-.725)) > .02 {
		t.Fatalf("artistic nonlinear red pixel = %v, want approximately 0.725", v)
	}
}

func TestComposeDiskHistEqUsesSharedCDFAfterCalibratedAccumulation(t *testing.T) {
	dir := t.TempDir()
	base, overlay := filepath.Join(dir, "base.bin"), filepath.Join(dir, "overlay.bin")
	a, err := fitsio.CreateFloat32Artifact(base, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	for y, row := range [][]float32{{.1, .2}, {.3, .4}} {
		if err := a.WriteRow(y, row); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	writeDiskComposeFixture(t, overlay, .1)
	out := [3]string{filepath.Join(dir, "r.bin"), filepath.Join(dir, "g.bin"), filepath.Join(dir, "b.bin")}
	meta := diskComposeTestChannel(base)
	meta.Image.Mode = stretch.HistEq
	state := &models.ColorCalibrationState{Status: models.CalibrationValid, BaseTransforms: [3]models.LinearTransform{{Gain: 1}, {Gain: 1}, {Gain: 1}}, Overlays: []models.OverlayCalibrationState{{Mode: models.OverlayCalibratedLinear, Status: models.CalibrationValid, Transform: models.LinearTransform{Gain: 1}, Strength: 1}}}
	ch := [3]DiskChannel{meta, meta, meta}
	if _, err := ComposeDisk(context.Background(), DiskComposeRequest{Channels: ch, Overlays: []DiskOverlay{{Channel: diskComposeTestChannel(overlay), Settings: models.OrangeLayerState{ColorR: 255}}}, Calibration: state, Output: out, PreviewMax: 1600}); err != nil {
		t.Fatal(err)
	}
	// The bounded bilinear sampler treats the outer edge as invalid; for this
	// 2x2 fixture only the first sample contributes and the shared CDF therefore
	// maps both observed bins to the upper quantile.
	a, err = fitsio.OpenFloat32ArtifactReadOnly(out[0])
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	row := make([]float32, 2)
	if err := a.ReadRow(0, row); err != nil {
		t.Fatal(err)
	}
	if math.Abs(float64(row[0]-1)) > .02 || math.Abs(float64(row[1]-1)) > .02 {
		t.Fatalf("shared HistEq row = %v, want [1 1] for edge-clipped fixture", row)
	}
}
