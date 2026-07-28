package models

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestLegacyComposeProjectCalibrationDefaults(t *testing.T) {
	var project ComposeProject
	if err := json.Unmarshal([]byte(`{"channels":[{"path":"r.fits"}]}`), &project); err != nil {
		t.Fatal(err)
	}
	if project.ColorCalibration != nil {
		t.Fatal("legacy project unexpectedly has calibration state")
	}
	if got := (ColorCalibrationState{}).Effective(); got.PhotometricMode != PhotometricOff || got.Status != CalibrationDisabled {
		t.Fatalf("defaults = %+v", got)
	}
	if got := (OverlayCalibrationState{}); got.Mode != "" {
		t.Fatal("zero overlay mode should remain omitted for compatibility")
	}
	if project.DisableColorCalibration {
		t.Fatal("legacy project unexpectedly disabled calibration")
	}
}

func TestComposeProjectDisableColorCalibrationRoundTrip(t *testing.T) {
	project := ComposeProject{DisableColorCalibration: true, ColorCalibration: &ColorCalibrationState{Status: CalibrationValid, BaseTransforms: [3]LinearTransform{{Gain: 1}, {Gain: 1}, {Gain: 1}}}}
	data, err := json.Marshal(project)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ComposeProject
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.DisableColorCalibration || decoded.ColorCalibration == nil || decoded.ColorCalibration.Status != CalibrationValid {
		t.Fatalf("project round trip lost disable/calibration state: %+v", decoded)
	}
}

func TestColorCalibrationStateRoundTripAndValidation(t *testing.T) {
	project := ComposeProject{ColorCalibration: &ColorCalibrationState{
		Version: 1, PhotometricMode: PhotometricInstrument, NeutralizeBackground: true,
		BackgroundSelection: BackgroundROI, BackgroundROI: CalibrationROI{X: 2, Y: 3, Width: 8, Height: 9},
		WhiteReference: WhiteReferenceFlatFlambda,
		BaseTransforms: [3]LinearTransform{{Offset: -2.5, Gain: 1.2}, {Offset: 0, Gain: 0.8}, {Offset: -0.25, Gain: 2}},
		Overlays:       []OverlayCalibrationState{{Mode: OverlayCalibratedLinear, Transform: LinearTransform{Offset: -1, Gain: .5}, Strength: -0.75, Status: CalibrationValid}},
		Status:         CalibrationValid, SourceFingerprint: "source", SettingsFingerprint: "settings",
	}}
	data, err := json.Marshal(project)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ComposeProject
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	state := decoded.ColorCalibration
	if state == nil || state.BaseTransforms[0].Offset != -2.5 || state.BaseTransforms[2].Gain != 2 || state.Overlays[0].Strength != -.75 {
		t.Fatalf("decoded calibration = %+v", state)
	}
	for _, raw := range []string{
		`{"status":"valid","baseTransforms":[{"offset":0,"gain":NaN}]}`,
		`{"status":"valid","baseTransforms":[{"offset":0,"gain":-1}]}`,
		`{"photometricMode":"bogus"}`,
		`{"status":"disabled","baseTransforms":[{"offset":0,"gain":-1}]}`,
	} {
		var got ColorCalibrationState
		if err := json.Unmarshal([]byte(raw), &got); err == nil {
			t.Fatalf("invalid state %s accepted", raw)
		}
	}
	var gaiaState ColorCalibrationState
	if err := json.Unmarshal([]byte(`{"photometricMode":"gaia"}`), &gaiaState); err != nil || gaiaState.PhotometricMode != PhotometricGaia {
		t.Fatalf("phase 2 Gaia mode rejected: state=%+v err=%v", gaiaState, err)
	}
	if math.IsNaN(decoded.ColorCalibration.BaseTransforms[1].Gain) {
		t.Fatal("NaN survived round trip")
	}
}

func TestOverlayCalibrationDefaultsNormalizeOnDecode(t *testing.T) {
	var state ColorCalibrationState
	if err := json.Unmarshal([]byte(`{"overlays":[{}]}`), &state); err != nil {
		t.Fatal(err)
	}
	if state.Overlays[0].Mode != OverlayArtistic || state.Overlays[0].Status != CalibrationDisabled {
		t.Fatalf("overlay defaults = %+v", state.Overlays[0])
	}
}

func TestExplicitZeroTransformsRejectedForEveryTerminalStatus(t *testing.T) {
	for _, status := range []string{"disabled", "stale", "cancelled", "failed"} {
		for _, raw := range []string{
			`{"status":"` + status + `","baseTransforms":[{"offset":0,"gain":0}]}`,
			`{"status":"` + status + `","overlays":[{"transform":{"offset":0,"gain":0}}]}`,
		} {
			var state ColorCalibrationState
			if err := json.Unmarshal([]byte(raw), &state); err == nil {
				t.Fatalf("status %s accepted explicit zero transform: %s", status, raw)
			}
		}
	}
}

func TestComposeProjectBlinkChannelsRoundTrip(t *testing.T) {
	channels := []int{0, 3, 7}
	project := ComposeProject{BlinkChannels: &channels}
	data, err := json.Marshal(project)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ComposeProject
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.BlinkChannels == nil || len(*decoded.BlinkChannels) != 3 || (*decoded.BlinkChannels)[1] != 3 || (*decoded.BlinkChannels)[2] != 7 {
		t.Fatalf("blink channels = %#v, want [0 3 7]", decoded.BlinkChannels)
	}
	empty := []int{}
	data, err = json.Marshal(ComposeProject{BlinkChannels: &empty})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == "{}" {
		t.Fatal("explicit empty blink selection was omitted")
	}
}

func TestMosaicProjectRoundTripPreservesNestedSettingsAndOptionalFields(t *testing.T) {
	project := MosaicProject{
		Inputs: []MosaicInputState{
			{
				Path:         "ref_flc.fits",
				SCIExt:       2,
				Combined:     true,
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
			FinalScale:            0.04,
			Scale:                 1.5,
			PixFrac:               0.8,
			CRMethod:              2,
			SepKernel:             3,
			FinalKernel:           4,
			WeightingMode:         1,
			UseERRWeighting:       true,
			SurfaceBrightnessNorm: true,
			CRSeedSNR:             4.2,
			CRDerivScale:          1.6,
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
			Enabled:                true,
			SkyMethod:              2,
			SkyStat:                3,
			SkyWidth:               0.25,
			SkyLower:               -0.5,
			SkyUpper:               4.5,
			SkyLowerSet:            true,
			SkyUpperSet:            true,
			SkyClip:                5,
			SkyLSigma:              2.5,
			SkyUSigma:              3.5,
			AmpPedestal:            true,
			RowDestripe:            true,
			RowDestripeMaskPath:    "masks/row_mask.fits",
			RowDestripeMaskDir:     "masks/row",
			RowDestripeMaskSigma:   2.75,
			RowDestripeTrendWindow: 97,
			RowDestripeDirection:   "rows",
			NIRCamWisp:             true,
			NIRCamWispTemplateDir:  "refs/wisps",
			NIRCamWispAutoScale:    true,
			NIRCamWispScale:        1.75,
			MIRIArtifactMask:       true,
			MIRIArtifactMaskPath:   "masks/miri_direct.fits",
			MIRIArtifactMaskDir:    "masks/miri",
		},
		SkysubSettingsSet: true,
		ActiveFilter:      "F502N",
		ArtifactMasks: &ArtifactMaskProject{
			Version: 1,
			Documents: []ArtifactMaskDocument{
				{
					ID:         "mask-1",
					Name:       "MIRI shower",
					Purpose:    ArtifactMaskPurposeMIRIArtifact,
					SourceMode: ArtifactMaskSourceInput,
					SourceKey:  "miri_cal.fits#sci1",
					Width:      3,
					Height:     2,
					Targets: []ArtifactMaskTarget{
						{Key: "miri_cal.fits#sci1", Path: "miri_cal.fits", SCIExt: 1, Selected: true},
					},
					Operations: []ArtifactMaskOperation{
						{
							Mode:   ArtifactMaskOperationAdd,
							Kind:   ArtifactMaskRegionRaster,
							Width:  3,
							Height: 2,
							Mask:   []byte{1, 0, 0, 0, 1, 0},
						},
					},
				},
			},
		},
	}
	project.SkysubSettings.EqualizeDisconnectedBackgrounds = true

	var decoded MosaicProject
	roundTripJSON(t, project, &decoded)

	if len(decoded.Inputs) != 1 {
		t.Fatalf("len(Inputs) = %d, want 1", len(decoded.Inputs))
	}
	if decoded.Inputs[0].SCIExt != 2 || !decoded.Inputs[0].HasTransform || !decoded.Inputs[0].Locked {
		t.Fatalf("Inputs[0] = %+v, want preserved transform and lock state", decoded.Inputs[0])
	}
	if !decoded.Inputs[0].Combined {
		t.Fatalf("Inputs[0].Combined = false, want true (combined flag must round-trip)")
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
	if !decoded.SkysubSettings.EqualizeDisconnectedBackgrounds {
		t.Fatalf("EqualizeDisconnectedBackgrounds = false, want round-tripped true")
	}
	if !decoded.SkysubSettings.AmpPedestal || !decoded.SkysubSettings.RowDestripe || !decoded.SkysubSettings.NIRCamWisp {
		t.Fatalf("Skysub detector corrections = %+v, want enabled settings preserved", decoded.SkysubSettings)
	}
	if decoded.SkysubSettings.RowDestripeMaskPath != "masks/row_mask.fits" || decoded.SkysubSettings.RowDestripeMaskDir != "masks/row" || decoded.SkysubSettings.RowDestripeMaskSigma != 2.75 || decoded.SkysubSettings.RowDestripeTrendWindow != 97 || decoded.SkysubSettings.RowDestripeDirection != "rows" {
		t.Fatalf("Row destripe settings = %+v, want preserved advanced settings", decoded.SkysubSettings)
	}
	if decoded.SkysubSettings.NIRCamWispTemplateDir != "refs/wisps" || !decoded.SkysubSettings.NIRCamWispAutoScale || decoded.SkysubSettings.NIRCamWispScale != 1.75 {
		t.Fatalf("NIRCam wisp settings = %+v, want preserved template settings", decoded.SkysubSettings)
	}
	if !decoded.SkysubSettings.MIRIArtifactMask || decoded.SkysubSettings.MIRIArtifactMaskPath != "masks/miri_direct.fits" || decoded.SkysubSettings.MIRIArtifactMaskDir != "masks/miri" {
		t.Fatalf("MIRI artifact settings = %+v, want preserved mask settings", decoded.SkysubSettings)
	}
	if decoded.ReferencePath != "ref_flc.fits" || decoded.ActiveFilter != "F502N" {
		t.Fatalf("reference/filter = (%q,%q), want preserved values", decoded.ReferencePath, decoded.ActiveFilter)
	}
	if !decoded.DrizzleSettings.SurfaceBrightnessNorm {
		t.Fatal("SurfaceBrightnessNorm = false, want true")
	}
	if decoded.ArtifactMasks == nil || len(decoded.ArtifactMasks.Documents) != 1 {
		t.Fatalf("ArtifactMasks = %+v, want one persisted mask document", decoded.ArtifactMasks)
	}
	doc := decoded.ArtifactMasks.Documents[0]
	if doc.Purpose != ArtifactMaskPurposeMIRIArtifact || doc.SourceMode != ArtifactMaskSourceInput || doc.Width != 3 || doc.Height != 2 || len(doc.Operations) != 1 {
		t.Fatalf("artifact mask document = %+v, want preserved editable document", doc)
	}
	if len(doc.Targets) != 1 || !doc.Targets[0].Selected || doc.Targets[0].SCIExt != 1 {
		t.Fatalf("artifact mask targets = %+v, want selected SCI target", doc.Targets)
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
	for _, field := range []string{"referencePath", "referenceSciExt", "sciExt", "combined", "locked", "transformA", "useERRWeighting", "equalizeDisconnectedBackgrounds"} {
		if containsJSONField(raw, field) {
			t.Fatalf("JSON unexpectedly contained omitted field %q: %s", field, raw)
		}
	}
	if containsJSONField(raw, "artifactMasks") {
		t.Fatalf("JSON unexpectedly contained empty artifactMasks field: %s", raw)
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
	if decoded.SkysubSettings.AmpPedestal || decoded.SkysubSettings.RowDestripe || decoded.SkysubSettings.NIRCamWisp || decoded.SkysubSettings.MIRIArtifactMask {
		t.Fatalf("artifact corrections = %+v, want omitted fields to stay disabled", decoded.SkysubSettings)
	}
	if decoded.SkysubSettings.EqualizeDisconnectedBackgrounds {
		t.Fatal("EqualizeDisconnectedBackgrounds = true, want legacy default false")
	}
	if decoded.SkysubSettings.RowDestripeMaskPath != "" || decoded.SkysubSettings.RowDestripeMaskDir != "" || decoded.SkysubSettings.RowDestripeMaskSigma != 0 || decoded.SkysubSettings.RowDestripeTrendWindow != 0 || decoded.SkysubSettings.RowDestripeDirection != "" {
		t.Fatalf("row destripe defaults = %+v, want zero values for old projects", decoded.SkysubSettings)
	}
	if decoded.SkysubSettings.NIRCamWispTemplateDir != "" || decoded.SkysubSettings.NIRCamWispAutoScale || decoded.SkysubSettings.NIRCamWispScale != 0 {
		t.Fatalf("NIRCam wisp defaults = %+v, want zero values for old projects", decoded.SkysubSettings)
	}
	if decoded.SkysubSettings.MIRIArtifactMaskPath != "" || decoded.SkysubSettings.MIRIArtifactMaskDir != "" {
		t.Fatalf("MIRI artifact defaults = %+v, want zero values for old projects", decoded.SkysubSettings)
	}
	if decoded.ArtifactMasks != nil {
		t.Fatalf("ArtifactMasks = %+v, want nil for old projects", decoded.ArtifactMasks)
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

func TestColorCalibrationGaiaSettingsRoundTrip(t *testing.T) {
	original := ColorCalibrationState{PhotometricMode: PhotometricGaia, Status: CalibrationValid, BaseTransforms: [3]LinearTransform{{Gain: 1}, {Gain: 1}, {Gain: 1}}, Gaia: GaiaCalibrationSettings{AccessMode: "cacheOnly", Endpoint: "https://gaia.test", Release: "DR3", XPRepresentation: "xp-v1", QualitySelector: "clean", MagnitudeLimit: 18, MatchRadiusArcsec: 2, ObservationEpoch: 2024, CachePath: "cache.db", CacheMaxBytes: 1234}, Provenance: CalibrationProvenance{CatalogVersion: "DR3", SourceIDs: []uint64{1, 2}}}
	var decoded ColorCalibrationState
	roundTripJSON(t, original, &decoded)
	if decoded.PhotometricMode != PhotometricGaia || decoded.Gaia.AccessMode != "cacheOnly" || decoded.Gaia.Release != "DR3" || len(decoded.Provenance.SourceIDs) != 2 {
		t.Fatalf("round trip lost Gaia state: %+v", decoded)
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
