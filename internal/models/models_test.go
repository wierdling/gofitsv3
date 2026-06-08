package models

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestComposeProjectStarlessSettingsRoundTripPreservesZeroValues(t *testing.T) {
	project := ComposeProject{
		Flip:                 true,
		SharedHistogramScale: true,
		DisableComposite:     true,
		MeasureComposite:     true,
		BlinkFilters:         true,
		BlinkExcludedFilter:  2,
		Channels: [3]ChannelState{
			{
				Path:       "blue.fits",
				Mode:       "Linear",
				Black:      1.1,
				White:      9.9,
				Background: 2.2,
				Peak:       8.8,
				ScaledPeak: 7.7,
				ShowClip:   true,
			},
		},
		OrangeLayer: OrangeLayerState{
			Open: true,
			Channel: ChannelState{
				Path:       "ha.fits",
				Mode:       "Asinh",
				Black:      0.1,
				White:      2.5,
				Background: 0.2,
				Peak:       1.8,
				ScaledPeak: 9,
				ShowClip:   true,
			},
			ColorR:  159,
			ColorG:  140,
			ColorB:  80,
			Opacity: 0.65,
		},
		StarlessSettings: StarlessComposeSettings{
			Enabled:                 true,
			DetectionMode:           "median",
			DetectionPreprocessMode: "dog",
			DetectionMergeMode:      "per-channel-merged",
			ThresholdSigma:          4.5,
			BackgroundTileSize:      32,
			UseNoDataFloor:          true,
			NoDataFloor:             0.02,
			SeedMinProminence:       0.04,
			MinDetectedChannels:     2,
			MinSeedFootprintArea:    5,
			MinSharedChannels:       2,
			SuppressionRadius:       5,
			MaskBaseRadius:          0,
			MaxRadius:               6,
			FeatherRadius:           0,
			InpaintRadius:           5,
			StarBrightness:          1.0,
			StarSaturation:          0.4,
			ExportDebugMasks:        false,
		},
	}

	data, err := json.Marshal(project)
	if err != nil {
		t.Fatalf("json.Marshal error = %v", err)
	}

	var decoded ComposeProject
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal error = %v", err)
	}

	if decoded.StarlessSettings.MaskBaseRadius != 0 {
		t.Fatalf("MaskBaseRadius = %d, want 0", decoded.StarlessSettings.MaskBaseRadius)
	}
	if decoded.StarlessSettings.FeatherRadius != 0 {
		t.Fatalf("FeatherRadius = %d, want 0", decoded.StarlessSettings.FeatherRadius)
	}
	if decoded.StarlessSettings.NoDataFloor != 0.02 {
		t.Fatalf("NoDataFloor = %v, want 0.02", decoded.StarlessSettings.NoDataFloor)
	}
	if decoded.StarlessSettings.DetectionMergeMode != "per-channel-merged" {
		t.Fatalf("DetectionMergeMode = %q, want per-channel-merged", decoded.StarlessSettings.DetectionMergeMode)
	}
	if decoded.StarlessSettings.DetectionPreprocessMode != "dog" {
		t.Fatalf("DetectionPreprocessMode = %q, want dog", decoded.StarlessSettings.DetectionPreprocessMode)
	}
	if !decoded.StarlessSettings.UseNoDataFloor {
		t.Fatal("UseNoDataFloor = false, want true")
	}
	if decoded.StarlessSettings.SeedMinProminence != 0.04 {
		t.Fatalf("SeedMinProminence = %v, want 0.04", decoded.StarlessSettings.SeedMinProminence)
	}
	if decoded.StarlessSettings.MinSeedFootprintArea != 5 {
		t.Fatalf("MinSeedFootprintArea = %d, want 5", decoded.StarlessSettings.MinSeedFootprintArea)
	}
	if decoded.StarlessSettings.MinDetectedChannels != 2 {
		t.Fatalf("MinDetectedChannels = %d, want 2", decoded.StarlessSettings.MinDetectedChannels)
	}
	if decoded.StarlessSettings.SuppressionRadius != 5 {
		t.Fatalf("SuppressionRadius = %d, want 5", decoded.StarlessSettings.SuppressionRadius)
	}
	if decoded.StarlessSettings.ExportDebugMasks != false {
		t.Fatalf("ExportDebugMasks = %v, want false", decoded.StarlessSettings.ExportDebugMasks)
	}
	if decoded.Channels[0].Path != "blue.fits" || !decoded.Channels[0].ShowClip {
		t.Fatalf("Channels[0] = %+v, want preserved compose channel state", decoded.Channels[0])
	}
	if !decoded.SharedHistogramScale || !decoded.MeasureComposite {
		t.Fatalf("compose UI settings = shared:%v measure:%v, want both true", decoded.SharedHistogramScale, decoded.MeasureComposite)
	}
	if !decoded.DisableComposite {
		t.Fatal("DisableComposite = false, want true")
	}
	if !decoded.BlinkFilters || decoded.BlinkExcludedFilter != 2 {
		t.Fatalf("blink settings = enabled:%v excluded:%d, want enabled true excluded 2", decoded.BlinkFilters, decoded.BlinkExcludedFilter)
	}
	if !decoded.OrangeLayer.Open || decoded.OrangeLayer.Channel.Path != "ha.fits" || decoded.OrangeLayer.ColorR != 159 || decoded.OrangeLayer.Opacity != 0.65 {
		t.Fatalf("orange layer = %+v, want preserved optional layer state", decoded.OrangeLayer)
	}
}

func TestMosaicProjectRoundTripPreservesNestedSettingsAndOptionalFields(t *testing.T) {
	project := MosaicProject{
		Inputs: []MosaicInputState{
			{
				Path:         "ref_flc.fits",
				SCIExt:       2,
				OffsetX:      1.25,
				OffsetY:      -2.5,
				HasTransform: true,
				Locked:       true,
				TransformA:   1,
				TransformB:   0.1,
				TransformC:   5,
				TransformD:   -0.1,
				TransformE:   1,
				TransformF:   -3,
			},
		},
		ReferencePath:   "ref_flc.fits",
		ReferenceSCIExt: 2,
		DrizzleSettings: DrizzleSettings{
			FinalScale:      0.04,
			Scale:           1.5,
			PixFrac:         0.8,
			CRMethod:        2,
			SepKernel:       3,
			FinalKernel:     4,
			WeightingMode:   1,
			UseERRWeighting: true,
			CRSeedSNR:       4.2,
			CRDerivScale:    1.6,
		},
		DrizzleSettingsSet: true,
		AlignmentSettings: AlignmentSettings{
			AlignmentMode:      1,
			SearchRadiusArcsec: 3.5,
			NumRefs:            42,
			DebugAlignment:     true,
		},
		AlignmentSettingsSet: true,
		SkysubSettings: SkysubSettings{
			Enabled:     true,
			SkyMethod:   2,
			SkyStat:     3,
			SkyWidth:    0.25,
			SkyLower:    -0.5,
			SkyUpper:    4.5,
			SkyLowerSet: true,
			SkyUpperSet: true,
			SkyClip:     5,
			SkyLSigma:   2.5,
			SkyUSigma:   3.5,
		},
		SkysubSettingsSet: true,
		ActiveFilter:      "F502N",
	}

	var decoded MosaicProject
	roundTripJSON(t, project, &decoded)

	if len(decoded.Inputs) != 1 {
		t.Fatalf("len(Inputs) = %d, want 1", len(decoded.Inputs))
	}
	if decoded.Inputs[0].SCIExt != 2 || !decoded.Inputs[0].HasTransform || !decoded.Inputs[0].Locked {
		t.Fatalf("Inputs[0] = %+v, want preserved transform and lock state", decoded.Inputs[0])
	}
	if decoded.Inputs[0].TransformB != 0.1 || decoded.Inputs[0].TransformF != -3 {
		t.Fatalf("transform fields = %+v, want preserved affine values", decoded.Inputs[0])
	}
	if !decoded.DrizzleSettingsSet || !decoded.AlignmentSettingsSet || !decoded.SkysubSettingsSet {
		t.Fatalf("settings flags = %+v, want all true", decoded)
	}
	if !decoded.DrizzleSettings.UseERRWeighting || decoded.DrizzleSettings.CRSeedSNR != 4.2 {
		t.Fatalf("DrizzleSettings = %+v, want preserved drizzle settings", decoded.DrizzleSettings)
	}
	if decoded.AlignmentSettings.NumRefs != 42 || !decoded.AlignmentSettings.DebugAlignment {
		t.Fatalf("AlignmentSettings = %+v, want preserved alignment settings", decoded.AlignmentSettings)
	}
	if !decoded.SkysubSettings.Enabled || !decoded.SkysubSettings.SkyLowerSet || decoded.SkysubSettings.SkyUSigma != 3.5 {
		t.Fatalf("SkysubSettings = %+v, want preserved sky settings", decoded.SkysubSettings)
	}
	if decoded.ReferencePath != "ref_flc.fits" || decoded.ActiveFilter != "F502N" {
		t.Fatalf("reference/filter = (%q,%q), want preserved values", decoded.ReferencePath, decoded.ActiveFilter)
	}
}

func TestMosaicProjectJSONOmitsOptionalZeroFieldsAndDecodesDefaults(t *testing.T) {
	project := MosaicProject{
		Inputs: []MosaicInputState{
			{
				Path:         "input.fits",
				OffsetX:      1,
				OffsetY:      2,
				HasTransform: false,
			},
		},
		DrizzleSettings: DrizzleSettings{},
	}

	data, err := json.Marshal(project)
	if err != nil {
		t.Fatalf("json.Marshal error = %v", err)
	}
	raw := string(data)
	for _, field := range []string{"referencePath", "referenceSciExt", "sciExt", "locked", "transformA", "useERRWeighting"} {
		if containsJSONField(raw, field) {
			t.Fatalf("JSON unexpectedly contained omitted field %q: %s", field, raw)
		}
	}

	var decoded MosaicProject
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal error = %v", err)
	}
	if decoded.ReferencePath != "" || decoded.ReferenceSCIExt != 0 {
		t.Fatalf("decoded reference fields = %+v, want zero values", decoded)
	}
	if len(decoded.Inputs) != 1 || decoded.Inputs[0].SCIExt != 0 || decoded.Inputs[0].Locked {
		t.Fatalf("decoded input = %+v, want omitted fields to stay zero", decoded.Inputs)
	}
	if decoded.DrizzleSettings.UseERRWeighting {
		t.Fatalf("UseERRWeighting = %v, want false", decoded.DrizzleSettings.UseERRWeighting)
	}
}

func TestChannelStateAndMosaicInputStateZeroAndNegativeValuesRoundTrip(t *testing.T) {
	type stateEnvelope struct {
		Channel ChannelState     `json:"channel"`
		Input   MosaicInputState `json:"input"`
	}

	original := stateEnvelope{
		Channel: ChannelState{
			Path:       "red.fits",
			Mode:       "HistEq",
			Black:      -1.5,
			White:      99.5,
			Background: -0.25,
			Peak:       12.75,
			ScaledPeak: 0,
			ShowClip:   false,
		},
		Input: MosaicInputState{
			Path:         "tile.fits",
			SCIExt:       0,
			OffsetX:      -12.5,
			OffsetY:      8.25,
			HasTransform: true,
			Locked:       false,
			TransformA:   0,
			TransformB:   -0.5,
			TransformC:   10,
			TransformD:   0.5,
			TransformE:   0,
			TransformF:   -10,
		},
	}

	var decoded stateEnvelope
	roundTripJSON(t, original, &decoded)

	if decoded.Channel.Black != -1.5 || decoded.Channel.Background != -0.25 || decoded.Channel.Mode != "HistEq" {
		t.Fatalf("Channel = %+v, want preserved negative/zero values", decoded.Channel)
	}
	if !decoded.Input.HasTransform || decoded.Input.OffsetX != -12.5 || decoded.Input.TransformB != -0.5 {
		t.Fatalf("Input = %+v, want preserved transform values", decoded.Input)
	}
}

func roundTripJSON[T any](t *testing.T, in T, out *T) {
	t.Helper()
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("json.Marshal error = %v", err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatalf("json.Unmarshal error = %v", err)
	}
}

func containsJSONField(raw, field string) bool {
	return raw != "" && strings.Contains(raw, `"`+field+`"`)
}
