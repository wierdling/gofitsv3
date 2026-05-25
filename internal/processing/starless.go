package processing

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"gofitsv3/internal/debuglog"
)

const localSigmaFloor = 1e-6

type BackgroundModel struct {
	Width      int
	Height     int
	Background []float32
	Sigma      []float32
}

type DetectedStarComponent struct {
	MinX         int
	MinY         int
	MaxX         int
	MaxY         int
	Area         int
	ValidArea    int
	BBoxWidth    int
	BBoxHeight   int
	FillRatio    float64
	AspectRatio  float64
	CentroidX    float64
	CentroidY    float64
	PeakValue    float32
	MeanValue    float32
	PeakContrast float64
	CoreArea     int
	CompactCore  bool
}

type StarSeed struct {
	X                    int
	Y                    int
	PeakValue            float32
	Background           float32
	Sigma                float32
	RingMedian           float32
	CoreMean             float32
	Prominence           float32
	StarLikeScore        float32
	Radius               float64
	FootprintArea        int
	SourceChannels       uint8
	DetectedChannelCount int
	MaxRadius            float64
	MaxPeakValue         float32
	SourceSeedCount      int
}

type ChannelStarDetection struct {
	DetectionImage                []float32
	Background                    []float32
	Sigma                         []float32
	RawCandidateMask              []bool
	AcceptedSeedMask              []bool
	RejectedSeedMask              []bool
	RejectedLowProminenceSeedMask []bool
	RejectedDiffuseSeedMask       []bool
	RejectedSmallSeedMask         []bool
	HardMask                      []bool
	Seeds                         []StarSeed
}

type StarMaskSettings struct {
	DetectionSigma          float64
	DetectionMode           string
	DetectionPreprocessMode string
	DetectionMergeMode      string
	DetectionMinArea        int
	MinDetectedChannels     int
	MinSeedFootprintArea    int
	BuildDebugMasks         bool
	InpaintMode             string
	BackgroundTileSize      int
	UseNoDataFloor          bool
	NoDataFloor             float32
	MinValidFraction        float64
	SeedMinProminence       float64
	SuppressionRadius       int
	MaskGrowRadius          int
	MaskMaxRadius           int
	MaskSoftEdgeRadius      int
	InpaintRadius           int
	MinSharedChannels       int
}

type StarMaskResult struct {
	Width          int
	Height         int
	Settings       StarMaskSettings
	DetectionImage []float32
	Mask           []bool
	Components     []StarComponent
}

type StarlessResult struct {
	Width                             int
	Height                            int
	Settings                          StarMaskSettings
	DetectionImage                    []float32
	Background                        []float32
	Sigma                             []float32
	SharedValid                       []bool
	RecombineValid                    []bool
	ChannelValid                      [][]bool
	RawCandidateMask                  []bool
	AcceptedSeedMask                  []bool
	RejectedSeedMask                  []bool
	RejectedLowProminenceSeedMask     []bool
	RejectedDiffuseSeedMask           []bool
	ChannelDetections                 []ChannelStarDetection
	MergedSeedMask                    []bool
	MergedSeedMaskBeforeChannelFilter []bool
	MergedSeedMaskAfterChannelFilter  []bool
	RejectedSingleChannelSeedMask     []bool
	RejectedSmallFootprintSeedMask    []bool
	HardMask                          []bool
	AlphaMask                         []float32
	Seeds                             []StarSeed
	Components                        []DetectedStarComponent
	Starless                          [][]float32
	Stars                             [][]float32
}

type StarRecombineSettings struct {
	StarBrightness float32
	StarSaturation float32
	ValidMask      []bool
}

func DefaultStarMaskSettings() StarMaskSettings {
	return StarMaskSettings{
		DetectionSigma:          5.5,
		DetectionMode:           "min",
		DetectionPreprocessMode: "none",
		DetectionMergeMode:      "per-channel-merged",
		DetectionMinArea:        3,
		MinDetectedChannels:     2,
		MinSeedFootprintArea:    5,
		BuildDebugMasks:         false,
		InpaintMode:             "idw-region",
		BackgroundTileSize:      96,
		UseNoDataFloor:          false,
		NoDataFloor:             0,
		MinValidFraction:        0.5,
		SeedMinProminence:       0.05,
		SuppressionRadius:       10,
		MaskGrowRadius:          3,
		MaskMaxRadius:           60,
		MaskSoftEdgeRadius:      2,
		InpaintRadius:           12,
		MinSharedChannels:       2,
	}
}

func (s StarMaskSettings) Validate() error {
	if s.DetectionSigma <= 0 {
		return fmt.Errorf("detection sigma must be > 0")
	}
	mode := strings.ToLower(strings.TrimSpace(s.DetectionMode))
	if mode == "" {
		mode = "median"
	}
	if mode != "median" && mode != "min" && mode != "max" {
		return fmt.Errorf("detection mode must be median, min, or max")
	}
	preprocessMode := strings.ToLower(strings.TrimSpace(s.DetectionPreprocessMode))
	if preprocessMode == "" {
		preprocessMode = "none"
	}
	if preprocessMode != "none" && preprocessMode != "dog" {
		return fmt.Errorf("detection preprocess mode must be none or dog")
	}
	mergeMode := strings.ToLower(strings.TrimSpace(s.DetectionMergeMode))
	if mergeMode == "" {
		mergeMode = "shared"
	}
	if mergeMode != "shared" && mergeMode != "per-channel-merged" {
		return fmt.Errorf("detection merge mode must be shared or per-channel-merged")
	}
	if s.DetectionMinArea < 1 {
		return fmt.Errorf("detection min area must be >= 1")
	}
	if s.MinDetectedChannels < 1 || s.MinDetectedChannels > 3 {
		return fmt.Errorf("min detected channels must be between 1 and 3")
	}
	if s.MinSeedFootprintArea < 1 {
		return fmt.Errorf("min seed footprint area must be >= 1")
	}
	inpaintMode := strings.ToLower(strings.TrimSpace(s.InpaintMode))
	if inpaintMode == "" {
		inpaintMode = "idw-region"
	}
	if inpaintMode != "idw-pixel" && inpaintMode != "idw-region" {
		return fmt.Errorf("inpaint mode must be idw-pixel or idw-region")
	}
	if s.BackgroundTileSize < 1 {
		return fmt.Errorf("background tile size must be >= 1")
	}
	if s.MinValidFraction < 0 || s.MinValidFraction > 1 {
		return fmt.Errorf("min valid fraction must be between 0 and 1")
	}
	if s.SeedMinProminence < 0 {
		return fmt.Errorf("seed min prominence must be >= 0")
	}
	if s.SuppressionRadius < 1 {
		return fmt.Errorf("suppression radius must be >= 1")
	}
	if s.MaskGrowRadius < 0 {
		return fmt.Errorf("mask grow radius must be >= 0")
	}
	if s.MaskMaxRadius < 0 {
		return fmt.Errorf("mask max radius must be >= 0")
	}
	if s.MaskMaxRadius > 0 && s.MaskMaxRadius < s.MaskGrowRadius {
		return fmt.Errorf("mask max radius must be >= mask grow radius")
	}
	if s.MaskSoftEdgeRadius < 0 {
		return fmt.Errorf("mask soft edge radius must be >= 0")
	}
	if s.InpaintRadius < 1 {
		return fmt.Errorf("inpaint radius must be >= 1")
	}
	if s.MinSharedChannels < 2 || s.MinSharedChannels > 3 {
		return fmt.Errorf("min shared channels must be 2 or 3")
	}
	return nil
}

type StarComponent struct {
	Width    int
	Height   int
	Original []float32
	Starless []float32
	Stars    []float32
}

func CreateStarlessChannels(channels [][]float32, width, height int, settings StarMaskSettings) (*StarlessResult, error) {
	debuglog.Log(fmt.Sprintf("CreateStarlessChannels: starting (%d channels, %dx%d)", len(channels), width, height))
	defer debuglog.Log("CreateStarlessChannels: finished")
	logPhase := func(name string, start time.Time) {
		debuglog.Log(fmt.Sprintf("CreateStarlessChannels: %s took %s", name, time.Since(start)))
	}
	if err := settings.Validate(); err != nil {
		debuglog.Log(fmt.Sprintf("CreateStarlessChannels: invalid settings: %v", err))
		return nil, err
	}
	if err := validateChannelSet(channels, width, height); err != nil {
		debuglog.Log(fmt.Sprintf("CreateStarlessChannels: invalid channel set: %v", err))
		return nil, err
	}
	detectionMode := strings.TrimSpace(settings.DetectionMode)
	if detectionMode == "" {
		detectionMode = "median"
	}
	detectionMergeMode := strings.ToLower(strings.TrimSpace(settings.DetectionMergeMode))
	if detectionMergeMode == "" {
		detectionMergeMode = "shared"
	}
	inpaintMode := strings.ToLower(strings.TrimSpace(settings.InpaintMode))
	if inpaintMode == "" {
		inpaintMode = "idw-region"
	}
	phaseStart := time.Now()
	channelValid := make([][]bool, len(channels))
	for i, ch := range channels {
		channelValid[i] = BuildValidityMaskWithNoDataFloor(ch, settings.NoDataFloor, settings.UseNoDataFloor)
	}
	sharedValid, err := BuildSharedValidityMask(channelValid, width, height, settings.MinSharedChannels)
	if err != nil {
		debuglog.Log(fmt.Sprintf("CreateStarlessChannels: shared validity failed: %v", err))
		return nil, err
	}
	recombineValid, err := BuildSharedValidityMask(channelValid, width, height, len(channelValid))
	if err != nil {
		debuglog.Log(fmt.Sprintf("CreateStarlessChannels: recombine validity failed: %v", err))
		return nil, err
	}
	logPhase("validity masks", phaseStart)
	maxRadius := resolveStarMaskMaxRadius(settings)
	var detectionImage []float32
	var bgModel BackgroundModel
	var seeds []StarSeed
	var candidateMask, acceptedSeedMask, rejectedSeedMask []bool
	var rejectedLowProminenceSeedMask, rejectedDiffuseSeedMask []bool
	var mergedSeedMaskBeforeChannelFilter, mergedSeedMaskAfterChannelFilter []bool
	var rejectedSingleChannelSeedMask, rejectedSmallFootprintSeedMask []bool
	var channelDetections []ChannelStarDetection
	debuglog.Log(fmt.Sprintf("CreateStarlessChannels: detection merge mode=%s", detectionMergeMode))
	if detectionMergeMode == "per-channel-merged" {
		phaseStart = time.Now()
		detectionImage, err = BuildStarDetectionImageWithChannelValidity(channels, channelValid, sharedValid, width, height, detectionMode)
		if err != nil {
			debuglog.Log(fmt.Sprintf("CreateStarlessChannels: detection image failed: %v", err))
			return nil, err
		}
		logPhase("shared detection image", phaseStart)
		phaseStart = time.Now()
		bgModel = EstimateLocalBackgroundMADWithValidity(detectionImage, sharedValid, width, height, settings.BackgroundTileSize)
		logPhase("shared background model", phaseStart)
		phaseStart = time.Now()
		channelDetections, err = DetectChannelStarSeeds(channels, channelValid, width, height, settings, maxRadius)
		if err != nil {
			debuglog.Log(fmt.Sprintf("CreateStarlessChannels: per-channel detection failed: %v", err))
			return nil, err
		}
		logPhase("per-channel seed detection", phaseStart)
		for i, det := range channelDetections {
			debuglog.Log(fmt.Sprintf("CreateStarlessChannels: channel %d raw candidates=%d accepted seeds=%d", i+1, countBoolMask(det.RawCandidateMask), len(det.Seeds)))
		}
		phaseStart = time.Now()
		mergedSeeds := MergeStarSeeds(channelDetections, settings.SuppressionRadius)
		mergedSeedMaskBeforeChannelFilter = buildSeedPointMask(mergedSeeds, width, height)
		seeds, rejectedSingleChannelSeedMask = filterMergedSeedsByDetectedChannels(mergedSeeds, settings.MinDetectedChannels, width, height)
		mergedSeedMaskAfterChannelFilter = buildSeedPointMask(seeds, width, height)
		candidateMask, acceptedSeedMask, rejectedSeedMask = mergeChannelDebugMasks(channelDetections, width*height)
		rejectedLowProminenceSeedMask = mergeChannelRejectedLowProminenceSeedMasks(channelDetections, width*height)
		rejectedDiffuseSeedMask = mergeChannelRejectedDiffuseSeedMasks(channelDetections, width*height)
		rejectedSmallFootprintSeedMask = mergeChannelRejectedSmallSeedMasks(channelDetections, width*height)
		logPhase("seed merge/filter", phaseStart)
		debuglog.Log(fmt.Sprintf("CreateStarlessChannels: merged %d per-channel seeds, kept %d with min channels=%d", len(mergedSeeds), len(seeds), settings.MinDetectedChannels))
	} else {
		debuglog.Log(fmt.Sprintf("CreateStarlessChannels: build detection image mode=%s", detectionMode))
		phaseStart = time.Now()
		detectionImage, err = BuildStarDetectionImageWithChannelValidity(channels, channelValid, sharedValid, width, height, detectionMode)
		if err != nil {
			debuglog.Log(fmt.Sprintf("CreateStarlessChannels: detection image failed: %v", err))
			return nil, err
		}
		logPhase("shared detection image", phaseStart)
		debuglog.Log(fmt.Sprintf("CreateStarlessChannels: estimate local background tile=%d", settings.BackgroundTileSize))
		phaseStart = time.Now()
		seedDetectionImage := preprocessStarSeedDetectionImage(detectionImage, sharedValid, width, height, settings.DetectionPreprocessMode)
		seedModel := EstimateLocalBackgroundMADWithValidity(seedDetectionImage, sharedValid, width, height, settings.BackgroundTileSize)
		bgModel = EstimateLocalBackgroundMADWithValidity(detectionImage, sharedValid, width, height, settings.BackgroundTileSize)
		logPhase("shared background model", phaseStart)
		debuglog.Log(fmt.Sprintf("CreateStarlessChannels: detect star seeds sigma=%.2f", settings.DetectionSigma))
		seeds, candidateMask, acceptedSeedMask, rejectedSeedMask, rejectedSmallFootprintSeedMask, rejectedLowProminenceSeedMask, rejectedDiffuseSeedMask, err = detectStarSeedsWithFootprint(seedDetectionImage, sharedValid, width, height, seedModel, settings.DetectionSigma, settings.SeedMinProminence, settings.SuppressionRadius, settings.MinSeedFootprintArea)
		if err != nil {
			debuglog.Log(fmt.Sprintf("CreateStarlessChannels: seed detection failed: %v", err))
			return nil, err
		}
		seeds = validateSeedsOnRadiusImage(seeds, detectionImage, sharedValid, width, height, acceptedSeedMask, rejectedSeedMask, rejectedDiffuseSeedMask, strings.ToLower(strings.TrimSpace(settings.DetectionPreprocessMode)) == "dog")
		if len(seeds) == 0 {
			if fallback, ok := detectFallbackBrightSeed(detectionImage, sharedValid, width, height, bgModel); ok {
				seeds = append(seeds, fallback)
				acceptedSeedMask[fallback.Y*width+fallback.X] = true
				debuglog.Log("CreateStarlessChannels: added fallback bright seed")
			}
		}
	}
	components := make([]DetectedStarComponent, 0, len(seeds))
	for _, seed := range seeds {
		components = append(components, DetectedStarComponent{
			MinX:         seed.X,
			MinY:         seed.Y,
			MaxX:         seed.X,
			MaxY:         seed.Y,
			Area:         1,
			ValidArea:    1,
			BBoxWidth:    1,
			BBoxHeight:   1,
			FillRatio:    1,
			AspectRatio:  1,
			CentroidX:    float64(seed.X),
			CentroidY:    float64(seed.Y),
			PeakValue:    seed.PeakValue,
			MeanValue:    seed.CoreMean,
			PeakContrast: float64(seed.Prominence),
			CoreArea:     1,
			CompactCore:  true,
		})
	}
	debuglog.Log(fmt.Sprintf("CreateStarlessChannels: detected %d seeds", len(seeds)))
	debuglog.Log(fmt.Sprintf("CreateStarlessChannels: build hard mask baseRadius=%d maxRadius=%d", settings.MaskGrowRadius, maxRadius))
	var hardMask []bool
	phaseStart = time.Now()
	if detectionMergeMode == "per-channel-merged" {
		hardMask, seeds = BuildSeedStarMaskFromRadii(sharedValid, width, height, seeds, settings.MaskGrowRadius, maxRadius)
	} else {
		hardMask, seeds = BuildSeedStarMask(detectionImage, sharedValid, width, height, bgModel, seeds, settings.MaskGrowRadius, maxRadius)
	}
	logPhase("hard mask build", phaseStart)
	mergedSeedMask := buildSeedPointMask(seeds, width, height)
	if mergedSeedMaskAfterChannelFilter == nil {
		mergedSeedMaskAfterChannelFilter = mergedSeedMask
	}
	hardCoverage := maskCoveragePercent(hardMask)
	debuglog.Log(fmt.Sprintf("CreateStarlessChannels: hard mask coverage %.2f%%", hardCoverage))
	if hardCoverage > 12 {
		debuglog.Log(fmt.Sprintf("CreateStarlessChannels: WARNING hard mask covers %.2f%% of image", hardCoverage))
	}
	debuglog.Log(fmt.Sprintf("CreateStarlessChannels: feather alpha radius=%d", settings.MaskSoftEdgeRadius))
	phaseStart = time.Now()
	alphaMask := FeatherMask(hardMask, width, height, float64(settings.MaskSoftEdgeRadius))
	for i := range alphaMask {
		if !sharedValid[i] {
			alphaMask[i] = 0
		}
	}
	debuglog.Log(fmt.Sprintf("CreateStarlessChannels: alpha mask coverage %.2f%%", alphaCoveragePercent(alphaMask)))
	logPhase("feather mask", phaseStart)
	starless := make([][]float32, len(channels))
	stars := make([][]float32, len(channels))
	phaseStart = time.Now()
	var wg sync.WaitGroup
	errs := make([]error, len(channels))
	for i, ch := range channels {
		i, ch := i, ch
		wg.Add(1)
		go func() {
			defer wg.Done()
			debuglog.Log(fmt.Sprintf("CreateStarlessChannels: inpaint channel %d/%d radius=%d mode=%s", i+1, len(channels), settings.InpaintRadius, inpaintMode))
			inpainted, err := InpaintStarsIDWWithValidity(ch, hardMask, channelValid[i], width, height, settings)
			if err != nil {
				errs[i] = fmt.Errorf("channel %d: %w", i, err)
				return
			}
			starless[i] = inpainted
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			debuglog.Log(fmt.Sprintf("CreateStarlessChannels: inpaint failed for channel %d: %v", i+1, err))
			return nil, err
		}
	}
	logPhase("per-channel inpainting", phaseStart)
	phaseStart = time.Now()
	for i, ch := range channels {
		layer := make([]float32, len(ch))
		for px := range ch {
			orig := ch[px]
			fill := starless[i][px]
			if !channelValid[i][px] || !isFiniteStar32(orig) || !isFiniteStar32(fill) {
				continue
			}
			delta := orig - fill
			if delta < 0 {
				delta = 0
			}
			layer[px] = delta
		}
		stars[i] = layer
	}
	logPhase("star layer extraction", phaseStart)
	debuglog.Log(fmt.Sprintf("CreateStarlessChannels: built starless/stars for %d channels", len(channels)))
	return &StarlessResult{Width: width, Height: height, Settings: settings, DetectionImage: detectionImage, Background: bgModel.Background, Sigma: bgModel.Sigma, SharedValid: sharedValid, RecombineValid: recombineValid, ChannelValid: channelValid, RawCandidateMask: candidateMask, AcceptedSeedMask: acceptedSeedMask, RejectedSeedMask: rejectedSeedMask, RejectedLowProminenceSeedMask: rejectedLowProminenceSeedMask, RejectedDiffuseSeedMask: rejectedDiffuseSeedMask, ChannelDetections: channelDetections, MergedSeedMask: mergedSeedMask, MergedSeedMaskBeforeChannelFilter: mergedSeedMaskBeforeChannelFilter, MergedSeedMaskAfterChannelFilter: mergedSeedMaskAfterChannelFilter, RejectedSingleChannelSeedMask: rejectedSingleChannelSeedMask, RejectedSmallFootprintSeedMask: rejectedSmallFootprintSeedMask, HardMask: hardMask, AlphaMask: alphaMask, Seeds: seeds, Components: components, Starless: starless, Stars: stars}, nil
}

func RecombineStarlessRGB(starless [][]float32, stars [][]float32, alpha []float32, width, height int, settings StarRecombineSettings) ([][]float32, error) {
	debuglog.Log(fmt.Sprintf("RecombineStarlessRGB: starting (%dx%d)", width, height))
	defer debuglog.Log("RecombineStarlessRGB: finished")
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("width and height must be > 0")
	}
	if len(starless) != 3 || len(stars) != 3 {
		return nil, fmt.Errorf("expected 3 RGB channels for starless and stars")
	}
	total := width * height
	if len(alpha) != total {
		return nil, fmt.Errorf("alpha length = %d, want %d", len(alpha), total)
	}
	if settings.ValidMask != nil && len(settings.ValidMask) != total {
		return nil, fmt.Errorf("valid mask length = %d, want %d", len(settings.ValidMask), total)
	}
	for i := 0; i < 3; i++ {
		if len(starless[i]) != total {
			return nil, fmt.Errorf("starless channel %d length = %d, want %d", i, len(starless[i]), total)
		}
		if len(stars[i]) != total {
			return nil, fmt.Errorf("star layer channel %d length = %d, want %d", i, len(stars[i]), total)
		}
	}
	brightness := settings.StarBrightness
	if brightness < 0 {
		brightness = 0
	}
	saturation := settings.StarSaturation
	if saturation < 0 {
		saturation = 0
	} else if saturation > 1 {
		saturation = 1
	}
	debuglog.Log(fmt.Sprintf("RecombineStarlessRGB: brightness=%.3f saturation=%.3f", brightness, saturation))
	out := make([][]float32, 3)
	for i := 0; i < 3; i++ {
		out[i] = append([]float32(nil), starless[i]...)
	}
	for idx := 0; idx < total; idx++ {
		if settings.ValidMask != nil && !settings.ValidMask[idx] {
			continue
		}
		a := alpha[idx]
		if a <= 0 {
			continue
		}
		sr := stars[0][idx]
		sg := stars[1][idx]
		sb := stars[2][idx]
		starL := maxStarFloat32(sr, maxStarFloat32(sg, sb))
		for ch := 0; ch < 3; ch++ {
			origStar := stars[ch][idx]
			mixedStar := starL + saturation*(origStar-starL)
			out[ch][idx] += a * brightness * mixedStar
		}
	}
	return out, nil
}

func EstimateLocalBackgroundMAD(pixels []float32, width, height int, tileSize int) BackgroundModel {
	return EstimateLocalBackgroundMADWithValidity(pixels, nil, width, height, tileSize)
}

func EstimateLocalBackgroundMADWithValidity(pixels []float32, valid []bool, width, height int, tileSize int) BackgroundModel {
	model := BackgroundModel{Width: width, Height: height}
	if width <= 0 || height <= 0 {
		return model
	}
	total := width * height
	if total <= 0 {
		return model
	}
	model.Background = make([]float32, total)
	model.Sigma = make([]float32, total)
	if len(pixels) < total {
		return model
	}
	if valid != nil && len(valid) != total {
		return model
	}
	if tileSize <= 0 {
		tileSize = minInt(width, height)
		if tileSize <= 0 {
			tileSize = 1
		}
	}
	for y0 := 0; y0 < height; y0 += tileSize {
		y1 := y0 + tileSize
		if y1 > height {
			y1 = height
		}
		for x0 := 0; x0 < width; x0 += tileSize {
			x1 := x0 + tileSize
			if x1 > width {
				x1 = width
			}
			values := make([]float64, 0, (x1-x0)*(y1-y0))
			for y := y0; y < y1; y++ {
				row := y * width
				for x := x0; x < x1; x++ {
					v := pixels[row+x]
					if (valid == nil || valid[row+x]) && isFiniteStar32(v) {
						values = append(values, float64(v))
					}
				}
			}
			bg, sigma := medianAndMADSigma(values)
			if sigma < localSigmaFloor {
				sigma = localSigmaFloor
			}
			for y := y0; y < y1; y++ {
				row := y * width
				for x := x0; x < x1; x++ {
					idx := row + x
					model.Background[idx] = float32(bg)
					model.Sigma[idx] = float32(sigma)
				}
			}
		}
	}
	return model
}

func FeatherMask(mask []bool, width, height int, radius float64) []float32 { /* unchanged */
	out := make([]float32, len(mask))
	if width <= 0 || height <= 0 || len(mask) == 0 {
		return out
	}
	total := width * height
	if len(mask) < total {
		total = len(mask)
	}
	if radius <= 0 {
		for i := 0; i < total; i++ {
			if mask[i] {
				out[i] = 1
			}
		}
		return out
	}
	return FeatherMaskFast(mask, width, height, radius)
}

func FeatherMaskFast(mask []bool, width, height int, radius float64) []float32 {
	out := make([]float32, len(mask))
	if width <= 0 || height <= 0 || len(mask) == 0 {
		return out
	}
	total := width * height
	if len(mask) < total {
		total = len(mask)
	}
	if radius <= 0 {
		for i := 0; i < total; i++ {
			if mask[i] {
				out[i] = 1
			}
		}
		return out
	}
	search := int(math.Ceil(radius))
	r2 := radius * radius
	for idx := 0; idx < total; idx++ {
		if !mask[idx] {
			continue
		}
		out[idx] = 1
		x, y := idx%width, idx/width
		x0, x1 := maxStarInt(0, x-search), minInt(width-1, x+search)
		y0, y1 := maxStarInt(0, y-search), minInt(height-1, y+search)
		for ny := y0; ny <= y1; ny++ {
			for nx := x0; nx <= x1; nx++ {
				nIdx := ny*width + nx
				if nIdx >= total || mask[nIdx] {
					continue
				}
				dx, dy := float64(nx-x), float64(ny-y)
				d2 := dx*dx + dy*dy
				if d2 > r2 {
					continue
				}
				alpha := 1.0 - (math.Sqrt(d2) / radius)
				if alpha > float64(out[nIdx]) {
					out[nIdx] = float32(alpha)
				}
			}
		}
	}
	return out
}

func InpaintStarsIDW(original []float32, mask []bool, width, height int, settings StarMaskSettings) ([]float32, error) { /* unchanged */
	return InpaintStarsIDWWithValidity(original, mask, nil, width, height, settings)
}

func InpaintStarsIDWWithValidity(original []float32, mask []bool, valid []bool, width, height int, settings StarMaskSettings) ([]float32, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("width and height must be > 0")
	}
	total := width * height
	if total <= 0 {
		return nil, fmt.Errorf("invalid image dimensions")
	}
	if len(original) != total {
		return nil, fmt.Errorf("original length = %d, want %d", len(original), total)
	}
	if len(mask) != total {
		return nil, fmt.Errorf("mask length = %d, want %d", len(mask), total)
	}
	if valid != nil && len(valid) != total {
		return nil, fmt.Errorf("valid mask length = %d, want %d", len(valid), total)
	}
	if err := settings.Validate(); err != nil {
		return nil, err
	}
	mode := strings.ToLower(strings.TrimSpace(settings.InpaintMode))
	if mode == "" {
		mode = "idw-region"
	}
	if mode == "idw-region" {
		return inpaintStarsIDWRegion(original, mask, valid, width, height, settings)
	}
	return inpaintStarsIDWPixel(original, mask, valid, width, height, settings)
}

func inpaintStarsIDWPixel(original []float32, mask []bool, valid []bool, width, height int, settings StarMaskSettings) ([]float32, error) {
	total := width * height
	out := append([]float32(nil), original...)
	baseRadius := settings.InpaintRadius
	if baseRadius < 1 {
		baseRadius = 1
	}
	for idx := 0; idx < total; idx++ {
		if !mask[idx] {
			continue
		}
		if valid != nil && !valid[idx] {
			continue
		}
		x, y := idx%width, idx/width
		replaced := false
		for mult := 1; mult <= 3 && !replaced; mult++ {
			radius := baseRadius * mult
			x0, x1 := maxStarInt(0, x-radius), minInt(width-1, x+radius)
			y0, y1 := maxStarInt(0, y-radius), minInt(height-1, y+radius)
			var weightedSum, weightTotal float64
			for ny := y0; ny <= y1; ny++ {
				for nx := x0; nx <= x1; nx++ {
					nIdx := ny*width + nx
					if mask[nIdx] {
						continue
					}
					if valid != nil && !valid[nIdx] {
						continue
					}
					v := original[nIdx]
					if !isFiniteStar32(v) {
						continue
					}
					dx, dy := float64(nx-x), float64(ny-y)
					d2 := dx*dx + dy*dy
					if d2 == 0 {
						out[idx] = v
						replaced = true
						break
					}
					w := 1.0 / d2
					weightedSum += float64(v) * w
					weightTotal += w
				}
				if replaced {
					break
				}
			}
			if replaced {
				break
			}
			if weightTotal > 0 {
				out[idx] = float32(weightedSum / weightTotal)
				replaced = true
			}
		}
	}
	return out, nil
}

type maskRegion struct {
	Pixels     []int
	MinX, MinY int
	MaxX, MaxY int
}

type inpaintSample struct {
	X, Y int
	V    float32
}

func inpaintStarsIDWRegion(original []float32, mask []bool, valid []bool, width, height int, settings StarMaskSettings) ([]float32, error) {
	out := append([]float32(nil), original...)
	regions := findMaskRegions(mask, valid, width, height)
	baseRadius := settings.InpaintRadius
	if baseRadius < 1 {
		baseRadius = 1
	}
	for _, region := range regions {
		var samples []inpaintSample
		for mult := 1; mult <= 3 && len(samples) == 0; mult++ {
			radius := baseRadius * mult
			samples = collectRegionInpaintSamples(original, mask, valid, width, height, region, radius)
		}
		if len(samples) == 0 {
			continue
		}
		for _, idx := range region.Pixels {
			x, y := idx%width, idx/width
			out[idx] = idwFromSamples(samples, x, y)
		}
	}
	return out, nil
}

func findMaskRegions(mask []bool, valid []bool, width, height int) []maskRegion {
	total := width * height
	visited := make([]bool, total)
	regions := make([]maskRegion, 0)
	queue := make([]int, 0, 64)
	dirs := [8][2]int{{-1, -1}, {0, -1}, {1, -1}, {-1, 0}, {1, 0}, {-1, 1}, {0, 1}, {1, 1}}
	for idx := 0; idx < total; idx++ {
		if visited[idx] || !mask[idx] || (valid != nil && !valid[idx]) {
			continue
		}
		region := maskRegion{MinX: idx % width, MaxX: idx % width, MinY: idx / width, MaxY: idx / width}
		queue = append(queue[:0], idx)
		visited[idx] = true
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			region.Pixels = append(region.Pixels, cur)
			x, y := cur%width, cur/width
			if x < region.MinX {
				region.MinX = x
			}
			if x > region.MaxX {
				region.MaxX = x
			}
			if y < region.MinY {
				region.MinY = y
			}
			if y > region.MaxY {
				region.MaxY = y
			}
			for _, d := range dirs {
				nx, ny := x+d[0], y+d[1]
				if nx < 0 || nx >= width || ny < 0 || ny >= height {
					continue
				}
				nIdx := ny*width + nx
				if visited[nIdx] || !mask[nIdx] || (valid != nil && !valid[nIdx]) {
					continue
				}
				visited[nIdx] = true
				queue = append(queue, nIdx)
			}
		}
		regions = append(regions, region)
	}
	return regions
}

func collectRegionInpaintSamples(original []float32, mask []bool, valid []bool, width, height int, region maskRegion, radius int) []inpaintSample {
	x0 := maxStarInt(0, region.MinX-radius)
	x1 := minInt(width-1, region.MaxX+radius)
	y0 := maxStarInt(0, region.MinY-radius)
	y1 := minInt(height-1, region.MaxY+radius)
	samples := make([]inpaintSample, 0, 2*(x1-x0+y1-y0+2))
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			if x >= region.MinX && x <= region.MaxX && y >= region.MinY && y <= region.MaxY {
				continue
			}
			idx := y*width + x
			if mask[idx] || (valid != nil && !valid[idx]) || !isFiniteStar32(original[idx]) {
				continue
			}
			samples = append(samples, inpaintSample{X: x, Y: y, V: original[idx]})
		}
	}
	return samples
}

func idwFromSamples(samples []inpaintSample, x, y int) float32 {
	var weightedSum, weightTotal float64
	for _, sample := range samples {
		dx, dy := float64(sample.X-x), float64(sample.Y-y)
		d2 := dx*dx + dy*dy
		if d2 == 0 {
			return sample.V
		}
		w := 1.0 / d2
		weightedSum += float64(sample.V) * w
		weightTotal += w
	}
	if weightTotal == 0 {
		return 0
	}
	return float32(weightedSum / weightTotal)
}

func NormalizeRobust(pixels []float32) []float32 {
	return NormalizeRobustWithValidity(pixels, nil)
}

func NormalizeRobustWithValidity(pixels []float32, valid []bool) []float32 {
	out := make([]float32, len(pixels))
	if len(pixels) == 0 {
		return out
	}
	if valid != nil && len(valid) != len(pixels) {
		return out
	}
	values := finiteFloat64sWithValidity(pixels, valid)
	if len(values) == 0 {
		return out
	}
	sort.Float64s(values)
	lo, hi := percentileSorted(values, 5.0), percentileSorted(values, 99.5)
	if !isFiniteStar64(lo) || !isFiniteStar64(hi) {
		return out
	}
	if hi <= lo {
		for i, v := range pixels {
			if (valid == nil || valid[i]) && isFiniteStar32(v) {
				out[i] = 0
			}
		}
		return out
	}
	scale := 1.0 / (hi - lo)
	for i, v := range pixels {
		if (valid != nil && !valid[i]) || !isFiniteStar32(v) {
			continue
		}
		n := (float64(v) - lo) * scale
		if n < 0 {
			n = 0
		} else if n > 1 {
			n = 1
		}
		out[i] = float32(n)
	}
	return out
}

func BuildStarDetectionImage(channels [][]float32, width, height int, mode string) ([]float32, error) {
	return BuildStarDetectionImageWithChannelValidity(channels, nil, nil, width, height, mode)
}

func preprocessStarSeedDetectionImage(pixels []float32, valid []bool, width, height int, mode string) []float32 {
	if strings.ToLower(strings.TrimSpace(mode)) != "dog" {
		return pixels
	}
	small := boxBlurFloat32WithValidity(pixels, valid, width, height, 1)
	large := boxBlurFloat32WithValidity(pixels, valid, width, height, 5)
	resp := make([]float32, len(pixels))
	for i := range resp {
		if valid != nil && !valid[i] {
			continue
		}
		v := small[i] - large[i]
		if v > 0 && isFiniteStar32(v) {
			resp[i] = v
		}
	}
	return NormalizeRobustWithValidity(resp, valid)
}

func boxBlurFloat32WithValidity(pixels []float32, valid []bool, width, height, radius int) []float32 {
	out := make([]float32, len(pixels))
	if radius < 1 || width <= 0 || height <= 0 {
		copy(out, pixels)
		return out
	}
	for y := 0; y < height; y++ {
		y0, y1 := maxStarInt(0, y-radius), minInt(height-1, y+radius)
		for x := 0; x < width; x++ {
			x0, x1 := maxStarInt(0, x-radius), minInt(width-1, x+radius)
			var sum float64
			count := 0
			for yy := y0; yy <= y1; yy++ {
				row := yy * width
				for xx := x0; xx <= x1; xx++ {
					idx := row + xx
					if (valid == nil || valid[idx]) && isFiniteStar32(pixels[idx]) {
						sum += float64(pixels[idx])
						count++
					}
				}
			}
			if count > 0 {
				out[y*width+x] = float32(sum / float64(count))
			}
		}
	}
	return out
}

func BuildStarDetectionImageWithValidity(channels [][]float32, sharedValid []bool, width, height int, mode string) ([]float32, error) {
	return BuildStarDetectionImageWithChannelValidity(channels, nil, sharedValid, width, height, mode)
}

func BuildStarDetectionImageWithChannelValidity(channels [][]float32, channelValid [][]bool, sharedValid []bool, width, height int, mode string) ([]float32, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("width and height must be > 0")
	}
	if len(channels) < 1 || len(channels) > 3 {
		return nil, fmt.Errorf("expected 1 to 3 channels, got %d", len(channels))
	}
	total := width * height
	if total <= 0 {
		return nil, fmt.Errorf("invalid image dimensions")
	}
	if sharedValid != nil && len(sharedValid) != total {
		return nil, fmt.Errorf("shared valid mask length = %d, want %d", len(sharedValid), total)
	}
	if channelValid != nil && len(channelValid) != len(channels) {
		return nil, fmt.Errorf("channel validity mask count = %d, want %d", len(channelValid), len(channels))
	}
	for i, ch := range channels {
		if len(ch) != total {
			return nil, fmt.Errorf("channel %d length = %d, want %d", i, len(ch), total)
		}
		if channelValid != nil && len(channelValid[i]) != total {
			return nil, fmt.Errorf("channel %d valid mask length = %d, want %d", i, len(channelValid[i]), total)
		}
	}
	combineMode := strings.ToLower(strings.TrimSpace(mode))
	if combineMode == "" {
		combineMode, mode = "median", "median"
	}
	if combineMode != "median" && combineMode != "min" && combineMode != "max" {
		return nil, fmt.Errorf("unsupported detection mode %q", mode)
	}
	normalized := make([][]float32, len(channels))
	for i, ch := range channels {
		var valid []bool
		if channelValid != nil {
			valid = channelValid[i]
		}
		normalized[i] = NormalizeRobustWithValidity(ch, valid)
	}
	out := make([]float32, total)
	for idx := 0; idx < total; idx++ {
		if sharedValid != nil && !sharedValid[idx] {
			continue
		}
		vals := make([]float64, 0, len(normalized))
		for chIdx, ch := range normalized {
			if channelValid != nil && !channelValid[chIdx][idx] {
				continue
			}
			v := float64(ch[idx])
			if isFiniteStar64(v) {
				vals = append(vals, v)
			}
		}
		if len(vals) == 0 {
			continue
		}
		switch combineMode {
		case "min":
			best := vals[0]
			for _, v := range vals[1:] {
				if v < best {
					best = v
				}
			}
			out[idx] = float32(best)
		case "max":
			best := vals[0]
			for _, v := range vals[1:] {
				if v > best {
					best = v
				}
			}
			out[idx] = float32(best)
		default:
			sort.Float64s(vals)
			out[idx] = float32(percentileSorted(vals, 50.0))
		}
	}
	return out, nil
}

func DetectChannelStarSeeds(channels [][]float32, channelValid [][]bool, width, height int, settings StarMaskSettings, maxRadius int) ([]ChannelStarDetection, error) {
	if err := validateChannelSet(channels, width, height); err != nil {
		return nil, err
	}
	total := width * height
	if len(channelValid) != len(channels) {
		return nil, fmt.Errorf("channel validity mask count = %d, want %d", len(channelValid), len(channels))
	}
	out := make([]ChannelStarDetection, len(channels))
	errs := make([]error, len(channels))
	var wg sync.WaitGroup
	for i, ch := range channels {
		i, ch := i, ch
		wg.Add(1)
		go func() {
			defer wg.Done()
			if len(channelValid[i]) != total {
				errs[i] = fmt.Errorf("channel %d valid mask length = %d, want %d", i, len(channelValid[i]), total)
				return
			}
			detection := NormalizeRobustWithValidity(ch, channelValid[i])
			seedDetection := preprocessStarSeedDetectionImage(detection, channelValid[i], width, height, settings.DetectionPreprocessMode)
			seedModel := EstimateLocalBackgroundMADWithValidity(seedDetection, channelValid[i], width, height, settings.BackgroundTileSize)
			radiusModel := EstimateLocalBackgroundMADWithValidity(detection, channelValid[i], width, height, settings.BackgroundTileSize)
			seeds, rawMask, acceptedMask, rejectedMask, rejectedSmallMask, rejectedLowMask, rejectedDiffuseMask, err := detectStarSeedsWithFootprint(seedDetection, channelValid[i], width, height, seedModel, settings.DetectionSigma, settings.SeedMinProminence, settings.SuppressionRadius, settings.MinSeedFootprintArea)
			if err != nil {
				errs[i] = fmt.Errorf("channel %d seed detection: %w", i, err)
				return
			}
			seeds = validateSeedsOnRadiusImage(seeds, detection, channelValid[i], width, height, acceptedMask, rejectedMask, rejectedDiffuseMask, strings.ToLower(strings.TrimSpace(settings.DetectionPreprocessMode)) == "dog")
			sourceChannel := uint8(1 << uint(i))
			seeds = EstimateSeedRadii(detection, channelValid[i], width, height, radiusModel, seeds, settings.MaskGrowRadius, maxRadius)
			for j := range seeds {
				seeds[j].SourceChannels = sourceChannel
				seeds[j].DetectedChannelCount = 1
				seeds[j].MaxRadius = seeds[j].Radius
				seeds[j].MaxPeakValue = seeds[j].PeakValue
				seeds[j].SourceSeedCount = 1
			}
			var hardMask []bool
			if settings.BuildDebugMasks {
				hardMask, _ = BuildSeedStarMaskFromRadii(channelValid[i], width, height, seeds, settings.MaskGrowRadius, maxRadius)
			}
			out[i] = ChannelStarDetection{
				DetectionImage:                detection,
				Background:                    radiusModel.Background,
				Sigma:                         radiusModel.Sigma,
				RawCandidateMask:              rawMask,
				AcceptedSeedMask:              acceptedMask,
				RejectedSeedMask:              rejectedMask,
				RejectedLowProminenceSeedMask: rejectedLowMask,
				RejectedDiffuseSeedMask:       rejectedDiffuseMask,
				RejectedSmallSeedMask:         rejectedSmallMask,
				HardMask:                      hardMask,
				Seeds:                         seeds,
			}
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func MergeStarSeeds(detections []ChannelStarDetection, mergeRadius int) []StarSeed {
	if mergeRadius < 1 {
		mergeRadius = 1
	}
	var all []StarSeed
	for ch, det := range detections {
		sourceChannel := uint8(1 << uint(ch))
		for _, seed := range det.Seeds {
			if seed.SourceChannels == 0 {
				seed.SourceChannels = sourceChannel
			}
			if seed.DetectedChannelCount == 0 {
				seed.DetectedChannelCount = bitCount8(seed.SourceChannels)
			}
			if seed.MaxPeakValue == 0 || seed.PeakValue > seed.MaxPeakValue {
				seed.MaxPeakValue = seed.PeakValue
			}
			if seed.MaxRadius == 0 || seed.Radius > seed.MaxRadius {
				seed.MaxRadius = seed.Radius
			}
			if seed.SourceSeedCount == 0 {
				seed.SourceSeedCount = 1
			}
			all = append(all, seed)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].PeakValue == all[j].PeakValue {
			return all[i].Radius > all[j].Radius
		}
		return all[i].PeakValue > all[j].PeakValue
	})
	merged := make([]StarSeed, 0, len(all))
	r2 := float64(mergeRadius * mergeRadius)
	for _, seed := range all {
		found := -1
		for i, existing := range merged {
			dx := float64(existing.X - seed.X)
			dy := float64(existing.Y - seed.Y)
			radius := r2
			larger := math.Max(existing.Radius, seed.Radius)
			if larger > float64(mergeRadius) {
				radius = larger * larger
			}
			if dx*dx+dy*dy <= radius {
				found = i
				break
			}
		}
		if found < 0 {
			merged = append(merged, seed)
			continue
		}
		merged[found].SourceChannels |= seed.SourceChannels
		merged[found].DetectedChannelCount = bitCount8(merged[found].SourceChannels)
		merged[found].SourceSeedCount += maxStarInt(seed.SourceSeedCount, 1)
		if seed.PeakValue > merged[found].MaxPeakValue {
			merged[found].MaxPeakValue = seed.PeakValue
		}
		if seed.Radius > merged[found].MaxRadius {
			merged[found].MaxRadius = seed.Radius
		}
		if seed.FootprintArea > merged[found].FootprintArea {
			merged[found].FootprintArea = seed.FootprintArea
		}
		if seed.Radius > merged[found].Radius {
			merged[found].Radius = seed.Radius
		}
		if seed.PeakValue > merged[found].PeakValue {
			sourceChannels := merged[found].SourceChannels
			detectedChannelCount := merged[found].DetectedChannelCount
			maxRadius := merged[found].MaxRadius
			maxPeakValue := merged[found].MaxPeakValue
			sourceSeedCount := merged[found].SourceSeedCount
			footprintArea := merged[found].FootprintArea
			keepRadius := merged[found].Radius
			merged[found] = seed
			merged[found].Radius = keepRadius
			merged[found].SourceChannels = sourceChannels
			merged[found].DetectedChannelCount = detectedChannelCount
			merged[found].MaxRadius = maxRadius
			merged[found].MaxPeakValue = maxPeakValue
			merged[found].SourceSeedCount = sourceSeedCount
			merged[found].FootprintArea = footprintArea
		}
	}
	return merged
}

func filterMergedSeedsByDetectedChannels(seeds []StarSeed, minChannels, width, height int) ([]StarSeed, []bool) {
	if minChannels < 1 {
		minChannels = 1
	}
	kept := make([]StarSeed, 0, len(seeds))
	rejected := make([]bool, width*height)
	for _, seed := range seeds {
		count := seed.DetectedChannelCount
		if count == 0 {
			count = bitCount8(seed.SourceChannels)
		}
		if count < minChannels {
			if seed.X >= 0 && seed.X < width && seed.Y >= 0 && seed.Y < height {
				rejected[seed.Y*width+seed.X] = true
			}
			continue
		}
		seed.DetectedChannelCount = count
		kept = append(kept, seed)
	}
	return kept, rejected
}

func mergeChannelDebugMasks(detections []ChannelStarDetection, total int) ([]bool, []bool, []bool) {
	raw := make([]bool, total)
	accepted := make([]bool, total)
	rejected := make([]bool, total)
	for _, det := range detections {
		orBoolMask(raw, det.RawCandidateMask)
		orBoolMask(accepted, det.AcceptedSeedMask)
		orBoolMask(rejected, det.RejectedSeedMask)
	}
	return raw, accepted, rejected
}

func orBoolMask(dst, src []bool) {
	for i, v := range src {
		if i >= len(dst) {
			return
		}
		if v {
			dst[i] = true
		}
	}
}

func mergeChannelRejectedSmallSeedMasks(detections []ChannelStarDetection, total int) []bool {
	rejected := make([]bool, total)
	for _, det := range detections {
		orBoolMask(rejected, det.RejectedSmallSeedMask)
	}
	return rejected
}

func mergeChannelRejectedLowProminenceSeedMasks(detections []ChannelStarDetection, total int) []bool {
	rejected := make([]bool, total)
	for _, det := range detections {
		orBoolMask(rejected, det.RejectedLowProminenceSeedMask)
	}
	return rejected
}

func mergeChannelRejectedDiffuseSeedMasks(detections []ChannelStarDetection, total int) []bool {
	rejected := make([]bool, total)
	for _, det := range detections {
		orBoolMask(rejected, det.RejectedDiffuseSeedMask)
	}
	return rejected
}

func validateSeedsOnRadiusImage(seeds []StarSeed, detection []float32, valid []bool, width, height int, acceptedMask, rejectedMask, rejectedDiffuseMask []bool, strict bool) []StarSeed {
	if !strict || len(seeds) == 0 {
		return seeds
	}
	kept := seeds[:0]
	for _, seed := range seeds {
		idx := seed.Y*width + seed.X
		if idx < 0 || idx >= len(detection) || (valid != nil && !valid[idx]) {
			continue
		}
		ringMedian, okRing := localAnnulusMedian(detection, valid, width, height, seed.X, seed.Y, 2.0, 4.5)
		if !okRing {
			markRejectedSeed(idx, acceptedMask, rejectedMask, rejectedDiffuseMask)
			continue
		}
		peak := float64(detection[idx])
		score := pointSourceScore(detection, valid, width, height, seed.X, seed.Y, peak, ringMedian)
		elongation := localCandidateElongation(detection, valid, width, height, seed.X, seed.Y, 0)
		if score < 0.015 || elongation > 2.5 {
			markRejectedSeed(idx, acceptedMask, rejectedMask, rejectedDiffuseMask)
			continue
		}
		seed.StarLikeScore = float32(score)
		seed.PeakValue = float32(peak)
		kept = append(kept, seed)
	}
	return kept
}

func markRejectedSeed(idx int, acceptedMask, rejectedMask, rejectedDiffuseMask []bool) {
	if idx >= 0 && idx < len(acceptedMask) {
		acceptedMask[idx] = false
	}
	if idx >= 0 && idx < len(rejectedMask) {
		rejectedMask[idx] = true
	}
	if idx >= 0 && idx < len(rejectedDiffuseMask) {
		rejectedDiffuseMask[idx] = true
	}
}

func bitCount8(v uint8) int {
	count := 0
	for v != 0 {
		count += int(v & 1)
		v >>= 1
	}
	return count
}

func countBoolMask(mask []bool) int {
	count := 0
	for _, v := range mask {
		if v {
			count++
		}
	}
	return count
}

func maskCoveragePercent(mask []bool) float64 {
	if len(mask) == 0 {
		return 0
	}
	return 100 * float64(countBoolMask(mask)) / float64(len(mask))
}

func alphaCoveragePercent(alpha []float32) float64 {
	if len(alpha) == 0 {
		return 0
	}
	count := 0
	for _, v := range alpha {
		if v > 0 {
			count++
		}
	}
	return 100 * float64(count) / float64(len(alpha))
}

func buildSeedPointMask(seeds []StarSeed, width, height int) []bool {
	mask := make([]bool, width*height)
	for _, seed := range seeds {
		if seed.X < 0 || seed.X >= width || seed.Y < 0 || seed.Y >= height {
			continue
		}
		mask[seed.Y*width+seed.X] = true
	}
	return mask
}

func localRawMaskFootprintArea(rawMask []bool, valid []bool, width, height, seedX, seedY, suppressionRadius int) int {
	if seedX < 0 || seedX >= width || seedY < 0 || seedY >= height {
		return 0
	}
	seedIdx := seedY*width + seedX
	if !rawMask[seedIdx] || (valid != nil && !valid[seedIdx]) {
		return 0
	}
	searchRadius := suppressionRadius
	if searchRadius < 2 {
		searchRadius = 2
	}
	if searchRadius > 12 {
		searchRadius = 12
	}
	minX := maxStarInt(0, seedX-searchRadius)
	maxX := minInt(width-1, seedX+searchRadius)
	minY := maxStarInt(0, seedY-searchRadius)
	maxY := minInt(height-1, seedY+searchRadius)
	winW := maxX - minX + 1
	visited := make([]bool, winW*(maxY-minY+1))
	queue := []int{seedIdx}
	visited[(seedY-minY)*winW+(seedX-minX)] = true
	area := 0
	for len(queue) > 0 {
		idx := queue[0]
		queue = queue[1:]
		x := idx % width
		y := idx / width
		if x < minX || x > maxX || y < minY || y > maxY {
			continue
		}
		if !rawMask[idx] || (valid != nil && !valid[idx]) {
			continue
		}
		area++
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				if dx == 0 && dy == 0 {
					continue
				}
				nx, ny := x+dx, y+dy
				if nx < minX || nx > maxX || ny < minY || ny > maxY {
					continue
				}
				nIdx := ny*width + nx
				vIdx := (ny-minY)*winW + (nx - minX)
				if visited[vIdx] {
					continue
				}
				visited[vIdx] = true
				if rawMask[nIdx] && (valid == nil || valid[nIdx]) {
					queue = append(queue, nIdx)
				}
			}
		}
	}
	return area
}

func DetectStarSeeds(pixels []float32, valid []bool, width, height int, model BackgroundModel, thresholdSigma, minProminence float64, suppressionRadius int) ([]StarSeed, []bool, []bool, []bool, error) {
	seeds, rawMask, acceptedMask, rejectedMask, _, _, _, err := detectStarSeedsWithFootprint(pixels, valid, width, height, model, thresholdSigma, minProminence, suppressionRadius, 1)
	return seeds, rawMask, acceptedMask, rejectedMask, err
}

func detectStarSeedsWithFootprint(pixels []float32, valid []bool, width, height int, model BackgroundModel, thresholdSigma, minProminence float64, suppressionRadius, minFootprintArea int) ([]StarSeed, []bool, []bool, []bool, []bool, []bool, []bool, error) {
	if width <= 0 || height <= 0 {
		return nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("width and height must be > 0")
	}
	total := width * height
	if len(pixels) != total {
		return nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("pixels length = %d, want %d", len(pixels), total)
	}
	if valid != nil && len(valid) != total {
		return nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("valid mask length = %d, want %d", len(valid), total)
	}
	if minProminence < 0 {
		return nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("min prominence must be >= 0")
	}
	if suppressionRadius < 1 {
		return nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("suppression radius must be >= 1")
	}
	if minFootprintArea < 1 {
		return nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("min footprint area must be >= 1")
	}
	rawMask := make([]bool, total)
	acceptedMask := make([]bool, total)
	rejectedMask := make([]bool, total)
	rejectedSmallMask := make([]bool, total)
	rejectedLowProminenceMask := make([]bool, total)
	rejectedDiffuseMask := make([]bool, total)
	for i, pix := range pixels {
		if valid != nil && !valid[i] {
			continue
		}
		if !isFiniteStar32(pix) {
			continue
		}
		bg := float64(model.Background[i])
		sigma := math.Max(float64(model.Sigma[i]), localSigmaFloor)
		if float64(pix) >= bg+thresholdSigma*sigma {
			rawMask[i] = true
		}
	}
	type candidate struct {
		StarSeed
		score float64
	}
	candidates := make([]candidate, 0)
	plateauVisited := make([]bool, total)
	for y := 2; y < height-2; y++ {
		for x := 2; x < width-2; x++ {
			idx := y*width + x
			if plateauVisited[idx] || !rawMask[idx] {
				continue
			}
			seedPeak := float64(pixels[idx])
			if !isFiniteStar64(seedPeak) {
				continue
			}
			plateau := collectPeakPlateau(pixels, valid, rawMask, plateauVisited, width, height, idx)
			for _, pIdx := range plateau {
				plateauVisited[pIdx] = true
			}
			peak, peakCount := plateauPeak(pixels, plateau)
			if !isFiniteStar64(peak) {
				continue
			}
			saturatedPlateau := peak >= 0.95 && peakCount > 1 && peakCount <= 25
			seedX, seedY := plateauCentroid(pixels, width, plateau)
			seedIdx := seedY*width + seedX
			if seedIdx < 0 || seedIdx >= total || (valid != nil && !valid[seedIdx]) {
				seedIdx = idx
				seedX, seedY = x, y
			}
			if !plateauIsLocalMaximum(pixels, valid, width, height, plateau, peak) {
				rejectedMask[seedIdx] = true
				continue
			}
			footprintArea := localRawMaskFootprintArea(rawMask, valid, width, height, seedX, seedY, suppressionRadius)
			if footprintArea < minFootprintArea {
				rejectedMask[seedIdx] = true
				rejectedSmallMask[seedIdx] = true
				continue
			}
			if !saturatedPlateau && footprintArea > 80 {
				rejectedMask[seedIdx] = true
				rejectedDiffuseMask[seedIdx] = true
				continue
			}
			bg := float64(model.Background[seedIdx])
			sigma := math.Max(float64(model.Sigma[seedIdx]), localSigmaFloor)
			coreMean, okCore := localMeanWithinRadius(pixels, valid, width, height, seedX, seedY, 1.25)
			ringMedian, okRing := localAnnulusMedian(pixels, valid, width, height, seedX, seedY, 2.0, 4.5)
			if !okCore || !okRing {
				rejectedMask[seedIdx] = true
				continue
			}
			prominence := peak - ringMedian
			if prominence < math.Max(minProminence, 1.5*sigma) {
				rejectedMask[seedIdx] = true
				rejectedLowProminenceMask[seedIdx] = true
				continue
			}
			if coreMean-ringMedian < math.Max(0.02, sigma) {
				rejectedMask[seedIdx] = true
				rejectedLowProminenceMask[seedIdx] = true
				continue
			}
			if ringMedian > 0 && peak/ringMedian < 1.08 {
				rejectedMask[seedIdx] = true
				rejectedLowProminenceMask[seedIdx] = true
				continue
			}
			starLikeScore := pointSourceScore(pixels, valid, width, height, seedX, seedY, peak, ringMedian)
			if !saturatedPlateau && localCandidateElongation(pixels, valid, width, height, seedX, seedY, 0) > 2.5 {
				rejectedMask[seedIdx] = true
				rejectedDiffuseMask[seedIdx] = true
				continue
			}
			if !saturatedPlateau && starLikeScore < 0.015 {
				farRingMedian, okFarRing := localAnnulusMedian(pixels, valid, width, height, seedX, seedY, 6.0, 10.0)
				if !(okFarRing && peak >= 0.95 && peak-farRingMedian >= 0.05) {
					rejectedMask[seedIdx] = true
					rejectedDiffuseMask[seedIdx] = true
					continue
				}
			}
			if peak >= 0.98 && !saturatedPlateau {
				farRingMedian, okFarRing := localAnnulusMedian(pixels, valid, width, height, seedX, seedY, 5.0, 9.0)
				if okFarRing && peak-farRingMedian < 0.04 {
					rejectedMask[seedIdx] = true
					rejectedDiffuseMask[seedIdx] = true
					continue
				}
			}
			score := prominence + (peak - coreMean)
			candidates = append(candidates, candidate{
				StarSeed: StarSeed{
					X:             seedX,
					Y:             seedY,
					PeakValue:     float32(peak),
					Background:    float32(bg),
					Sigma:         float32(sigma),
					RingMedian:    float32(ringMedian),
					CoreMean:      float32(coreMean),
					Prominence:    float32(prominence),
					StarLikeScore: float32(starLikeScore),
					FootprintArea: footprintArea,
				},
				score: score,
			})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score == candidates[j].score {
			return candidates[i].PeakValue > candidates[j].PeakValue
		}
		return candidates[i].score > candidates[j].score
	})
	seeds := make([]StarSeed, 0, len(candidates))
	for _, cand := range candidates {
		radius := float64(suppressionRadius)
		if cand.PeakValue >= 0.95 {
			radius = math.Max(radius, float64(suppressionRadius)*1.5)
		}
		if hasNearbySeed(seeds, cand.X, cand.Y, radius) {
			rejectedMask[cand.Y*width+cand.X] = true
			continue
		}
		acceptedMask[cand.Y*width+cand.X] = true
		seeds = append(seeds, cand.StarSeed)
	}
	return seeds, rawMask, acceptedMask, rejectedMask, rejectedSmallMask, rejectedLowProminenceMask, rejectedDiffuseMask, nil
}

func BuildSeedStarMask(detection []float32, valid []bool, width, height int, model BackgroundModel, seeds []StarSeed, baseRadius, maxRadius int) ([]bool, []StarSeed) {
	mask := make([]bool, width*height)
	outSeeds := EstimateSeedRadii(detection, valid, width, height, model, seeds, baseRadius, maxRadius)
	for i := range outSeeds {
		paintSeedCircle(mask, valid, width, height, outSeeds[i])
	}
	return mask, outSeeds
}

func EstimateSeedRadii(detection []float32, valid []bool, width, height int, model BackgroundModel, seeds []StarSeed, baseRadius, maxRadius int) []StarSeed {
	if maxRadius <= 0 {
		maxRadius = maxStarInt(baseRadius+1, 8)
	}
	outSeeds := make([]StarSeed, len(seeds))
	copy(outSeeds, seeds)
	for i, seed := range outSeeds {
		outSeeds[i].Radius = estimateSeedRadius(detection, valid, width, height, model, seed, baseRadius, maxRadius)
	}
	return outSeeds
}

func BuildSeedStarMaskFromRadii(valid []bool, width, height int, seeds []StarSeed, baseRadius, maxRadius int) ([]bool, []StarSeed) {
	mask := make([]bool, width*height)
	if maxRadius <= 0 {
		maxRadius = maxStarInt(baseRadius+1, 8)
	}
	outSeeds := make([]StarSeed, len(seeds))
	copy(outSeeds, seeds)
	for i := range outSeeds {
		if outSeeds[i].Radius < float64(baseRadius) {
			outSeeds[i].Radius = float64(baseRadius)
		}
		if outSeeds[i].Radius > float64(maxRadius) {
			outSeeds[i].Radius = float64(maxRadius)
		}
		paintSeedCircle(mask, valid, width, height, outSeeds[i])
	}
	return mask, outSeeds
}

func paintSeedCircle(mask []bool, valid []bool, width, height int, seed StarSeed) {
	radius := seed.Radius
	if radius <= 0 {
		radius = 1
	}
	cr := int(math.Ceil(radius))
	r2 := radius * radius
	x0 := maxStarInt(0, seed.X-cr)
	x1 := minInt(width-1, seed.X+cr)
	y0 := maxStarInt(0, seed.Y-cr)
	y1 := minInt(height-1, seed.Y+cr)
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			idx := y*width + x
			if valid != nil && !valid[idx] {
				continue
			}
			dx := float64(x - seed.X)
			dy := float64(y - seed.Y)
			if dx*dx+dy*dy <= r2 {
				mask[idx] = true
			}
		}
	}
}

func medianAndMADSigma(values []float64) (float64, float64) {
	if len(values) == 0 {
		return 0, localSigmaFloor
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	median := percentileSorted(sorted, 50.0)
	devs := make([]float64, len(sorted))
	for i, v := range sorted {
		devs[i] = math.Abs(v - median)
	}
	sort.Float64s(devs)
	mad := percentileSorted(devs, 50.0)
	sigma := 1.4826 * mad
	if !isFiniteStar64(sigma) || sigma < localSigmaFloor {
		sigma = localSigmaFloor
	}
	return median, sigma
}

func finiteFloat64s(pixels []float32) []float64 {
	return finiteFloat64sWithValidity(pixels, nil)
}

func finiteFloat64sWithValidity(pixels []float32, valid []bool) []float64 {
	values := make([]float64, 0, len(pixels))
	for i, v := range pixels {
		if (valid == nil || valid[i]) && isFiniteStar32(v) {
			values = append(values, float64(v))
		}
	}
	return values
}

func BuildValidityMask(pixels []float32, floor float32) []bool {
	return BuildValidityMaskWithNoDataFloor(pixels, floor, true)
}

func BuildValidityMaskWithNoDataFloor(pixels []float32, floor float32, useFloor bool) []bool {
	valid := make([]bool, len(pixels))
	for i, v := range pixels {
		valid[i] = isFiniteStar32(v) && (!useFloor || v > floor)
	}
	return valid
}

func BuildSharedValidityMask(channelValid [][]bool, width, height, minSharedChannels int) ([]bool, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("width and height must be > 0")
	}
	if len(channelValid) == 0 {
		return nil, fmt.Errorf("expected at least one channel validity mask")
	}
	total := width * height
	if minSharedChannels < 1 {
		minSharedChannels = 1
	}
	if minSharedChannels > len(channelValid) {
		minSharedChannels = len(channelValid)
	}
	shared := make([]bool, total)
	for _, valid := range channelValid {
		if len(valid) != total {
			return nil, fmt.Errorf("channel valid mask length = %d, want %d", len(valid), total)
		}
	}
	for i := 0; i < total; i++ {
		count := 0
		for _, valid := range channelValid {
			if valid[i] {
				count++
			}
		}
		shared[i] = count >= minSharedChannels
	}
	return shared, nil
}

func collectPeakPlateau(pixels []float32, valid []bool, rawMask []bool, visited []bool, width, height, start int) []int {
	peak := float64(pixels[start])
	eps := math.Max(1e-6, math.Abs(peak)*1e-6)
	minSaturatedPlateau := math.Max(0.95, peak-0.02)
	queue := []int{start}
	visitedLocal := map[int]struct{}{start: {}}
	plateau := make([]int, 0, 8)
	dirs := [8][2]int{{-1, -1}, {0, -1}, {1, -1}, {-1, 0}, {1, 0}, {-1, 1}, {0, 1}, {1, 1}}
	for len(queue) > 0 {
		idx := queue[0]
		queue = queue[1:]
		plateau = append(plateau, idx)
		x := idx % width
		y := idx / width
		for _, d := range dirs {
			nx, ny := x+d[0], y+d[1]
			if nx < 0 || nx >= width || ny < 0 || ny >= height {
				continue
			}
			nIdx := ny*width + nx
			if _, ok := visitedLocal[nIdx]; ok {
				continue
			}
			if visited[nIdx] || !rawMask[nIdx] || (valid != nil && !valid[nIdx]) {
				continue
			}
			v := float64(pixels[nIdx])
			if !isFiniteStar64(v) {
				continue
			}
			sameFlatValue := math.Abs(v-peak) <= eps
			nearSaturatedPeak := peak >= 0.95 && v >= minSaturatedPlateau
			if !sameFlatValue && !nearSaturatedPeak {
				continue
			}
			visitedLocal[nIdx] = struct{}{}
			queue = append(queue, nIdx)
		}
	}
	return plateau
}

func plateauPeak(pixels []float32, plateau []int) (float64, int) {
	peak := math.Inf(-1)
	count := 0
	for _, idx := range plateau {
		v := float64(pixels[idx])
		if !isFiniteStar64(v) {
			continue
		}
		if v > peak {
			peak = v
			count = 1
			continue
		}
		eps := math.Max(1e-6, math.Abs(peak)*1e-6)
		if math.Abs(v-peak) <= eps || (peak >= 0.95 && v >= math.Max(0.95, peak-0.02)) {
			count++
		}
	}
	return peak, count
}

func plateauCentroid(pixels []float32, width int, plateau []int) (int, int) {
	if len(plateau) == 0 {
		return 0, 0
	}
	var sumX, sumY, sumW float64
	for _, idx := range plateau {
		w := math.Max(float64(pixels[idx]), 0)
		x := float64(idx % width)
		y := float64(idx / width)
		sumX += x * w
		sumY += y * w
		sumW += w
	}
	if sumW == 0 {
		for _, idx := range plateau {
			sumX += float64(idx % width)
			sumY += float64(idx / width)
		}
		sumW = float64(len(plateau))
	}
	return int(math.Round(sumX / sumW)), int(math.Round(sumY / sumW))
}

func plateauIsLocalMaximum(pixels []float32, valid []bool, width, height int, plateau []int, peak float64) bool {
	eps := math.Max(1e-6, math.Abs(peak)*1e-6)
	inPlateau := make(map[int]struct{}, len(plateau))
	for _, idx := range plateau {
		inPlateau[idx] = struct{}{}
	}
	dirs := [8][2]int{{-1, -1}, {0, -1}, {1, -1}, {-1, 0}, {1, 0}, {-1, 1}, {0, 1}, {1, 1}}
	for _, idx := range plateau {
		x := idx % width
		y := idx / width
		for _, d := range dirs {
			nx, ny := x+d[0], y+d[1]
			if nx < 0 || nx >= width || ny < 0 || ny >= height {
				continue
			}
			nIdx := ny*width + nx
			if _, ok := inPlateau[nIdx]; ok {
				continue
			}
			if valid != nil && !valid[nIdx] {
				continue
			}
			v := float64(pixels[nIdx])
			if isFiniteStar64(v) && v > peak+eps {
				return false
			}
		}
	}
	return true
}

func hasNearbySeed(seeds []StarSeed, x, y int, radius float64) bool {
	r2 := radius * radius
	for _, seed := range seeds {
		dx := float64(seed.X - x)
		dy := float64(seed.Y - y)
		if dx*dx+dy*dy <= r2 {
			return true
		}
	}
	return false
}

func localMeanWithinRadius(pixels []float32, valid []bool, width, height, cx, cy int, radius float64) (float64, bool) {
	r2 := radius * radius
	var sum float64
	count := 0
	cr := int(math.Ceil(radius))
	for y := maxStarInt(0, cy-cr); y <= minInt(height-1, cy+cr); y++ {
		for x := maxStarInt(0, cx-cr); x <= minInt(width-1, cx+cr); x++ {
			idx := y*width + x
			if valid != nil && !valid[idx] {
				continue
			}
			v := float64(pixels[idx])
			if !isFiniteStar64(v) {
				continue
			}
			dx := float64(x - cx)
			dy := float64(y - cy)
			if dx*dx+dy*dy <= r2 {
				sum += v
				count++
			}
		}
	}
	if count == 0 {
		return 0, false
	}
	return sum / float64(count), true
}

func localAnnulusMedian(pixels []float32, valid []bool, width, height, cx, cy int, innerRadius, outerRadius float64) (float64, bool) {
	values := make([]float64, 0)
	inner2 := innerRadius * innerRadius
	outer2 := outerRadius * outerRadius
	cr := int(math.Ceil(outerRadius))
	for y := maxStarInt(0, cy-cr); y <= minInt(height-1, cy+cr); y++ {
		for x := maxStarInt(0, cx-cr); x <= minInt(width-1, cx+cr); x++ {
			idx := y*width + x
			if valid != nil && !valid[idx] {
				continue
			}
			v := float64(pixels[idx])
			if !isFiniteStar64(v) {
				continue
			}
			dx := float64(x - cx)
			dy := float64(y - cy)
			d2 := dx*dx + dy*dy
			if d2 > inner2 && d2 <= outer2 {
				values = append(values, v)
			}
		}
	}
	if len(values) == 0 {
		return 0, false
	}
	sort.Float64s(values)
	return percentileSorted(values, 50.0), true
}

func hasPointSourceFalloff(pixels []float32, valid []bool, width, height, x, y int, peak, ringMedian float64) bool {
	return pointSourceScore(pixels, valid, width, height, x, y, peak, ringMedian) >= 0.015
}

func pointSourceScore(pixels []float32, valid []bool, width, height, x, y int, peak, ringMedian float64) float64 {
	innerMean, okInner := localMeanWithinRadius(pixels, valid, width, height, x, y, 1.25)
	outerMean, okOuter := localMeanWithinRadius(pixels, valid, width, height, x, y, 3.0)
	if !okInner || !okOuter {
		return 0
	}
	if peak-innerMean < 0 {
		return 0
	}
	if innerMean <= outerMean {
		return 0
	}
	if outerMean-ringMedian < -0.01 {
		return 0
	}
	annulusContrast := innerMean - outerMean
	peakContrast := peak - ringMedian
	if peakContrast <= 0 {
		return 0
	}
	return annulusContrast * math.Min(2.0, peakContrast)
}

func localCandidateElongation(pixels []float32, valid []bool, width, height, cx, cy int, floor float64) float64 {
	const radius = 4
	var sumW, sumX, sumY float64
	for y := maxStarInt(0, cy-radius); y <= minInt(height-1, cy+radius); y++ {
		for x := maxStarInt(0, cx-radius); x <= minInt(width-1, cx+radius); x++ {
			idx := y*width + x
			if valid != nil && !valid[idx] {
				continue
			}
			v := float64(pixels[idx])
			if !isFiniteStar64(v) || v <= floor {
				continue
			}
			w := v - floor
			sumW += w
			sumX += float64(x) * w
			sumY += float64(y) * w
		}
	}
	if sumW <= 0 {
		return 1
	}
	meanX, meanY := sumX/sumW, sumY/sumW
	var xx, yy, xy float64
	for y := maxStarInt(0, cy-radius); y <= minInt(height-1, cy+radius); y++ {
		for x := maxStarInt(0, cx-radius); x <= minInt(width-1, cx+radius); x++ {
			idx := y*width + x
			if valid != nil && !valid[idx] {
				continue
			}
			v := float64(pixels[idx])
			if !isFiniteStar64(v) || v <= floor {
				continue
			}
			w := v - floor
			dx, dy := float64(x)-meanX, float64(y)-meanY
			xx += w * dx * dx
			yy += w * dy * dy
			xy += w * dx * dy
		}
	}
	xx /= sumW
	yy /= sumW
	xy /= sumW
	trace := xx + yy
	detTerm := math.Sqrt(math.Max(0, (xx-yy)*(xx-yy)+4*xy*xy))
	large := (trace + detTerm) / 2
	small := (trace - detTerm) / 2
	if small <= 1e-6 {
		return math.Inf(1)
	}
	return large / small
}

func estimateSeedRadius(detection []float32, valid []bool, width, height int, model BackgroundModel, seed StarSeed, baseRadius, maxRadius int) float64 {
	if baseRadius < 1 {
		baseRadius = 1
	}
	effectiveMaxRadius := maxRadius
	if seed.PeakValue < 0.95 {
		limit := 24
		if seed.StarLikeScore < 0.08 {
			limit = 16
		}
		effectiveMaxRadius = minInt(maxRadius, maxStarInt(baseRadius, limit))
	}
	stopLevel := float64(seed.Background) + math.Max(float64(seed.Sigma)*1.5, float64(seed.Prominence)*0.12)
	last := float64(baseRadius)
	for r := 1; r <= effectiveMaxRadius; r++ {
		ringMedian, ok := localAnnulusMedian(detection, valid, width, height, seed.X, seed.Y, math.Max(0, float64(r)-1.0), float64(r)+0.5)
		if !ok {
			continue
		}
		if ringMedian > stopLevel {
			last = float64(r + 1)
			continue
		}
		if r <= baseRadius {
			last = float64(baseRadius)
		}
		break
	}
	if last < float64(baseRadius) {
		last = float64(baseRadius)
	}
	if seed.PeakValue >= 0.95 {
		saturatedMin := minInt(20, maxStarInt(baseRadius, minInt(width, height)/3))
		last = math.Max(last, float64(minInt(maxRadius, saturatedMin)))
	}
	if last > float64(effectiveMaxRadius) && seed.PeakValue < 0.95 {
		last = float64(effectiveMaxRadius)
	} else if last > float64(maxRadius) {
		last = float64(maxRadius)
	}
	return last
}

func resolveStarMaskMaxRadius(settings StarMaskSettings) int {
	maxRadius := settings.MaskMaxRadius
	if maxRadius == 0 {
		maxRadius = settings.MaskGrowRadius * 4
		if maxRadius < settings.MaskGrowRadius+1 {
			maxRadius = settings.MaskGrowRadius + 1
		}
	}
	return maxRadius
}

func detectFallbackBrightSeed(pixels []float32, valid []bool, width, height int, model BackgroundModel) (StarSeed, bool) {
	bestIdx := -1
	bestVal := math.Inf(-1)
	for idx, v := range pixels {
		if valid != nil && !valid[idx] {
			continue
		}
		fv := float64(v)
		if !isFiniteStar64(fv) {
			continue
		}
		if fv > bestVal {
			bestVal = fv
			bestIdx = idx
		}
	}
	if bestIdx < 0 {
		return StarSeed{}, false
	}
	x := bestIdx % width
	y := bestIdx / width
	ringMedian, okRing := localAnnulusMedian(pixels, valid, width, height, x, y, 5.0, 10.0)
	if !okRing || bestVal-ringMedian < 0.04 {
		return StarSeed{}, false
	}
	coreMean, okCore := localMeanWithinRadius(pixels, valid, width, height, x, y, 1.5)
	if !okCore {
		coreMean = bestVal
	}
	return StarSeed{
		X:          x,
		Y:          y,
		PeakValue:  pixels[bestIdx],
		Background: model.Background[bestIdx],
		Sigma:      model.Sigma[bestIdx],
		RingMedian: float32(ringMedian),
		CoreMean:   float32(coreMean),
		Prominence: float32(bestVal - ringMedian),
	}, true
}
func isFiniteStar32(v float32) bool { f := float64(v); return !math.IsNaN(f) && !math.IsInf(f, 0) }
func isFiniteStar64(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func maxStarFloat32(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}
func maxStarInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func validateChannelSet(channels [][]float32, width, height int) error {
	if width <= 0 || height <= 0 {
		return fmt.Errorf("width and height must be > 0")
	}
	if len(channels) < 1 || len(channels) > 3 {
		return fmt.Errorf("expected 1 to 3 channels, got %d", len(channels))
	}
	total := width * height
	if total <= 0 {
		return fmt.Errorf("invalid image dimensions")
	}
	for i, ch := range channels {
		if len(ch) != total {
			return fmt.Errorf("channel %d length = %d, want %d", i, len(ch), total)
		}
	}
	return nil
}
