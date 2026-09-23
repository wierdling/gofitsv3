package starbench

import "testing"

func TestDefaultProjectIsUnreviewedAndPartitioned(t *testing.T) {
	d := Dataset{SHA256: "data", Width: 4121, Height: 4287}
	p := DefaultProject(d)
	if err := ValidateProject(p, d); err != nil {
		t.Fatal(err)
	}
	if len(p.Regions) != 12 || len(p.Labels) != 0 {
		t.Fatal("unexpected default regions")
	}
	validation := 0
	for _, r := range p.Regions {
		if r.Reviewed || r.RevealedAt != "" {
			t.Fatal("default truth must be unreviewed and blind")
		}
		if r.Split == "validation" {
			validation++
		}
	}
	if validation != 4 {
		t.Fatalf("validation regions = %d", validation)
	}
}
func TestMergeProjectPreservesExposureAndFlagsSubsequentEdits(t *testing.T) {
	p, _, _ := fixture()
	p.Regions[0].RevealedAt = "2026-09-12T00:00:00Z"
	p.Regions[0].RevealedBeforeReview = true
	next := cloneProject(p)
	next.Regions[0].RevealedAt = ""
	next.Regions[0].RevealedBeforeReview = false
	next.Labels = append(next.Labels, label("new", "star", 10, 10))
	merged, err := MergeProject(p, next, Dataset{SHA256: p.DatasetHash, Width: 64, Height: 64})
	if err != nil {
		t.Fatal(err)
	}
	if merged.Revision != 1 || merged.Regions[0].RevealedAt != p.Regions[0].RevealedAt || !merged.Regions[0].EditedAfterReveal || !merged.Regions[0].RevealedBeforeReview {
		t.Fatalf("lost audit: %+v", merged)
	}
}
func TestMergeProjectRejectsStaleAndExposedPartitionChanges(t *testing.T) {
	for _, name := range []string{"stale", "split", "geometry", "delete", "overlap"} {
		t.Run(name, func(t *testing.T) {
			p, _, _ := fixture()
			p.Regions[0].RevealedAt = "exposed"
			next := cloneProject(p)
			switch name {
			case "stale":
				next.Revision++
			case "split":
				next.Regions[0].Split = "validation"
			case "geometry":
				next.Regions[0].Width--
			case "delete":
				next.Regions = nil
			case "overlap":
				r := next.Regions[0]
				r.ID = "overlap"
				next.Regions = append(next.Regions, r)
			}
			if _, err := MergeProject(p, next, Dataset{SHA256: p.DatasetHash, Width: 64, Height: 64}); err == nil {
				t.Fatal("invalid update accepted")
			}
		})
	}
}
