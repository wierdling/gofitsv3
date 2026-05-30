package processing

import (
	"math"
	"testing"
)

func TestDefaultStarMaskSettingsValidate(t *testing.T) {
	settings := DefaultStarMaskSettings()
	if err := settings.Validate(); err != nil {
		t.Fatalf("default settings should validate: %v", err)
	}
}

func TestStarMaskSettingsValidateRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*StarMaskSettings)
		wantErr string
	}{
		{name: "non-positive detection sigma", mutate: func(s *StarMaskSettings) { s.DetectionSigma = 0 }, wantErr: "detection sigma must be > 0"},
		{name: "bad detection mode", mutate: func(s *StarMaskSettings) { s.DetectionMode = "sum" }, wantErr: "detection mode must be median, min, or max"},
		{name: "bad detection merge mode", mutate: func(s *StarMaskSettings) { s.DetectionMergeMode = "or-masks" }, wantErr: "detection merge mode must be shared or per-channel-merged"},
		{name: "small detection area", mutate: func(s *StarMaskSettings) { s.DetectionMinArea = 0 }, wantErr: "detection min area must be >= 1"},
		{name: "too few detected channels", mutate: func(s *StarMaskSettings) { s.MinDetectedChannels = 0 }, wantErr: "min detected channels must be between 1 and 3"},
		{name: "too many detected channels", mutate: func(s *StarMaskSettings) { s.MinDetectedChannels = 4 }, wantErr: "min detected channels must be between 1 and 3"},
		{name: "small seed footprint", mutate: func(s *StarMaskSettings) { s.MinSeedFootprintArea = 0 }, wantErr: "min seed footprint area must be >= 1"},
		{name: "small background tile", mutate: func(s *StarMaskSettings) { s.BackgroundTileSize = 0 }, wantErr: "background tile size must be >= 1"},
		{name: "bad min valid fraction", mutate: func(s *StarMaskSettings) { s.MinValidFraction = 1.5 }, wantErr: "min valid fraction must be between 0 and 1"},
		{name: "negative seed min prominence", mutate: func(s *StarMaskSettings) { s.SeedMinProminence = -0.1 }, wantErr: "seed min prominence must be >= 0"},
		{name: "small suppression radius", mutate: func(s *StarMaskSettings) { s.SuppressionRadius = 0 }, wantErr: "suppression radius must be >= 1"},
		{name: "negative grow radius", mutate: func(s *StarMaskSettings) { s.MaskGrowRadius = -1 }, wantErr: "mask grow radius must be >= 0"},
		{name: "negative max radius", mutate: func(s *StarMaskSettings) { s.MaskMaxRadius = -1 }, wantErr: "mask max radius must be >= 0"},
		{name: "max radius below grow radius", mutate: func(s *StarMaskSettings) { s.MaskGrowRadius = 3; s.MaskMaxRadius = 2 }, wantErr: "mask max radius must be >= mask grow radius"},
		{name: "negative soft edge radius", mutate: func(s *StarMaskSettings) { s.MaskSoftEdgeRadius = -1 }, wantErr: "mask soft edge radius must be >= 0"},
		{name: "small inpaint radius", mutate: func(s *StarMaskSettings) { s.InpaintRadius = 0 }, wantErr: "inpaint radius must be >= 1"},
		{name: "too few shared channels", mutate: func(s *StarMaskSettings) { s.MinSharedChannels = 1 }, wantErr: "min shared channels must be 2 or 3"},
		{name: "too many shared channels", mutate: func(s *StarMaskSettings) { s.MinSharedChannels = 4 }, wantErr: "min shared channels must be 2 or 3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings := DefaultStarMaskSettings()
			tt.mutate(&settings)
			err := settings.Validate()
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("Validate() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestRecombineStarlessRGBNeutralStars(t *testing.T) {
	starless := [][]float32{{10}, {20}, {30}}
	stars := [][]float32{{9}, {3}, {0}}
	alpha := []float32{1}
	out, err := RecombineStarlessRGB(starless, stars, alpha, 1, 1, StarRecombineSettings{StarBrightness: 1, StarSaturation: 0})
	if err != nil {
		t.Fatalf("RecombineStarlessRGB error = %v", err)
	}
	for i, got := range []float32{out[0][0], out[1][0], out[2][0]} {
		want := []float32{19, 29, 39}[i]
		if got != want {
			t.Fatalf("channel %d = %v, want %v", i, got, want)
		}
	}
}

func TestRecombineStarlessRGBLowSaturation(t *testing.T) {
	starless := [][]float32{{10}, {20}, {30}}
	stars := [][]float32{{9}, {3}, {0}}
	alpha := []float32{0.5}
	out, err := RecombineStarlessRGB(starless, stars, alpha, 1, 1, StarRecombineSettings{StarBrightness: 2, StarSaturation: 0.25})
	if err != nil {
		t.Fatalf("RecombineStarlessRGB error = %v", err)
	}
	neutral := float32(9)
	mixed := []float32{
		neutral + 0.25*(9-neutral),
		neutral + 0.25*(3-neutral),
		neutral + 0.25*(0-neutral),
	}
	want := []float32{10 + mixed[0], 20 + mixed[1], 30 + mixed[2]}
	for i, got := range []float32{out[0][0], out[1][0], out[2][0]} {
		if math.Abs(float64(got-want[i])) > 1e-6 {
			t.Fatalf("channel %d = %v, want %v", i, got, want[i])
		}
	}
}

func TestRecombineStarlessRGBWhiteStarsWhenSaturationZero(t *testing.T) {
	starless := [][]float32{{1}, {2}, {3}}
	stars := [][]float32{{2}, {8}, {4}}
	alpha := []float32{1}
	out, err := RecombineStarlessRGB(starless, stars, alpha, 1, 1, StarRecombineSettings{StarBrightness: 1, StarSaturation: 0})
	if err != nil {
		t.Fatalf("RecombineStarlessRGB error = %v", err)
	}
	got := []float32{out[0][0] - starless[0][0], out[1][0] - starless[1][0], out[2][0] - starless[2][0]}
	if got[0] != got[1] || got[1] != got[2] || got[0] != 8 {
		t.Fatalf("star additions = %v, want equal luminance additions of 8", got)
	}
}

func TestRecombineStarlessRGBDesaturatesStrongStarSignalOnly(t *testing.T) {
	starless := [][]float32{
		{100, 100},
		{20, 20},
		{20, 20},
	}
	stars := [][]float32{
		{100, 2},
		{0, 0},
		{0, 0},
	}
	alpha := []float32{1, 1}
	out, err := RecombineStarlessRGB(starless, stars, alpha, 2, 1, StarRecombineSettings{StarBrightness: 1, StarSaturation: 0})
	if err != nil {
		t.Fatalf("RecombineStarlessRGB error = %v", err)
	}
	if out[0][0] != out[1][0] || out[1][0] != out[2][0] {
		t.Fatalf("strong star pixel = %v,%v,%v, want neutral", out[0][0], out[1][0], out[2][0])
	}
	if out[0][1] <= out[1][1] {
		t.Fatalf("weak residual pixel was over-neutralized: %v,%v,%v", out[0][1], out[1][1], out[2][1])
	}
}

func TestCreateStarlessChannelsStoresRawStarDeltaAndRecombineAppliesAlphaOnce(t *testing.T) {
	const w, h = 11, 11
	channels := make([][]float32, 3)
	for i := range channels {
		channels[i] = make([]float32, w*h)
		for p := range channels[i] {
			channels[i][p] = 10
		}
		addSyntheticStar(channels[i], w, h, 5, 5, 90)
	}
	settings := DefaultStarMaskSettings()
	settings.DetectionMergeMode = "shared"
	settings.DetectionPreprocessMode = "none"
	settings.MaskGrowRadius = 1
	settings.MaskMaxRadius = 2
	settings.MaskSoftEdgeRadius = 3
	settings.InpaintRadius = 3
	result, err := CreateStarlessChannels(channels, w, h, settings)
	if err != nil {
		t.Fatalf("CreateStarlessChannels error = %v", err)
	}
	starIdx := -1
	for i, a := range result.AlphaMask {
		if a == 1 && result.Stars[0][i] > 0 {
			starIdx = i
			break
		}
	}
	if starIdx < 0 {
		t.Fatal("expected a hard-masked star-layer pixel")
	}
	rawDelta := channels[0][starIdx] - result.Starless[0][starIdx]
	if rawDelta < 0 {
		rawDelta = 0
	}
	if math.Abs(float64(result.Stars[0][starIdx]-rawDelta)) > 1e-5 {
		t.Fatalf("stored star delta = %v, want raw delta %v", result.Stars[0][starIdx], rawDelta)
	}
	recombined, err := RecombineStarlessRGB(
		[][]float32{{10}, {10}, {10}},
		[][]float32{{8}, {8}, {8}},
		[]float32{0.5},
		1, 1,
		StarRecombineSettings{
			StarBrightness: 1,
			StarSaturation: 1,
		},
	)
	if err != nil {
		t.Fatalf("RecombineStarlessRGB error = %v", err)
	}
	if recombined[0][0] != 14 {
		t.Fatalf("recombined = %v, want one alpha application value 14", recombined[0][0])
	}
}

func TestCreateStarlessChannelsSyntheticRGB(t *testing.T) {
	const w, h = 21, 21
	channels := make([][]float32, 3)
	base := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			base[y*w+x] = float32(10 + x + y)
		}
	}
	peaks := []float32{80, 120, 60}
	for i := range channels {
		channels[i] = append([]float32(nil), base...)
		addSyntheticStar(channels[i], w, h, 10, 10, peaks[i])
	}
	before := make([][]float32, len(channels))
	for i := range channels {
		before[i] = append([]float32(nil), channels[i]...)
	}
	settings := DefaultStarMaskSettings()
	settings.DetectionMergeMode = "shared"
	settings.DetectionPreprocessMode = "none"
	settings.MaskGrowRadius = 2
	settings.MaskSoftEdgeRadius = 2
	settings.InpaintRadius = 3
	result, err := CreateStarlessChannels(channels, w, h, settings)
	if err != nil {
		t.Fatalf("CreateStarlessChannels error = %v", err)
	}
	if result.Width != w || result.Height != h {
		t.Fatalf("result dimensions = %dx%d, want %dx%d", result.Width, result.Height, w, h)
	}
	maskCount := 0
	for _, v := range result.HardMask {
		if v {
			maskCount++
		}
	}
	if maskCount == 0 {
		t.Fatal("expected non-empty hard mask")
	}
	for i := range channels {
		if channels[i][10*w+10] != before[i][10*w+10] {
			t.Fatalf("input channel %d mutated", i)
		}
		if result.Starless[i][10*w+10] >= channels[i][10*w+10] {
			t.Fatalf("starless center for channel %d did not reduce star peak", i)
		}
		bgIdx := 2*w + 2
		if math.Abs(float64(result.Starless[i][bgIdx]-channels[i][bgIdx])) > 0.5 {
			t.Fatalf("background changed too much in channel %d", i)
		}
		for px, v := range result.Stars[i] {
			if v < 0 {
				t.Fatalf("star layer[%d] for channel %d is negative: %v", px, i, v)
			}
			if result.AlphaMask[px] == 0 && v != 0 {
				t.Fatalf("star layer[%d] for channel %d should be zero outside alpha mask", px, i)
			}
		}
	}
}

func TestCreateStarlessChannelsPerChannelMergedRejectsSingleChannelStar(t *testing.T) {
	const w, h = 31, 31
	channels := make([][]float32, 3)
	for i := range channels {
		channels[i] = make([]float32, w*h)
		for p := range channels[i] {
			channels[i][p] = 10
		}
	}
	addSyntheticStar(channels[0], w, h, 15, 15, 120)
	settings := DefaultStarMaskSettings()
	settings.DetectionMergeMode = "per-channel-merged"
	settings.DetectionSigma = 3
	settings.MaskGrowRadius = 2
	settings.MaskMaxRadius = 8
	settings.MaskSoftEdgeRadius = 1
	settings.InpaintRadius = 3
	result, err := CreateStarlessChannels(channels, w, h, settings)
	if err != nil {
		t.Fatalf("CreateStarlessChannels error = %v", err)
	}
	if len(result.ChannelDetections) != 3 {
		t.Fatalf("len(ChannelDetections) = %d, want 3", len(result.ChannelDetections))
	}
	if len(result.Seeds) != 0 {
		t.Fatalf("len(merged seeds) = %d, want single-channel star rejected", len(result.Seeds))
	}
	center := 15*w + 15
	if result.HardMask[center] || result.MergedSeedMask[center] {
		t.Fatal("single-channel star was included in merged shared mask")
	}
	if !result.MergedSeedMaskBeforeChannelFilter[center] || !result.RejectedSingleChannelSeedMask[center] {
		t.Fatal("expected single-channel seed to appear before filtering and in rejected single-channel debug mask")
	}
}

func TestStarlessPerChannelMergedRejectsOneChannelHotPixel(t *testing.T) {
	const w, h = 11, 11
	channels := newFlatStarlessChannels(3, w, h, 10)
	channels[0][5*w+5] = 180
	settings := starlessArtifactFilterTestSettings()
	result, err := CreateStarlessChannels(channels, w, h, settings)
	if err != nil {
		t.Fatalf("CreateStarlessChannels error = %v", err)
	}
	center := 5*w + 5
	if result.HardMask[center] || len(result.Seeds) != 0 {
		t.Fatal("expected one-channel hot pixel rejected from final hard mask")
	}
	if !result.RejectedSmallFootprintSeedMask[center] {
		t.Fatal("expected hot pixel in too-small-footprint debug mask")
	}
}

func TestStarlessPerChannelMergedAcceptsTwoChannelStar(t *testing.T) {
	const w, h = 41, 41
	channels := newFlatStarlessChannels(3, w, h, 10)
	addSyntheticStar(channels[0], w, h, 20, 20, 120)
	addSyntheticStar(channels[1], w, h, 20, 20, 110)
	settings := starlessArtifactFilterTestSettings()
	result, err := CreateStarlessChannels(channels, w, h, settings)
	if err != nil {
		t.Fatalf("CreateStarlessChannels error = %v", err)
	}
	center := 20*w + 20
	if len(result.Seeds) != 1 {
		t.Fatalf("len(seeds) = %d, want accepted two-channel star", len(result.Seeds))
	}
	if !result.HardMask[center] || !result.MergedSeedMaskAfterChannelFilter[center] {
		t.Fatal("expected two-channel star in final hard mask")
	}
	if result.Seeds[0].DetectedChannelCount < 2 {
		t.Fatalf("detected channel count = %d, want at least 2", result.Seeds[0].DetectedChannelCount)
	}
}

func TestStarlessDefaultAcceptsCompactStarInAllChannels(t *testing.T) {
	const w, h = 41, 41
	channels := newFlatStarlessChannels(3, w, h, 10)
	for i := range channels {
		addSyntheticStar(channels[i], w, h, 20, 20, 130)
	}
	settings := DefaultStarMaskSettings()
	settings.InpaintRadius = 3
	result, err := CreateStarlessChannels(channels, w, h, settings)
	if err != nil {
		t.Fatalf("CreateStarlessChannels error = %v", err)
	}
	if len(result.Seeds) == 0 || !result.HardMask[20*w+20] {
		t.Fatalf("expected compact all-channel star accepted, seeds=%d hard=%v", len(result.Seeds), result.HardMask[20*w+20])
	}
}

func TestStarlessPerChannelMergedRejectsOnePixelHitsInAllChannels(t *testing.T) {
	const w, h = 11, 11
	channels := newFlatStarlessChannels(3, w, h, 10)
	for i := range channels {
		channels[i][5*w+5] = 160
	}
	settings := starlessArtifactFilterTestSettings()
	result, err := CreateStarlessChannels(channels, w, h, settings)
	if err != nil {
		t.Fatalf("CreateStarlessChannels error = %v", err)
	}
	center := 5*w + 5
	if len(result.Seeds) != 0 || result.HardMask[center] {
		t.Fatal("expected one-pixel hits to be rejected despite appearing in all channels")
	}
	if !result.RejectedSmallFootprintSeedMask[center] {
		t.Fatal("expected rejected too-small-footprint debug mask at hot pixel")
	}
}

func TestStarlessPerChannelMergedAcceptsSmallRealFootprint(t *testing.T) {
	const w, h = 11, 11
	channels := newFlatStarlessChannels(3, w, h, 10)
	addSyntheticStar(channels[0], w, h, 5, 5, 140)
	addSyntheticStar(channels[1], w, h, 5, 5, 125)
	settings := starlessArtifactFilterTestSettings()
	result, err := CreateStarlessChannels(channels, w, h, settings)
	if err != nil {
		t.Fatalf("CreateStarlessChannels error = %v", err)
	}
	center := 5*w + 5
	if len(result.Seeds) != 1 || !result.HardMask[center] {
		t.Fatalf("expected 3-pixel two-channel source accepted, seeds=%d hard=%v", len(result.Seeds), result.HardMask[center])
	}
	if result.Seeds[0].FootprintArea < 3 {
		t.Fatalf("footprint area = %d, want at least 3", result.Seeds[0].FootprintArea)
	}
}

func TestStarlessPerChannelMergedAcceptsLargeSaturatedStarWithLargeRadius(t *testing.T) {
	const w, h = 61, 61
	channels := newFlatStarlessChannels(3, w, h, 10)
	addSaturatedTestStar(channels[0], w, h, 30, 30, 140)
	addSaturatedTestStar(channels[1], w, h, 30, 30, 130)
	settings := starlessArtifactFilterTestSettings()
	settings.MaskGrowRadius = 3
	settings.MaskMaxRadius = 28
	settings.InpaintRadius = 6
	result, err := CreateStarlessChannels(channels, w, h, settings)
	if err != nil {
		t.Fatalf("CreateStarlessChannels error = %v", err)
	}
	if len(result.Seeds) != 1 {
		t.Fatalf("len(seeds) = %d, want one saturated star", len(result.Seeds))
	}
	if result.Seeds[0].Radius < 12 {
		t.Fatalf("saturated seed radius = %.2f, want large radius", result.Seeds[0].Radius)
	}
	if !result.HardMask[30*w+30] {
		t.Fatal("expected saturated star core in hard mask")
	}
}

func TestStarlessPerChannelMergedRejectsSingleFilterNebulaKnot(t *testing.T) {
	const w, h = 61, 61
	channels := newFlatStarlessChannels(3, w, h, 0.1)
	addNebulaKnot(channels[0], w, h, 30, 30, 1.2, 2.0)
	settings := starlessArtifactFilterTestSettings()
	result, err := CreateStarlessChannels(channels, w, h, settings)
	if err != nil {
		t.Fatalf("CreateStarlessChannels error = %v", err)
	}
	center := 30*w + 30
	if len(result.Seeds) != 0 || result.HardMask[center] {
		t.Fatal("expected single-filter nebula knot rejected by MinDetectedChannels")
	}
	if !result.RejectedSingleChannelSeedMask[center] {
		t.Fatal("expected rejected single-channel debug mask at nebula knot")
	}
}

func TestStarlessRejectsBrightFilamentRidge(t *testing.T) {
	const w, h = 61, 61
	channels := newFlatStarlessChannels(3, w, h, 0.1)
	for c := range channels {
		for y := 26; y <= 34; y++ {
			for x := 8; x < 53; x++ {
				dy := float64(y - 30)
				channels[c][y*w+x] += float32(0.9 * math.Exp(-(dy*dy)/(2*2.0*2.0)))
			}
		}
	}
	settings := DefaultStarMaskSettings()
	settings.DetectionSigma = 3
	settings.InpaintRadius = 3
	result, err := CreateStarlessChannels(channels, w, h, settings)
	if err != nil {
		t.Fatalf("CreateStarlessChannels error = %v", err)
	}
	if len(result.Seeds) != 0 {
		t.Fatalf("len(seeds) = %d (%+v), want bright filament rejected", len(result.Seeds), result.Seeds)
	}
}

func TestDogPreprocessingDoesNotAcceptBroadNebulaRidge(t *testing.T) {
	const w, h = 61, 61
	channels := newFlatStarlessChannels(3, w, h, 0.1)
	for c := range channels {
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				dy := float64(y - 30)
				channels[c][y*w+x] += float32(0.7 * math.Exp(-(dy*dy)/(2*5*5)))
			}
		}
	}
	settings := DefaultStarMaskSettings()
	settings.DetectionPreprocessMode = "dog"
	settings.DetectionSigma = 3
	result, err := CreateStarlessChannels(channels, w, h, settings)
	if err != nil {
		t.Fatalf("CreateStarlessChannels error = %v", err)
	}
	if len(result.Seeds) != 0 {
		t.Fatalf("len(seeds) = %d, want DoG ridge rejected", len(result.Seeds))
	}
}

func TestHardMaskCoverageSmallForSyntheticNebulaField(t *testing.T) {
	const w, h = 81, 81
	channels := newFlatStarlessChannels(3, w, h, 0.1)
	for c := range channels {
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				dx := float64(x - 40)
				dy := float64(y - 40)
				channels[c][y*w+x] += float32(0.9 * math.Exp(-(dx*dx+dy*dy)/(2*18*18)))
			}
		}
	}
	settings := DefaultStarMaskSettings()
	settings.DetectionSigma = 3
	result, err := CreateStarlessChannels(channels, w, h, settings)
	if err != nil {
		t.Fatalf("CreateStarlessChannels error = %v", err)
	}
	if got := maskCoveragePercent(result.HardMask); got > 2 {
		t.Fatalf("hard mask coverage = %.2f%%, want small for synthetic nebula", got)
	}
}

func TestAlphaGateStarLayerMasksDiagnosticStars(t *testing.T) {
	stars := []float32{5, 4, 3}
	alpha := []float32{1, 0.25, 0}
	got := alphaGateStarLayer(stars, alpha)
	want := []float32{5, 1, 0}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("gated[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestEstimateSeedRadiusLimitsLowConfidenceGrowth(t *testing.T) {
	const w, h = 101, 101
	pixels := make([]float32, w*h)
	for i := range pixels {
		pixels[i] = 0.1
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx := float64(x - 50)
			dy := float64(y - 50)
			pixels[y*w+x] += float32(0.7 * math.Exp(-(dx*dx+dy*dy)/(2*18*18)))
		}
	}
	valid := BuildValidityMask(pixels, 0)
	model := EstimateLocalBackgroundMADWithValidity(pixels, valid, w, h, 16)
	seed := StarSeed{X: 50, Y: 50, PeakValue: 0.8, Background: model.Background[50*w+50], Sigma: model.Sigma[50*w+50], Prominence: 0.2, StarLikeScore: 0.01}
	radius := estimateSeedRadius(pixels, valid, w, h, model, seed, 3, 90)
	if radius > 16 {
		t.Fatalf("low-confidence radius = %.2f, want <= 16", radius)
	}
}

func TestStarlessSharedModeStillWorksAndPerChannelMergedFiltersChannels(t *testing.T) {
	const w, h = 41, 41
	channels := newFlatStarlessChannels(3, w, h, 10)
	addSyntheticStar(channels[0], w, h, 20, 20, 120)
	addSyntheticStar(channels[1], w, h, 20, 20, 110)
	settings := starlessArtifactFilterTestSettings()
	settings.DetectionMergeMode = "shared"
	settings.DetectionMode = "median"
	settings.MinSeedFootprintArea = 3
	shared, err := CreateStarlessChannels(channels, w, h, settings)
	if err != nil {
		t.Fatalf("shared CreateStarlessChannels error = %v", err)
	}
	if !shared.HardMask[20*w+20] {
		t.Fatal("expected shared mode to detect aligned two-channel star")
	}

	perChannel := settings
	perChannel.DetectionMergeMode = "per-channel-merged"
	perChannel.MinDetectedChannels = 3
	merged, err := CreateStarlessChannels(channels, w, h, perChannel)
	if err != nil {
		t.Fatalf("per-channel CreateStarlessChannels error = %v", err)
	}
	if merged.HardMask[20*w+20] || len(merged.Seeds) != 0 {
		t.Fatal("expected per-channel mode to apply MinDetectedChannels")
	}
}

func TestMergeStarSeedsUsesLargestRadius(t *testing.T) {
	const w, h = 81, 81
	detections := []ChannelStarDetection{
		{Seeds: []StarSeed{{X: 40, Y: 40, PeakValue: 0.9, Radius: 18}}},
		{Seeds: []StarSeed{{X: 42, Y: 40, PeakValue: 1.0, Radius: 4}}},
		{Seeds: []StarSeed{{X: 39, Y: 41, PeakValue: 0.8, Radius: 6}}},
	}
	seeds := MergeStarSeeds(detections, 10)
	if len(seeds) != 1 {
		t.Fatalf("len(merged seeds) = %d, want 1", len(seeds))
	}
	if seeds[0].Radius != 18 {
		t.Fatalf("merged radius = %.2f, want largest channel radius 18", seeds[0].Radius)
	}
	mask, _ := BuildSeedStarMaskFromRadii(nil, w, h, seeds, 2, 24)
	if !mask[40*w+58] {
		t.Fatal("merged mask did not include the larger one-channel star radius")
	}
}

func TestLocalRawMaskFootprintAreaSmallExamples(t *testing.T) {
	const w, h = 7, 7
	raw := make([]bool, w*h)
	raw[3*w+3] = true
	if got := localRawMaskFootprintArea(raw, nil, w, h, 3, 3, 3); got != 1 {
		t.Fatalf("single pixel footprint = %d, want 1", got)
	}
	raw[3*w+4] = true
	raw[4*w+4] = true
	raw[1*w+1] = true
	if got := localRawMaskFootprintArea(raw, nil, w, h, 3, 3, 3); got != 3 {
		t.Fatalf("connected footprint = %d, want 3", got)
	}
	valid := make([]bool, w*h)
	for i := range valid {
		valid[i] = true
	}
	valid[3*w+4] = false
	if got := localRawMaskFootprintArea(raw, valid, w, h, 3, 3, 3); got != 2 {
		t.Fatalf("valid-clipped footprint = %d, want 2", got)
	}
}

func TestEstimateSeedRadiiMatchesBuildSeedStarMaskRadii(t *testing.T) {
	const w, h = 31, 31
	pixels := make([]float32, w*h)
	for i := range pixels {
		pixels[i] = 0.1
	}
	addSyntheticStar(pixels, w, h, 15, 15, 1)
	valid := BuildValidityMask(pixels, 0)
	model := EstimateLocalBackgroundMADWithValidity(pixels, valid, w, h, 8)
	seeds := []StarSeed{{X: 15, Y: 15, PeakValue: 1, Background: model.Background[15*w+15], Sigma: model.Sigma[15*w+15], Prominence: 0.8}}
	_, paintedSeeds := BuildSeedStarMask(pixels, valid, w, h, model, seeds, 2, 12)
	estimatedSeeds := EstimateSeedRadii(pixels, valid, w, h, model, seeds, 2, 12)
	if len(estimatedSeeds) != len(paintedSeeds) || estimatedSeeds[0].Radius != paintedSeeds[0].Radius {
		t.Fatalf("estimated radii = %+v, painted radii = %+v", estimatedSeeds, paintedSeeds)
	}
}

func TestBuildDebugMasksFalseStillProducesFinalMergedMask(t *testing.T) {
	const w, h = 41, 41
	channels := newFlatStarlessChannels(3, w, h, 10)
	addSyntheticStar(channels[0], w, h, 20, 20, 120)
	addSyntheticStar(channels[1], w, h, 20, 20, 110)
	settings := starlessArtifactFilterTestSettings()
	settings.BuildDebugMasks = false
	result, err := CreateStarlessChannels(channels, w, h, settings)
	if err != nil {
		t.Fatalf("CreateStarlessChannels error = %v", err)
	}
	if !result.HardMask[20*w+20] {
		t.Fatal("expected final merged hard mask to include star")
	}
	for i, det := range result.ChannelDetections {
		if det.HardMask != nil {
			t.Fatalf("channel %d hard debug mask present when BuildDebugMasks=false", i)
		}
	}
}

func TestInpaintRegionFiniteAndOnlyMaskedPixelsChange(t *testing.T) {
	const w, h = 9, 9
	orig := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			orig[y*w+x] = float32(x + y)
		}
	}
	mask := make([]bool, w*h)
	mask[4*w+4] = true
	mask[4*w+5] = true
	settings := DefaultStarMaskSettings()
	settings.InpaintMode = "idw-region"
	settings.InpaintRadius = 2
	out, err := InpaintStarsIDWWithValidity(orig, mask, nil, w, h, settings)
	if err != nil {
		t.Fatalf("InpaintStarsIDWWithValidity error = %v", err)
	}
	for i := range out {
		if !isFiniteStar32(out[i]) {
			t.Fatalf("out[%d] is not finite", i)
		}
		if !mask[i] && out[i] != orig[i] {
			t.Fatalf("unmasked pixel %d changed: %v -> %v", i, orig[i], out[i])
		}
	}
	if out[4*w+4] == orig[4*w+4] && out[4*w+5] == orig[4*w+5] {
		t.Fatal("expected at least one masked pixel to change")
	}
}

func TestFeatherMaskAlphaRangeAndDeterministic(t *testing.T) {
	const w, h = 11, 11
	mask := make([]bool, w*h)
	mask[5*w+5] = true
	a := FeatherMask(mask, w, h, 3)
	b := FeatherMask(mask, w, h, 3)
	for i := range a {
		if a[i] < 0 || a[i] > 1 {
			t.Fatalf("alpha[%d] = %v, want in [0,1]", i, a[i])
		}
		if a[i] != b[i] {
			t.Fatalf("alpha not deterministic at %d: %v vs %v", i, a[i], b[i])
		}
	}
}

func TestBuildValidityMaskUsesFiniteAndFloor(t *testing.T) {
	mask := BuildValidityMask([]float32{-1, 0, 0.01, 1, float32(math.NaN()), float32(math.Inf(1))}, 0)
	want := []bool{false, false, true, true, false, false}
	for i := range want {
		if mask[i] != want[i] {
			t.Fatalf("mask[%d] = %v, want %v", i, mask[i], want[i])
		}
	}
}

func TestBuildValidityMaskNoDataFloorOptional(t *testing.T) {
	pixels := []float32{-1, 0, 0.01, 1, float32(math.NaN()), float32(math.Inf(1))}
	disabled := BuildValidityMaskWithNoDataFloor(pixels, 0, false)
	wantDisabled := []bool{true, true, true, true, false, false}
	for i := range wantDisabled {
		if disabled[i] != wantDisabled[i] {
			t.Fatalf("disabled[%d] = %v, want %v", i, disabled[i], wantDisabled[i])
		}
	}
	enabled := BuildValidityMaskWithNoDataFloor(pixels, 0, true)
	wantEnabled := []bool{false, false, true, true, false, false}
	for i := range wantEnabled {
		if enabled[i] != wantEnabled[i] {
			t.Fatalf("enabled[%d] = %v, want %v", i, enabled[i], wantEnabled[i])
		}
	}
}

func TestBuildStarDetectionImageWithValidityIgnoresNoDataBands(t *testing.T) {
	const w, h = 9, 9
	ch1 := make([]float32, w*h)
	ch2 := make([]float32, w*h)
	ch3 := make([]float32, w*h)
	for i := range ch1 {
		ch1[i], ch2[i], ch3[i] = 10, 10, 10
	}
	addSyntheticStar(ch1, w, h, 6, 4, 60)
	addSyntheticStar(ch2, w, h, 6, 4, 70)
	addSyntheticStar(ch3, w, h, 6, 4, 80)
	for y := 0; y < h; y++ {
		idx := y*w + 2
		ch1[idx], ch2[idx], ch3[idx] = 0, 0, 0
	}
	validMasks := [][]bool{
		BuildValidityMask(ch1, 0),
		BuildValidityMask(ch2, 0),
		BuildValidityMask(ch3, 0),
	}
	shared, err := BuildSharedValidityMask(validMasks, w, h, 2)
	if err != nil {
		t.Fatalf("BuildSharedValidityMask error = %v", err)
	}
	img, err := BuildStarDetectionImageWithChannelValidity([][]float32{ch1, ch2, ch3}, validMasks, shared, w, h, "median")
	if err != nil {
		t.Fatalf("BuildStarDetectionImageWithChannelValidity error = %v", err)
	}
	if img[4*w+2] != 0 {
		t.Fatalf("no-data band pixel = %v, want 0", img[4*w+2])
	}
	if img[4*w+6] <= 0.5 {
		t.Fatalf("star pixel = %v, want bright detection response", img[4*w+6])
	}
}

func TestBuildStarDetectionImageWithChannelValidityUsesPerChannelMasks(t *testing.T) {
	const w, h = 4, 1
	ch1 := []float32{0, 10, 20, 100000}
	ch2 := []float32{0, 0, 0, 0}
	channelValid := [][]bool{
		{true, true, true, false},
		{true, true, true, true},
	}
	shared := []bool{true, true, true, true}
	img, err := BuildStarDetectionImageWithChannelValidity([][]float32{ch1, ch2}, channelValid, shared, w, h, "max")
	if err != nil {
		t.Fatalf("BuildStarDetectionImageWithChannelValidity error = %v", err)
	}
	if img[2] < 0.9 {
		t.Fatalf("valid channel value was scaled down by invalid outlier: img[2] = %v", img[2])
	}
	if img[3] != 0 {
		t.Fatalf("invalid channel outlier contributed to detection image: img[3] = %v", img[3])
	}
}

func TestStarlessPipelineRejectsNoDataBandArtifacts(t *testing.T) {
	const w, h = 15, 15
	channels := make([][]float32, 3)
	for i := range channels {
		channels[i] = make([]float32, w*h)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				channels[i][y*w+x] = float32(12 + x + y)
			}
		}
	}
	addSyntheticStar(channels[0], w, h, 10, 7, 70)
	addSyntheticStar(channels[1], w, h, 10, 7, 90)
	addSyntheticStar(channels[2], w, h, 10, 7, 60)
	for y := 0; y < h; y++ {
		channels[0][y*w+3] = 0
		channels[1][y*w+3] = 0
		channels[2][y*w+3] = 0
	}
	for x := 0; x < w; x++ {
		channels[0][x] = 0
		channels[1][x] = 0
		channels[2][x] = 0
	}

	settings := DefaultStarMaskSettings()
	settings.DetectionMode = "median"
	settings.DetectionSigma = 3
	settings.MaskGrowRadius = 2
	settings.MaskSoftEdgeRadius = 2
	settings.InpaintRadius = 3
	settings.MinSeedFootprintArea = 3
	settings.UseNoDataFloor = true
	settings.NoDataFloor = 0
	settings.MinValidFraction = 0.6

	result, err := CreateStarlessChannels(channels, w, h, settings)
	if err != nil {
		t.Fatalf("CreateStarlessChannels error = %v", err)
	}
	if len(result.Components) != 1 {
		t.Fatalf("len(components) = %d, want 1 real star", len(result.Components))
	}
	bandIdx := 7*w + 3
	if result.SharedValid[bandIdx] {
		t.Fatalf("band pixel should be invalid")
	}
	if result.DetectionImage[bandIdx] != 0 {
		t.Fatalf("band detection value = %v, want 0", result.DetectionImage[bandIdx])
	}
	if result.HardMask[bandIdx] {
		t.Fatalf("band pixel should not be hard-masked")
	}
	if result.AlphaMask[bandIdx] != 0 {
		t.Fatalf("band alpha = %v, want 0", result.AlphaMask[bandIdx])
	}
	for i := range channels {
		if result.Stars[i][bandIdx] != 0 {
			t.Fatalf("stars[%d][band] = %v, want 0", i, result.Stars[i][bandIdx])
		}
		if result.Starless[i][bandIdx] != 0 {
			t.Fatalf("starless[%d][band] = %v, want preserved invalid/no-data", i, result.Starless[i][bandIdx])
		}
	}
	centerIdx := 7*w + 10
	if result.Starless[1][centerIdx] >= channels[1][centerIdx] {
		t.Fatalf("real star peak was not reduced")
	}
}

func TestCreateStarlessChannelsNoDataFloorDisabledPreservesNegativeValidPixels(t *testing.T) {
	const w, h = 9, 9
	channels := make([][]float32, 3)
	for i := range channels {
		channels[i] = make([]float32, w*h)
		for p := range channels[i] {
			channels[i][p] = 10
		}
		channels[i][4*w+4] = -0.5
	}
	settings := DefaultStarMaskSettings()
	settings.UseNoDataFloor = false
	result, err := CreateStarlessChannels(channels, w, h, settings)
	if err != nil {
		t.Fatalf("CreateStarlessChannels error = %v", err)
	}
	if !result.SharedValid[4*w+4] {
		t.Fatal("negative finite pixel should remain valid when no-data floor is disabled")
	}
}

func TestCreateStarlessChannelsNoDataFloorEnabledRejectsClippedBlack(t *testing.T) {
	const w, h = 9, 9
	channels := make([][]float32, 3)
	for i := range channels {
		channels[i] = make([]float32, w*h)
		for p := range channels[i] {
			channels[i][p] = 10
		}
		channels[i][4*w+4] = 0
	}
	settings := DefaultStarMaskSettings()
	settings.UseNoDataFloor = true
	settings.NoDataFloor = 0
	result, err := CreateStarlessChannels(channels, w, h, settings)
	if err != nil {
		t.Fatalf("CreateStarlessChannels error = %v", err)
	}
	if result.SharedValid[4*w+4] {
		t.Fatal("clipped black pixel should be invalid when no-data floor is enabled")
	}
}

func TestDetectStarComponentsRejectsDiffuseBlob(t *testing.T) {
	const w, h = 25, 25
	pixels := make([]float32, w*h)
	for i := range pixels {
		pixels[i] = 10
	}
	for y := 8; y <= 16; y++ {
		for x := 5; x <= 19; x++ {
			dx := float64(x - 12)
			dy := float64(y - 12)
			pixels[y*w+x] += float32(8 + math.Max(0, 6-0.2*(dx*dx+dy*dy)))
		}
	}
	model := EstimateLocalBackgroundMAD(pixels, w, h, 8)
	components, mask, err := DetectStarComponents(pixels, w, h, model, 3, 3, 0)
	if err != nil {
		t.Fatalf("DetectStarComponents error = %v", err)
	}
	if len(components) != 0 {
		t.Fatalf("expected diffuse blob to be rejected, got %d components", len(components))
	}
	for _, v := range mask {
		if v {
			t.Fatal("expected empty accepted mask")
		}
	}
}

func TestDetectStarSeedsRejectsNebulaLikeBroadBlob(t *testing.T) {
	const w, h = 41, 41
	pixels := make([]float32, w*h)
	for i := range pixels {
		pixels[i] = 0.1
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx := float64(x - 20)
			dy := float64(y - 20)
			r2 := dx*dx + dy*dy
			pixels[y*w+x] += float32(0.5 * math.Exp(-r2/(2*10*10)))
		}
	}
	model := BackgroundModel{
		Width:      w,
		Height:     h,
		Background: make([]float32, w*h),
		Sigma:      make([]float32, w*h),
	}
	for i := range model.Background {
		model.Background[i] = 0.1
		model.Sigma[i] = 0.02
	}
	valid := BuildValidityMaskWithNoDataFloor(pixels, 0, false)
	seeds, _, _, _, err := DetectStarSeeds(pixels, valid, w, h, model, 3, 0.05, 10)
	if err != nil {
		t.Fatalf("DetectStarSeeds error = %v", err)
	}
	if len(seeds) != 0 {
		t.Fatalf("len(seeds) = %d, want broad blob rejected", len(seeds))
	}
}

func TestCreateStarlessChannelsPerChannelMergedDoesNotMergeRejectedNebula(t *testing.T) {
	const w, h = 41, 41
	channels := make([][]float32, 3)
	for i := range channels {
		channels[i] = make([]float32, w*h)
		for p := range channels[i] {
			channels[i][p] = 0.1
		}
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx := float64(x - 20)
			dy := float64(y - 20)
			r2 := dx*dx + dy*dy
			channels[0][y*w+x] += float32(0.5 * math.Exp(-r2/(2*10*10)))
		}
	}
	settings := DefaultStarMaskSettings()
	settings.DetectionMergeMode = "per-channel-merged"
	settings.DetectionSigma = 3
	settings.SeedMinProminence = 0.05
	result, err := CreateStarlessChannels(channels, w, h, settings)
	if err != nil {
		t.Fatalf("CreateStarlessChannels error = %v", err)
	}
	if len(result.Seeds) != 0 {
		t.Fatalf("len(merged seeds) = %d, want rejected nebula not merged", len(result.Seeds))
	}
	for _, v := range result.HardMask {
		if v {
			t.Fatal("expected empty hard mask for rejected nebula")
		}
	}
}

func TestCreateStarlessChannelsLargeSaturatedStarAccepted(t *testing.T) {
	const w, h = 121, 121
	channels := make([][]float32, 3)
	for i := range channels {
		channels[i] = make([]float32, w*h)
		for p := range channels[i] {
			channels[i][p] = 20
		}
	}
	addLargeSaturatedStar := func(pixels []float32, peak float32) {
		cx, cy := 60, 60
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				dx := float64(x - cx)
				dy := float64(y - cy)
				r := math.Sqrt(dx*dx + dy*dy)
				if r <= 40 {
					boost := peak * float32(math.Max(0, 1.0-r/40.0))
					if math.Abs(dx) <= 1 || math.Abs(dy) <= 1 {
						boost += peak * 0.15
					}
					pixels[y*w+x] += boost
				}
			}
		}
	}
	addLargeSaturatedStar(channels[0], 120)
	addLargeSaturatedStar(channels[1], 140)
	addLargeSaturatedStar(channels[2], 100)
	settings := DefaultStarMaskSettings()
	settings.DetectionMergeMode = "shared"
	settings.DetectionPreprocessMode = "none"
	settings.DetectionSigma = 3
	settings.MaskGrowRadius = 3
	settings.MaskMaxRadius = 24
	settings.InpaintRadius = 6
	result, err := CreateStarlessChannels(channels, w, h, settings)
	if err != nil {
		t.Fatalf("CreateStarlessChannels error = %v", err)
	}
	if len(result.Components) == 0 {
		t.Fatal("expected large saturated star to be accepted")
	}
	if len(result.Seeds) != 1 {
		t.Fatalf("len(seeds) = %d, want 1 saturated large-star seed", len(result.Seeds))
	}
	if result.Seeds[0].Radius < 20 {
		t.Fatalf("saturated seed radius = %.2f, want at least 20", result.Seeds[0].Radius)
	}
	maskedCore := false
	for y := 57; y <= 63 && !maskedCore; y++ {
		for x := 57; x <= 63; x++ {
			idx := y*w + x
			if result.HardMask[idx] && result.AlphaMask[idx] > 0 {
				maskedCore = true
				break
			}
		}
	}
	if !maskedCore {
		t.Fatal("expected bright core neighborhood of large star to be masked")
	}
}

func TestCreateStarlessChannelsDetectsStarsAcrossSizeContinuum(t *testing.T) {
	const w, h = 220, 180
	channels := newFlatStarlessChannels(3, w, h, 0.1)
	addStar := func(pixels []float32, cx, cy int, radius float64, peak float32, saturatedCore float64) {
		for y := maxStarInt(0, int(math.Floor(float64(cy)-radius-2))); y <= minInt(h-1, int(math.Ceil(float64(cy)+radius+2))); y++ {
			for x := maxStarInt(0, int(math.Floor(float64(cx)-radius-2))); x <= minInt(w-1, int(math.Ceil(float64(cx)+radius+2))); x++ {
				dx := float64(x - cx)
				dy := float64(y - cy)
				r := math.Sqrt(dx*dx + dy*dy)
				if r > radius {
					continue
				}
				profile := math.Exp(-(r * r) / (2 * (radius / 2.7) * (radius / 2.7)))
				boost := float32(float64(peak) * profile)
				if saturatedCore > 0 && r <= saturatedCore {
					boost = peak
				}
				pixels[y*w+x] += boost
			}
		}
	}
	addNebulaKnot := func(pixels []float32) {
		for y := 10; y < 70; y++ {
			for x := 145; x < 210; x++ {
				dx := float64(x - 176)
				dy := float64(y - 39)
				r2 := dx*dx/36 + dy*dy/280
				pixels[y*w+x] += float32(0.7 * math.Exp(-r2/2))
			}
		}
	}
	for _, ch := range channels {
		addStar(ch, 26, 26, 4, 2.8, 0)
		addStar(ch, 82, 48, 16, 5.5, 3)
		addStar(ch, 94, 124, 56, 8.0, 12)
		addNebulaKnot(ch)
	}

	settings := DefaultStarMaskSettings()
	settings.DetectionMergeMode = "per-channel-merged"
	settings.DetectionPreprocessMode = "none"
	settings.DetectionSigma = 3
	settings.SeedMinProminence = 0.03
	settings.SuppressionRadius = 8
	settings.MaskGrowRadius = 2
	settings.MaskMaxRadius = 70
	settings.InpaintRadius = 8
	settings.MinDetectedChannels = 2
	settings.MinSeedFootprintArea = 3
	result, err := CreateStarlessChannels(channels, w, h, settings)
	if err != nil {
		t.Fatalf("CreateStarlessChannels error = %v", err)
	}
	targets := []struct {
		name      string
		x, y      int
		minRadius float64
	}{
		{name: "small star", x: 26, y: 26, minRadius: 2},
		{name: "medium star", x: 82, y: 48, minRadius: 8},
		{name: "large star", x: 94, y: 124, minRadius: 40},
	}
	for _, target := range targets {
		seed, ok := nearestSeed(result.Seeds, target.x, target.y, 8)
		if !ok {
			t.Fatalf("missing %s near (%d,%d); seeds=%+v", target.name, target.x, target.y, result.Seeds)
		}
		if seed.Radius < target.minRadius {
			t.Fatalf("%s radius = %.2f, want at least %.2f", target.name, seed.Radius, target.minRadius)
		}
	}
	nebulaIdx := 176 + 39*w
	if result.HardMask[nebulaIdx] {
		t.Fatal("expected elongated nebula knot center to remain unmasked")
	}
}

func TestDetectStarSeedsCollapsesSaturatedPlateau(t *testing.T) {
	const w, h = 25, 25
	pixels := make([]float32, w*h)
	for i := range pixels {
		pixels[i] = 0.1
	}
	for y := 10; y <= 12; y++ {
		for x := 9; x <= 11; x++ {
			pixels[y*w+x] = 1
		}
	}
	model := BackgroundModel{
		Width:      w,
		Height:     h,
		Background: make([]float32, w*h),
		Sigma:      make([]float32, w*h),
	}
	for i := range model.Background {
		model.Background[i] = 0.1
		model.Sigma[i] = 0.02
	}
	valid := BuildValidityMask(pixels, 0)
	seeds, rawMask, acceptedMask, rejectedMask, err := DetectStarSeeds(pixels, valid, w, h, model, 3, 0.03, 10)
	if err != nil {
		t.Fatalf("DetectStarSeeds error = %v", err)
	}
	if len(seeds) != 1 {
		t.Fatalf("len(seeds) = %d, want 1", len(seeds))
	}
	if seeds[0].X != 10 || seeds[0].Y != 11 {
		t.Fatalf("seed = (%d,%d), want weighted plateau centroid (10,11)", seeds[0].X, seeds[0].Y)
	}
	if !acceptedMask[11*w+10] {
		t.Fatal("expected accepted seed mask at plateau centroid")
	}
	for y := 10; y <= 12; y++ {
		for x := 9; x <= 11; x++ {
			if !rawMask[y*w+x] {
				t.Fatalf("expected raw candidate mask at plateau pixel (%d,%d)", x, y)
			}
		}
	}
	for i, v := range rejectedMask {
		if v {
			t.Fatalf("unexpected rejected plateau seed at %d", i)
		}
	}
}

func TestBuildDilatedStarMaskIsNotBoundingBoxFill(t *testing.T) {
	components := []DetectedStarComponent{{CentroidX: 10, CentroidY: 10, Area: 49, PeakValue: 100}}
	mask, err := BuildDilatedStarMask(25, 25, components, nil, 2.0, 0.0, 9)
	if err != nil {
		t.Fatalf("BuildDilatedStarMask error = %v", err)
	}
	if !mask[10*25+10] {
		t.Fatal("expected centroid masked")
	}
	if mask[3*25+3] {
		t.Fatal("unexpected far corner mask pixel suggests box fill")
	}
	if mask[10*25+19] && mask[19*25+10] && mask[19*25+19] {
		t.Fatal("unexpected square corner coverage")
	}
}

func TestRecombineStarlessRGBSkipsInvalidRegions(t *testing.T) {
	starless := [][]float32{{0, 10}, {0, 20}, {0, 30}}
	stars := [][]float32{{100, 9}, {0, 3}, {0, 0}}
	alpha := []float32{1, 1}
	valid := []bool{false, true}
	out, err := RecombineStarlessRGB(starless, stars, alpha, 2, 1, StarRecombineSettings{
		StarBrightness: 1,
		StarSaturation: 1,
		ValidMask:      valid,
	})
	if err != nil {
		t.Fatalf("RecombineStarlessRGB error = %v", err)
	}
	if out[0][0] != 0 || out[1][0] != 0 || out[2][0] != 0 {
		t.Fatalf("invalid pixel was recombined: %v %v %v", out[0][0], out[1][0], out[2][0])
	}
	if out[0][1] <= starless[0][1] {
		t.Fatalf("valid star pixel did not brighten")
	}
}

func TestEstimateLocalBackgroundMADGradientAndStars(t *testing.T) {
	const w, h, tile = 16, 16, 8
	pixels := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			pixels[y*w+x] = float32(10 + x + 2*y)
		}
	}
	addStar := func(cx, cy int, peak float32) {
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				x, y := cx+dx, cy+dy
				if x < 0 || x >= w || y < 0 || y >= h {
					continue
				}
				boost := peak / 4
				if dx == 0 && dy == 0 {
					boost = peak
				}
				pixels[y*w+x] += boost
			}
		}
	}
	addStar(3, 3, 120)
	addStar(12, 12, 150)
	pixels[1] = float32(math.NaN())
	pixels[w+1] = float32(math.Inf(1))
	model := EstimateLocalBackgroundMAD(pixels, w, h, tile)
	if model.Width != w || model.Height != h || len(model.Background) != w*h || len(model.Sigma) != w*h {
		t.Fatalf("unexpected model shape")
	}
	if model.Background[0] >= model.Background[(h-1)*w+(w-1)] {
		t.Fatalf("expected gradient-tracking background")
	}
	if model.Background[3*w+3] >= pixels[3*w+3] {
		t.Fatalf("star pixel background should stay below star intensity")
	}
	for i, sigma := range model.Sigma {
		if sigma < localSigmaFloor || math.IsNaN(float64(sigma)) || math.IsInf(float64(sigma), 0) {
			t.Fatalf("sigma[%d] invalid: %v", i, sigma)
		}
	}
}

func TestDetectStarComponentsGaussianLikeSources(t *testing.T) {
	const w, h = 20, 20
	pixels := make([]float32, w*h)
	for i := range pixels {
		pixels[i] = 10
	}
	addGaussianLike := func(cx, cy int, peak float32) {
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				x, y := cx+dx, cy+dy
				if x < 0 || x >= w || y < 0 || y >= h {
					continue
				}
				v := peak / 5
				if absInt(dx)+absInt(dy) == 1 {
					v = peak / 2
				}
				if dx == 0 && dy == 0 {
					v = peak
				}
				pixels[y*w+x] += v
			}
		}
	}
	addGaussianLike(5, 6, 60)
	addGaussianLike(14, 13, 80)
	before := append([]float32(nil), pixels...)
	model := EstimateLocalBackgroundMAD(pixels, w, h, 10)
	components, mask, err := DetectStarComponents(pixels, w, h, model, 5, 3, 20)
	if err != nil || len(components) != 2 || len(mask) != w*h {
		t.Fatalf("unexpected component results: %v %d", err, len(components))
	}
	for i := range pixels {
		if pixels[i] != before[i] {
			t.Fatalf("input mutated at %d", i)
		}
	}
}

func TestDetectStarComponentsTouchingEdges(t *testing.T) {
	const w, h = 10, 10
	pixels := make([]float32, w*h)
	for i := range pixels {
		pixels[i] = 5
	}
	for y := 0; y <= 1; y++ {
		for x := 0; x <= 1; x++ {
			pixels[y*w+x] = 30
		}
	}
	model := EstimateLocalBackgroundMAD(pixels, w, h, 5)
	components, mask, err := DetectStarComponents(pixels, w, h, model, 5, 1, 10)
	if err != nil || len(components) != 1 {
		t.Fatalf("unexpected edge component result")
	}
	if !mask[0] || !mask[1] || !mask[w] || !mask[w+1] {
		t.Fatalf("expected edge component pixels in mask")
	}
}

func TestDetectStarComponentsAreaRejection(t *testing.T) {
	const w, h = 8, 8
	pixels := make([]float32, w*h)
	for i := range pixels {
		pixels[i] = 5
	}
	pixels[3*w+3] = 40
	model := EstimateLocalBackgroundMAD(pixels, w, h, 4)
	components, mask, err := DetectStarComponents(pixels, w, h, model, 5, 2, 0)
	if err != nil || len(components) != 0 {
		t.Fatalf("unexpected rejection result")
	}
	for _, v := range mask {
		if v {
			t.Fatalf("expected empty mask")
		}
	}
}

func TestBuildDilatedStarMaskCenteredRadius(t *testing.T) {
	components := []DetectedStarComponent{{CentroidX: 5, CentroidY: 5, Area: 1, PeakValue: 0}}
	mask, err := BuildDilatedStarMask(11, 11, components, nil, 1.0, 0.0, 2)
	if err != nil {
		t.Fatalf("BuildDilatedStarMask error = %v", err)
	}
	count := 0
	for _, v := range mask {
		if v {
			count++
		}
	}
	if count != 13 {
		t.Fatalf("dilated pixel count = %d, want 13", count)
	}
}

func TestBuildDilatedStarMaskEdgeClipping(t *testing.T) {
	components := []DetectedStarComponent{{CentroidX: 0.2, CentroidY: 0.2, Area: 9, PeakValue: 100}}
	mask, err := BuildDilatedStarMask(6, 6, components, nil, 1.0, 1.0, 2)
	if err != nil {
		t.Fatalf("BuildDilatedStarMask error = %v", err)
	}
	if !mask[0] || !mask[1] || !mask[6] || mask[len(mask)-1] {
		t.Fatalf("unexpected edge clipping result")
	}
}

func TestFeatherMaskSinglePixel(t *testing.T) {
	mask := make([]bool, 25)
	mask[12] = true
	alpha := FeatherMask(mask, 5, 5, 2)
	if alpha[12] != 1 || alpha[13] <= 0 || alpha[13] >= 1 || alpha[0] != 0 {
		t.Fatalf("unexpected feather values")
	}
}

func TestFeatherMaskNearEdge(t *testing.T) {
	mask := make([]bool, 16)
	mask[0] = true
	alpha := FeatherMask(mask, 4, 4, 2)
	if alpha[0] != 1 || alpha[1] <= 0 || alpha[1] >= 1 || alpha[15] != 0 {
		t.Fatalf("unexpected edge feather values")
	}
}

func TestFeatherMaskRadiusZero(t *testing.T) {
	mask := make([]bool, 9)
	mask[4] = true
	alpha := FeatherMask(mask, 3, 3, 0)
	for i, v := range alpha {
		if i == 4 {
			if v != 1 {
				t.Fatalf("center alpha = %v, want 1", v)
			}
		} else if v != 0 {
			t.Fatalf("alpha[%d] = %v, want 0", i, v)
		}
	}
}

func TestInpaintStarsIDWFlatBackground(t *testing.T) {
	original := make([]float32, 25)
	for i := range original {
		original[i] = 10
	}
	original[12] = 100
	mask := make([]bool, 25)
	mask[12] = true
	before := append([]float32(nil), original...)
	out, err := InpaintStarsIDW(original, mask, 5, 5, DefaultStarMaskSettings())
	if err != nil || out[12] != 10 {
		t.Fatalf("unexpected flat inpaint result")
	}
	for i := range original {
		if original[i] != before[i] {
			t.Fatalf("original mutated at %d", i)
		}
	}
}

func TestInpaintStarsIDWGradientBackground(t *testing.T) {
	original := make([]float32, 49)
	for y := 0; y < 7; y++ {
		for x := 0; x < 7; x++ {
			original[y*7+x] = float32(10 + x + 2*y)
		}
	}
	original[24] = 200
	mask := make([]bool, 49)
	mask[24] = true
	settings := DefaultStarMaskSettings()
	settings.InpaintRadius = 2
	out, err := InpaintStarsIDW(original, mask, 7, 7, settings)
	if err != nil || math.Abs(float64(out[24]-19)) > 1.5 {
		t.Fatalf("unexpected gradient inpaint result: %v %v", err, out[24])
	}
}

func TestInpaintStarsIDWMaskAtEdge(t *testing.T) {
	original := make([]float32, 25)
	for y := 0; y < 5; y++ {
		for x := 0; x < 5; x++ {
			original[y*5+x] = float32(20 + x + y)
		}
	}
	original[0] = 100
	mask := make([]bool, 25)
	mask[0] = true
	settings := DefaultStarMaskSettings()
	settings.InpaintRadius = 1
	out, err := InpaintStarsIDW(original, mask, 5, 5, settings)
	if err != nil || !isFiniteStar32(out[0]) || out[0] == original[0] {
		t.Fatalf("unexpected edge inpaint result")
	}
}

func TestNormalizeRobustIgnoresNaNInfAndDoesNotMutate(t *testing.T) {
	input := []float32{1, 2, float32(math.NaN()), 3, float32(math.Inf(1)), 100}
	before := append([]float32(nil), input...)
	got := NormalizeRobust(input)
	if len(got) != len(input) {
		t.Fatalf("len(got) = %d, want %d", len(got), len(input))
	}
	for i := range input {
		orig, prev := input[i], before[i]
		if math.IsNaN(float64(orig)) && math.IsNaN(float64(prev)) {
			continue
		}
		if orig != prev {
			t.Fatalf("input mutated at %d", i)
		}
	}
	if got[0] != 0 || got[2] != 0 || got[4] != 0 || got[5] != 1 {
		t.Fatalf("unexpected normalization result: %v", got)
	}
}

func TestBuildStarDetectionImageMedianDefault(t *testing.T) {
	got, err := BuildStarDetectionImage([][]float32{{0, 1, 2, 10}, {0, 2, 4, 20}, {0, 3, 6, 30}}, 2, 2, "")
	if err != nil || len(got) != 4 || got[0] != 0 || got[3] != 1 {
		t.Fatalf("unexpected detection image: %v %v", err, got)
	}
}

func TestBuildStarDetectionImageMinAndMaxModes(t *testing.T) {
	minImg, err := BuildStarDetectionImage([][]float32{{0, 0.1, 0.2, 10}, {0, 0.4, 0.5, 10}}, 2, 2, "min")
	if err != nil {
		t.Fatalf("min mode error = %v", err)
	}
	maxImg, err := BuildStarDetectionImage([][]float32{{0, 0.1, 0.2, 10}, {0, 0.4, 0.5, 10}}, 2, 2, "max")
	if err != nil {
		t.Fatalf("max mode error = %v", err)
	}
	for i := range minImg {
		if minImg[i] > maxImg[i] {
			t.Fatalf("min > max at %d", i)
		}
	}
}

func TestBuildStarDetectionImageRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		channels      [][]float32
		width, height int
		mode          string
	}{
		{channels: [][]float32{{1, 2}, {3, 4}}, width: 0, height: 1},
		{channels: [][]float32{}, width: 2, height: 2},
		{channels: [][]float32{{1}, {1}, {1}, {1}}, width: 1, height: 1},
		{channels: [][]float32{{1, 2, 3, 4}, {1, 2, 3}}, width: 2, height: 2},
		{channels: [][]float32{{1, 2, 3, 4}, {1, 2, 3, 4}}, width: 2, height: 2, mode: "sum"},
	}
	for _, tt := range tests {
		if _, err := BuildStarDetectionImage(tt.channels, tt.width, tt.height, tt.mode); err == nil {
			t.Fatal("expected error")
		}
	}
}

func addSyntheticStar(pixels []float32, width, height, cx, cy int, peak float32) {
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			x, y := cx+dx, cy+dy
			if x < 0 || x >= width || y < 0 || y >= height {
				continue
			}
			boost := peak / 5
			if absInt(dx)+absInt(dy) == 1 {
				boost = peak / 2
			}
			if dx == 0 && dy == 0 {
				boost = peak
			}
			pixels[y*width+x] += boost
		}
	}
}

func newFlatStarlessChannels(n, width, height int, value float32) [][]float32 {
	channels := make([][]float32, n)
	for i := range channels {
		channels[i] = make([]float32, width*height)
		for p := range channels[i] {
			channels[i][p] = value
		}
	}
	return channels
}

func starlessArtifactFilterTestSettings() StarMaskSettings {
	settings := DefaultStarMaskSettings()
	settings.DetectionMergeMode = "per-channel-merged"
	settings.DetectionPreprocessMode = "none"
	settings.DetectionSigma = 3
	settings.SeedMinProminence = 0.03
	settings.SuppressionRadius = 6
	settings.MaskGrowRadius = 2
	settings.MaskMaxRadius = 12
	settings.MaskSoftEdgeRadius = 1
	settings.InpaintRadius = 3
	settings.MinDetectedChannels = 2
	settings.MinSeedFootprintArea = 3
	return settings
}

func addThreePixelSource(pixels []float32, width, cx, cy int, peak float32) {
	pixels[cy*width+cx] = peak
	pixels[cy*width+cx-1] = peak * 0.65
	pixels[(cy-1)*width+cx] = peak * 0.6
}

func nearestSeed(seeds []StarSeed, x, y int, maxDistance float64) (StarSeed, bool) {
	maxDist2 := maxDistance * maxDistance
	bestDist2 := math.Inf(1)
	var best StarSeed
	for _, seed := range seeds {
		dx := float64(seed.X - x)
		dy := float64(seed.Y - y)
		dist2 := dx*dx + dy*dy
		if dist2 <= maxDist2 && dist2 < bestDist2 {
			best = seed
			bestDist2 = dist2
		}
	}
	return best, bestDist2 < math.Inf(1)
}

func addSaturatedTestStar(pixels []float32, width, height, cx, cy int, peak float32) {
	for y := cy - 10; y <= cy+10; y++ {
		if y < 0 || y >= height {
			continue
		}
		for x := cx - 10; x <= cx+10; x++ {
			if x < 0 || x >= width {
				continue
			}
			dx := float64(x - cx)
			dy := float64(y - cy)
			r2 := dx*dx + dy*dy
			pixels[y*width+x] += peak * 0.2 * float32(math.Exp(-r2/(2*4.0*4.0)))
			if math.Abs(dx) <= 2 && math.Abs(dy) <= 2 {
				pixels[y*width+x] += peak
			}
		}
	}
}

func addNebulaKnot(pixels []float32, width, height, cx, cy int, peak, sigma float64) {
	for y := cy - 8; y <= cy+8; y++ {
		if y < 0 || y >= height {
			continue
		}
		for x := cx - 8; x <= cx+8; x++ {
			if x < 0 || x >= width {
				continue
			}
			dx := float64(x - cx)
			dy := float64(y - cy)
			pixels[y*width+x] += float32(peak * math.Exp(-(dx*dx+dy*dy)/(2*sigma*sigma)))
		}
	}
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
