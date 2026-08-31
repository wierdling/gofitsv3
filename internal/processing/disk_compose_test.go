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
	"gofitsv3/internal/histogram"
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

func TestArtifactSamplerPreservesInBoundsEdges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "edge.bin")
	a, err := fitsio.CreateFloat32Artifact(path, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.WriteRow(0, []float32{1, 2}); err != nil {
		t.Fatal(err)
	}
	if err := a.WriteRow(1, []float32{3, 4}); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err = fitsio.OpenFloat32ArtifactReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	s := newArtifactSampler(a)
	for _, tc := range []struct{ x, y, want float64 }{{1, 1, 4}, {0, 1, 3}, {1, .5, 3}} {
		if got := s.sample(tc.x, tc.y); math.Abs(float64(got)-tc.want) > 1e-6 {
			t.Fatalf("sample(%v,%v)=%v, want %v", tc.x, tc.y, got, tc.want)
		}
	}
	if got := s.sample(2, 1); got != 0 {
		t.Fatalf("out-of-footprint sample = %v, want 0", got)
	}
}

func TestComposeDiskPreviewFailurePreservesDestinations(t *testing.T) {
	dir := t.TempDir()
	paths := [3]string{filepath.Join(dir, "b.bin"), filepath.Join(dir, "g.bin"), filepath.Join(dir, "r.bin")}
	for _, p := range paths {
		writeDiskComposeFixture(t, p, .5)
	}
	out := [3]string{filepath.Join(dir, "out-r.bin"), filepath.Join(dir, "out-g.bin"), filepath.Join(dir, "out-b.bin")}
	for i, p := range out {
		if err := os.WriteFile(p, []byte{byte('Q' + i)}, 0600); err != nil {
			t.Fatal(err)
		}
	}
	old := diskCompositePreviewForCompose
	diskCompositePreviewForCompose = func(context.Context, [3]string, int, int, int, *models.RgbLevels) ([]byte, [3]histogram.Stats, error) {
		return nil, [3]histogram.Stats{}, context.Canceled
	}
	t.Cleanup(func() { diskCompositePreviewForCompose = old })
	ch := [3]DiskChannel{diskComposeTestChannel(paths[0]), diskComposeTestChannel(paths[1]), diskComposeTestChannel(paths[2])}
	if _, err := ComposeDisk(context.Background(), DiskComposeRequest{Channels: ch, Output: out}); err == nil {
		t.Fatal("expected preview failure")
	}
	for i, p := range out {
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, []byte{byte('Q' + i)}) {
			t.Fatalf("destination %d changed: %q", i, got)
		}
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
	levels := &models.RgbLevels{Min: [3]float64{0, 64, 128}, Max: [3]float64{255, 192, 255}}
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

func TestComposeDiskWeightedRejectsLRGB(t *testing.T) {
	dir := t.TempDir()
	paths := [3]string{filepath.Join(dir, "b.bin"), filepath.Join(dir, "g.bin"), filepath.Join(dir, "r.bin")}
	for _, p := range paths {
		writeDiskComposeFixture(t, p, .5)
	}
	out := [3]string{filepath.Join(dir, "r-out.bin"), filepath.Join(dir, "g-out.bin"), filepath.Join(dir, "b-out.bin")}
	channels := [3]DiskChannel{diskComposeTestChannel(paths[0]), diskComposeTestChannel(paths[1]), diskComposeTestChannel(paths[2])}
	for _, mode := range []models.ComposeMode{"", models.ComposeModeAuto, models.ComposeModeWeighted, models.ComposeModeArtistic} {
		_, err := ComposeDisk(context.Background(), DiskComposeRequest{Channels: channels, CompositionMode: mode, LRGB: models.LRGBSettings{Enabled: true}, Output: out})
		if err == nil || !strings.Contains(err.Error(), "does not support LRGB") {
			t.Fatalf("mode %q error = %v, want actionable LRGB rejection", mode, err)
		}
	}
}

func TestComposeDiskWeightedKeepsDuplicateArtifactIdentities(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.bin")
	dup := filepath.Join(dir, "duplicate.bin")
	writeDiskComposeFixture(t, base, .2)
	writeDiskComposeFixture(t, dup, .4)
	ch := [3]DiskChannel{diskComposeTestChannel(base), diskComposeTestChannel(base), diskComposeTestChannel(base)}
	out := [3]string{filepath.Join(dir, "r.bin"), filepath.Join(dir, "g.bin"), filepath.Join(dir, "b.bin")}
	ovs := []DiskOverlay{{Channel: diskComposeTestChannel(dup), Settings: models.OrangeLayerState{BlinkID: "first"}}, {Channel: diskComposeTestChannel(dup), Settings: models.OrangeLayerState{BlinkID: "second"}}}
	weights := []models.ComposeMixWeight{{BlinkID: models.ComposeChannel1BlinkID, Blue: 1}, {BlinkID: models.ComposeChannel2BlinkID, Green: 1}, {BlinkID: models.ComposeChannel3BlinkID, Red: 1}, {BlinkID: "first", Red: .25}, {BlinkID: "second", Green: .5}}
	if _, err := ComposeDisk(context.Background(), DiskComposeRequest{Channels: ch, Overlays: ovs, CompositionMode: models.ComposeModeWeighted, MixWeights: weights, Output: out}); err != nil {
		t.Fatal(err)
	}
	if got := readDiskComposePixel(t, out[0]); math.Abs(float64(got-(1.25/1.5))) > 1e-5 {
		t.Fatalf("red = %v, want normalized duplicate contribution", got)
	}
	if got := readDiskComposePixel(t, out[1]); math.Abs(float64(got-1)) > 1e-5 {
		t.Fatalf("green = %v, want normalized duplicate contribution", got)
	}
}

func TestComposeDiskArtisticOverlayRemainsPostStretchBlend(t *testing.T) {
	dir := t.TempDir()
	base, overlay := filepath.Join(dir, "base.bin"), filepath.Join(dir, "overlay.bin")
	writeDiskComposeFixture(t, base, .2)
	writeDiskComposeFixture(t, overlay, .4)
	out := [3]string{filepath.Join(dir, "r.bin"), filepath.Join(dir, "g.bin"), filepath.Join(dir, "b.bin")}
	ch := [3]DiskChannel{diskComposeTestChannel(base), diskComposeTestChannel(base), diskComposeTestChannel(base)}
	_, err := ComposeDisk(context.Background(), DiskComposeRequest{Channels: ch, Overlays: []DiskOverlay{{Channel: diskComposeTestChannel(overlay), Settings: models.OrangeLayerState{ColorR: 255, Opacity: .5}}}, Output: out, PreviewMax: 1600})
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
	ch := [3]DiskChannel{baseCh, baseCh, baseCh}
	if _, err := ComposeDisk(context.Background(), DiskComposeRequest{Channels: ch, Overlays: []DiskOverlay{{Channel: overlayCh, Settings: models.OrangeLayerState{ColorR: 255, Opacity: 1}}}, Output: out, PreviewMax: 1600}); err != nil {
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
	ch := [3]DiskChannel{meta, meta, meta}
	if _, err := ComposeDisk(context.Background(), DiskComposeRequest{Channels: ch, Overlays: []DiskOverlay{{Channel: diskComposeTestChannel(overlay), Settings: models.OrangeLayerState{ColorR: 255}}}, Output: out, PreviewMax: 1600}); err != nil {
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

func TestComposeDiskWeightedStreamsAllSources(t *testing.T) {
	dir := t.TempDir()
	paths := [4]string{filepath.Join(dir, "b.bin"), filepath.Join(dir, "g.bin"), filepath.Join(dir, "r.bin"), filepath.Join(dir, "o.bin")}
	for i, value := range []float32{.2, .4, .6, .8} {
		writeDiskComposeFixture(t, paths[i], value)
	}
	channels := [3]DiskChannel{diskComposeTestChannel(paths[0]), diskComposeTestChannel(paths[1]), diskComposeTestChannel(paths[2])}
	out := [3]string{filepath.Join(dir, "r-out.bin"), filepath.Join(dir, "g-out.bin"), filepath.Join(dir, "b-out.bin")}
	result, err := ComposeDisk(context.Background(), DiskComposeRequest{
		Channels: channels, CompositionMode: models.ComposeModeWeighted,
		Overlays: []DiskOverlay{{Channel: diskComposeTestChannel(paths[3]), Settings: models.OrangeLayerState{BlinkID: "overlay-1"}}},
		MixWeights: []models.ComposeMixWeight{
			{BlinkID: models.ComposeChannel1BlinkID, Blue: 1},
			{BlinkID: models.ComposeChannel2BlinkID, Green: 1},
			{BlinkID: models.ComposeChannel3BlinkID, Red: 1},
			{BlinkID: "overlay-1", Red: .25, Green: .5, Blue: .75},
		},
		Output: out, PreviewMax: 1600,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Width != 2 || result.Height != 2 {
		t.Fatalf("weighted result dimensions = %dx%d", result.Width, result.Height)
	}
	for i, want := range []float32{float32(1.25 / 1.75), float32(1.5 / 1.75), 1} {
		if got := readDiskComposePixel(t, out[[3]int{0, 1, 2}[i]]); math.Abs(float64(got-want)) > 1e-5 {
			t.Errorf("weighted output channel %d = %v, want %v", i, got, want)
		}
	}
}

func TestComposeDiskWeightedMatchesNormalForThreeFourFiveSources(t *testing.T) {
	dir := t.TempDir()
	values := [][]float32{{.1, .8, .3, .6}, {.2, .7, .4, .9}, {.3, .6, .5, .8}, {.4, .5, .6, .7}, {.5, .4, .7, .6}}
	weights := []models.ComposeMixWeight{{BlinkID: models.ComposeChannel1BlinkID, Blue: .7, Red: .2}, {BlinkID: models.ComposeChannel2BlinkID, Green: .8, Red: .1}, {BlinkID: models.ComposeChannel3BlinkID, Red: .9, Blue: .1}, {BlinkID: "overlay-1", Red: .3, Green: .6, Blue: .2}, {BlinkID: "overlay-2", Red: .5, Green: .1, Blue: .4}}
	imgs := make([]*models.LoadedImage, 5)
	channels := [3]DiskChannel{}
	for i := range values {
		path := filepath.Join(dir, fmt.Sprintf("source-%d.bin", i))
		a, err := fitsio.CreateFloat32Artifact(path, 2, 2)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.WriteRow(0, values[i][:2]); err != nil {
			t.Fatal(err)
		}
		if err := a.WriteRow(1, values[i][2:]); err != nil {
			t.Fatal(err)
		}
		if err := a.Close(); err != nil {
			t.Fatal(err)
		}
		imgs[i] = &models.LoadedImage{HDU: fitsio.HDU{Data: fitsio.ImageData{Width: 2, Height: 2, Pixels: append([]float32(nil), values[i]...)}}, Mode: stretch.Linear, Black: 0, White: 1, Background: 0, Peak: 1, ScaledPeak: 1}
		if i < 3 {
			channels[i] = DiskChannel{ArtifactPath: path, Image: *imgs[i]}
			continue
		}
	}
	for count := 3; count <= 5; count++ {
		overlays := make([]OverlayLayer, 0, count-3)
		diskOverlays := make([]DiskOverlay, 0, count-3)
		for i := 3; i < count; i++ {
			id := fmt.Sprintf("overlay-%d", i-2)
			overlays = append(overlays, OverlayLayer{Image: imgs[i], Settings: models.OrangeLayerState{BlinkID: id}})
			diskOverlays = append(diskOverlays, DiskOverlay{Channel: DiskChannel{ArtifactPath: filepath.Join(dir, fmt.Sprintf("source-%d.bin", i)), Image: *imgs[i]}, Settings: models.OrangeLayerState{BlinkID: id}})
		}
		normal, w, _, err := ComposeWeightedRGBPlanes(context.Background(), imgs[:3], overlays, weights)
		if err != nil {
			t.Fatal(err)
		}
		out := [3]string{filepath.Join(dir, fmt.Sprintf("r-%d.bin", count)), filepath.Join(dir, fmt.Sprintf("g-%d.bin", count)), filepath.Join(dir, fmt.Sprintf("b-%d.bin", count))}
		if _, err := ComposeDisk(context.Background(), DiskComposeRequest{Channels: channels, Overlays: diskOverlays, CompositionMode: models.ComposeModeWeighted, MixWeights: weights, Output: out}); err != nil {
			t.Fatal(err)
		}
		for c := 0; c < 3; c++ {
			a, err := fitsio.OpenFloat32ArtifactReadOnly(out[c])
			if err != nil {
				t.Fatal(err)
			}
			row := make([]float32, w)
			if err := a.ReadRow(0, row); err != nil {
				t.Fatal(err)
			}
			_ = a.Close()
			if math.Abs(float64(row[0]-normal[c][0])) > 1e-5 {
				t.Fatalf("%d-source channel %d = %v, normal %v", count, c, row[0], normal[c][0])
			}
		}
	}
}

func TestComposeDiskWeightedHistEqMatchesNormal(t *testing.T) {
	dir := t.TempDir()
	vals := [][]float32{{.1, .8, .3, .6}, {.2, .7, .4, .9}, {.3, .6, .5, .8}}
	imgs := make([]*models.LoadedImage, 3)
	var channels [3]DiskChannel
	weights := []models.ComposeMixWeight{{BlinkID: models.ComposeChannel1BlinkID, Blue: 1}, {BlinkID: models.ComposeChannel2BlinkID, Green: 1}, {BlinkID: models.ComposeChannel3BlinkID, Red: 1}}
	for i := range vals {
		p := filepath.Join(dir, fmt.Sprintf("hist-%d.bin", i))
		a, err := fitsio.CreateFloat32Artifact(p, 2, 2)
		if err != nil {
			t.Fatal(err)
		}
		_ = a.WriteRow(0, vals[i][:2])
		_ = a.WriteRow(1, vals[i][2:])
		_ = a.Close()
		imgs[i] = &models.LoadedImage{HDU: fitsio.HDU{Data: fitsio.ImageData{Width: 2, Height: 2, Pixels: append([]float32(nil), vals[i]...)}}, Mode: stretch.HistEq, Black: 0, White: 1, Background: 0, Peak: 1, ScaledPeak: 1}
		channels[i] = DiskChannel{ArtifactPath: p, Image: *imgs[i]}
	}
	normal, _, _, err := ComposeWeightedRGBPlanes(context.Background(), imgs, nil, weights)
	if err != nil {
		t.Fatal(err)
	}
	out := [3]string{filepath.Join(dir, "hr.bin"), filepath.Join(dir, "hg.bin"), filepath.Join(dir, "hb.bin")}
	if _, err := ComposeDisk(context.Background(), DiskComposeRequest{Channels: channels, CompositionMode: models.ComposeModeWeighted, MixWeights: weights, Output: out}); err != nil {
		t.Fatal(err)
	}
	for c := range out {
		a, err := fitsio.OpenFloat32ArtifactReadOnly(out[c])
		if err != nil {
			t.Fatal(err)
		}
		row := make([]float32, 2)
		_ = a.ReadRow(0, row)
		_ = a.Close()
		if math.Abs(float64(row[0]-normal[c][0])) > 1e-5 {
			t.Fatalf("HistEq channel %d = %v, normal %v", c, row[0], normal[c][0])
		}
	}
}
