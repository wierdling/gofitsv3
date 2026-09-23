package starbench

import (
	"context"
	"fmt"
	"gofitsv3/internal/processing"
	"math"
	"sort"
)

type Run struct {
	VerifyOriginals *bool                      `json:"verifyOriginals,omitempty"`
	ID              string                     `json:"id"`
	Name            string                     `json:"name"`
	CreatedAt       string                     `json:"createdAt"`
	DatasetHash     string                     `json:"datasetHash"`
	MapSHA256       string                     `json:"mapSHA256"`
	ExecutableHash  string                     `json:"executableHash"`
	SourceVersion   string                     `json:"sourceVersion"`
	EvidenceMode    string                     `json:"evidenceMode"`
	Origin          string                     `json:"origin"`
	MinSNR          float64                    `json:"minSNR"`
	MaxResidual     float64                    `json:"maxResidual"`
	FWHM            float64                    `json:"fwhm"`
	RequestedFWHM   *float64                   `json:"requestedFWHM,omitempty"`
	Sources         []processing.StarMapSource `json:"sources"`
}
type Counts struct {
	TruePositive          int      `json:"truePositive"`
	FalsePositive         int      `json:"falsePositive"`
	FalseNegative         int      `json:"falseNegative"`
	Unresolved            int      `json:"unresolved"`
	TruthStars            int      `json:"truthStars"`
	Selected              int      `json:"selected"`
	ConservativePrecision *float64 `json:"conservativePrecision"`
	CertainPrecision      *float64 `json:"certainPrecision"`
	Recall                *float64 `json:"recall"`
}

func (c *Counts) finish() {
	if c.Selected > 0 {
		v := float64(c.TruePositive) / float64(c.Selected)
		c.ConservativePrecision = &v
	}
	if c.TruePositive+c.FalsePositive > 0 {
		v := float64(c.TruePositive) / float64(c.TruePositive+c.FalsePositive)
		c.CertainPrecision = &v
	}
	if c.TruthStars > 0 {
		v := float64(c.TruePositive) / float64(c.TruthStars)
		c.Recall = &v
	}
}
func (c *Counts) add(v Counts) {
	c.TruePositive += v.TruePositive
	c.FalsePositive += v.FalsePositive
	c.FalseNegative += v.FalseNegative
	c.Unresolved += v.Unresolved
	c.TruthStars += v.TruthStars
	c.Selected += v.Selected
	c.finish()
}

type Stratum struct {
	Tag       string   `json:"tag"`
	Truth     int      `json:"truth"`
	Recovered int      `json:"recovered"`
	Recall    *float64 `json:"recall"`
}
type ErrorPoint struct {
	RegionID string  `json:"regionId"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	Kind     string  `json:"kind"`
	SourceID int     `json:"sourceId,omitempty"`
	LabelID  string  `json:"labelId,omitempty"`
}
type RegionReport struct {
	Region                Region  `json:"region"`
	Counts                Counts  `json:"counts"`
	IgnoredBoundaryLabels int     `json:"ignoredBoundaryLabels"`
	ProtectedPixels       int     `json:"protectedPixels"`
	TouchedPixels         int     `json:"touchedPixels"`
	MaskWeight            float64 `json:"maskWeight"`
}
type Report struct {
	Version           int                  `json:"version"`
	RunID             string               `json:"runId"`
	DatasetHash       string               `json:"datasetHash"`
	LabelsRevision    int                  `json:"labelsRevision"`
	CreatedAt         string               `json:"createdAt"`
	MatchRadius       float64              `json:"matchRadius"`
	MaskThreshold     float64              `json:"maskThreshold"`
	Regions           []RegionReport       `json:"regions"`
	Splits            map[string]Counts    `json:"splits"`
	Strata            map[string][]Stratum `json:"strata"`
	Errors            []ErrorPoint         `json:"errors"`
	IncompleteRegions int                  `json:"incompleteRegions"`
	Warnings          []string             `json:"warnings"`
}

// Evaluate uses automatic statuses, never the star-map reviewer's overrides.
// Only exhaustively reviewed regions are scored. Their outer match-radius band
// is excluded on both sides, avoiding ambiguous cross-boundary ownership.
func Evaluate(p Project, run Run, pixels []float32, width, height int, tolerance float64) (Report, error) {
	if !finite(tolerance) || tolerance <= 0 || tolerance > 10 {
		return Report{}, fmt.Errorf("matching radius must be in (0,10]")
	}
	if width <= 0 || height <= 0 || width > int(^uint(0)>>1)/height || len(pixels) != width*height {
		return Report{}, fmt.Errorf("invalid science dimensions")
	}
	if run.DatasetHash != p.DatasetHash {
		return Report{}, fmt.Errorf("run belongs to another dataset")
	}
	if err := ValidateProject(p, Dataset{SHA256: p.DatasetHash, Width: width, Height: height}); err != nil {
		return Report{}, err
	}
	for _, s := range run.Sources {
		if !finite(s.X) || !finite(s.Y) || !finite(s.Radius) || s.Radius <= 0 || s.Radius > 100 {
			return Report{}, fmt.Errorf("invalid footprint for source %d", s.ID)
		}
	}
	report := Report{Version: 1, RunID: run.ID, DatasetHash: p.DatasetHash, LabelsRevision: p.Revision, CreatedAt: now(), MatchRadius: tolerance, MaskThreshold: .05, Splits: map[string]Counts{}, Strata: map[string][]Stratum{}, Regions: []RegionReport{}, Errors: []ErrorPoint{}, Warnings: []string{"These are development measurements, not certification of 99% precision. Ambiguous/unusable selections lower conservative precision.", "A region must be labeled exhaustively before Reviewed is checked. Unmatched selections in reviewed regions count as false positives.", "The outer matching-radius band is excluded from source matching. Footprint leakage includes all protected valid pixels, including selections centered outside a region."}}
	tags := []string{"bright", "saturated", "on-nebula", "blended"}
	for _, split := range []string{"development", "validation"} {
		report.Splits[split] = Counts{}
		for _, tag := range tags {
			report.Strata[split] = append(report.Strata[split], Stratum{Tag: tag})
		}
	}
	for _, region := range p.Regions {
		if !region.Reviewed {
			report.IncompleteRegions++
			continue
		}
		if region.EditedAfterReveal {
			report.Warnings = append(report.Warnings, region.Name+": labels changed after detector reveal; this region is not blind validation")
		}
		if region.RevealedBeforeReview {
			report.Warnings = append(report.Warnings, region.Name+": detector revealed before exhaustive review; this region is not blind validation")
		}
		rr := RegionReport{Region: region}
		truth := []Label{}
		others := []Label{}
		for _, l := range p.Labels {
			if l.RegionID != region.ID {
				continue
			}
			if !inside(region, l.X, l.Y, tolerance) {
				rr.IgnoredBoundaryLabels++
				continue
			}
			if l.Kind == "star" {
				truth = append(truth, l)
			} else {
				others = append(others, l)
			}
		}
		candidates := []processing.StarMapSource{}
		for _, s := range run.Sources {
			if s.Status == "accepted" && inside(region, s.X, s.Y, tolerance) {
				candidates = append(candidates, s)
			}
		}
		sort.Slice(truth, func(i, j int) bool { return truth[i].ID < truth[j].ID })
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
		// Maximum-cardinality one-to-one matching avoids greedy collisions for close
		// pairs. Neighbor order and IDs make ties deterministic.
		edges := make([][]int, len(truth))
		for i, l := range truth {
			for j, s := range candidates {
				if math.Hypot(l.X-s.X, l.Y-s.Y) <= tolerance {
					edges[i] = append(edges[i], j)
				}
			}
			sort.SliceStable(edges[i], func(a, b int) bool {
				x, y := candidates[edges[i][a]], candidates[edges[i][b]]
				return math.Hypot(l.X-x.X, l.Y-x.Y) < math.Hypot(l.X-y.X, l.Y-y.Y)
			})
		}
		owner := make([]int, len(candidates))
		for i := range owner {
			owner[i] = -1
		}
		var augment func(int, []bool) bool
		augment = func(i int, seen []bool) bool {
			for _, j := range edges[i] {
				if seen[j] {
					continue
				}
				seen[j] = true
				if owner[j] < 0 || augment(owner[j], seen) {
					owner[j] = i
					return true
				}
			}
			return false
		}
		for i := range truth {
			augment(i, make([]bool, len(candidates)))
		}
		recovered := make([]bool, len(truth))
		rr.Counts.TruthStars = len(truth)
		rr.Counts.Selected = len(candidates)
		for j, s := range candidates {
			if owner[j] >= 0 {
				rr.Counts.TruePositive++
				recovered[owner[j]] = true
				continue
			}
			known := false
			unknown := false
			for _, l := range truth {
				if math.Hypot(s.X-l.X, s.Y-l.Y) <= tolerance {
					known = true
				}
			}
			for _, l := range others {
				if math.Hypot(s.X-l.X, s.Y-l.Y) <= tolerance {
					if l.Kind == "nonstar" {
						known = true
					} else {
						unknown = true
					}
				}
			}
			kind := "false-positive"
			if unknown && !known {
				rr.Counts.Unresolved++
				kind = "unresolved"
			} else {
				rr.Counts.FalsePositive++
			}
			report.Errors = append(report.Errors, ErrorPoint{RegionID: region.ID, X: s.X, Y: s.Y, Kind: kind, SourceID: s.ID})
		}
		for i, l := range truth {
			if !recovered[i] {
				rr.Counts.FalseNegative++
				report.Errors = append(report.Errors, ErrorPoint{RegionID: region.ID, X: l.X, Y: l.Y, Kind: "missed-star", LabelID: l.ID})
			}
			for _, tag := range l.Tags {
				for k := range report.Strata[region.Split] {
					st := &report.Strata[region.Split][k]
					if st.Tag == tag {
						st.Truth++
						if recovered[i] {
							st.Recovered++
						}
					}
				}
			}
		}
		rr.Counts.finish()
		// Union protected rectangles so overlapping marks do not double the score.
		protected := make([]bool, region.Width*region.Height)
		for _, b := range p.Protected {
			if b.RegionID != region.ID {
				continue
			}
			for y := b.Y; y < b.Y+b.Height; y++ {
				for x := b.X; x < b.X+b.Width; x++ {
					protected[(y-region.Y)*region.Width+x-region.X] = true
				}
			}
		}
		// Use the production rasterizer on this cutout, retaining footprints whose
		// centers lie outside it. Automatic scoring explicitly clears overrides.
		crop := make([]float32, region.Width*region.Height)
		for y := 0; y < region.Height; y++ {
			copy(crop[y*region.Width:(y+1)*region.Width], pixels[(region.Y+y)*width+region.X:(region.Y+y)*width+region.X+region.Width])
		}
		sm := processing.StarMap{Width: region.Width, Height: region.Height}
		for _, s := range run.Sources {
			s.X -= float64(region.X)
			s.Y -= float64(region.Y)
			s.Override = ""
			sm.Sources = append(sm.Sources, s)
		}
		mask, _, _, err := sm.Rasterize(context.Background(), crop)
		if err != nil {
			return Report{}, err
		}
		for i, on := range protected {
			if !on {
				continue
			}
			x, y := region.X+i%region.Width, region.Y+i/region.Width
			if !finite(float64(pixels[y*width+x])) {
				continue
			}
			rr.ProtectedPixels++
			weight := 0.
			weight = float64(mask[i])
			rr.MaskWeight += weight
			if weight > report.MaskThreshold {
				rr.TouchedPixels++
			}
		}
		c := report.Splits[region.Split]
		c.add(rr.Counts)
		report.Splits[region.Split] = c
		report.Regions = append(report.Regions, rr)
	}
	for split, list := range report.Strata {
		for i := range list {
			if list[i].Truth > 0 {
				v := float64(list[i].Recovered) / float64(list[i].Truth)
				list[i].Recall = &v
			}
		}
		report.Strata[split] = list
	}
	return report, nil
}
