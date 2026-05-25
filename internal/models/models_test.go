package models

import (
	"encoding/json"
	"testing"
)

func TestComposeProjectStarlessSettingsRoundTripPreservesZeroValues(t *testing.T) {
	project := ComposeProject{
		Flip: true,
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
}
