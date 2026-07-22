package models

import (
	"encoding/json"
	"testing"
)

func TestMosaicProjectRoundTripPreservesQueueRelevantInputState(t *testing.T) {
	want := MosaicProject{
		ExposureNormMode: 2,
		Inputs: []MosaicInputState{{
			Path: "frame.fits", Excluded: true, NormalizeExposure: true,
			ExposureScale: 0.01, OffsetX: 2.5, OffsetY: -1.5,
		}},
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got MosaicProject
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.ExposureNormMode != want.ExposureNormMode || len(got.Inputs) != 1 {
		t.Fatalf("project = %+v", got)
	}
	input := got.Inputs[0]
	if !input.Excluded || !input.NormalizeExposure || input.ExposureScale != want.Inputs[0].ExposureScale {
		t.Fatalf("input queue state = %+v", input)
	}
}

func TestMosaicProjectLegacyJSONDefaultsQueueState(t *testing.T) {
	var project MosaicProject
	if err := json.Unmarshal([]byte(`{"inputs":[{"path":"frame.fits"}]}`), &project); err != nil {
		t.Fatal(err)
	}
	if project.ExposureNormMode != 0 || len(project.Inputs) != 1 {
		t.Fatalf("legacy project = %+v", project)
	}
	if project.Inputs[0].Excluded || project.Inputs[0].NormalizeExposure || project.Inputs[0].ExposureScale != 0 {
		t.Fatalf("legacy input defaults = %+v", project.Inputs[0])
	}
}
