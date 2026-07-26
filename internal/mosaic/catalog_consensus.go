package mosaic

import (
	"math"

	"gofitsv3/internal/processing"
)

// projectedCatalogDetection is a catalog entry projected onto the common
// reference grid. inputIndex and starIndex identify its source entry.
type projectedCatalogDetection struct {
	inputIndex int
	starIndex  int
	x          float64
	y          float64
}

type alignmentConsensusStats struct {
	Candidates   int
	Corroborated int
	Strong       int
	Selected     int
	Fallback     bool
}

const consensusMatchRadius = 4.0

type consensusDetection struct {
	frame, star int
	x, y        float64
}

// selectCrossFrameConsensusCatalogs keeps detections corroborated by at least
// one other exposure. Matching uses a spatial hash so catalogs remain linear
// in size rather than requiring all-pairs comparisons.
func selectCrossFrameConsensusCatalogs(candidates [][]processing.Star, projected [][]projectedCatalogDetection, exposures []string, maxStars int, radiusPx float64) ([][]processing.Star, []alignmentConsensusStats) {
	selected := make([][]processing.Star, len(candidates))
	stats := make([]alignmentConsensusStats, len(candidates))
	if maxStars <= 0 {
		maxStars = int(^uint(0) >> 1)
	}
	if !finite(radiusPx) || radiusPx <= 0 {
		radiusPx = consensusMatchRadius
	}

	valid := make([]consensusDetection, 0)
	for frame, entries := range projected {
		if frame >= len(candidates) || frame >= len(exposures) {
			continue
		}
		for _, d := range entries {
			if d.inputIndex != frame || d.starIndex < 0 || d.starIndex >= len(candidates[frame]) || !finite(d.x) || !finite(d.y) {
				continue
			}
			valid = append(valid, consensusDetection{frame: frame, star: d.starIndex, x: d.x, y: d.y})
		}
	}

	// Cell size equals the matching radius. Each cell lookup touches at most
	// nine neighboring cells and records distinct exposure votes per candidate.
	cells := make(map[consensusCell][]int, len(valid))
	for i, d := range valid {
		cell := consensusCell{int(math.Floor(d.x / radiusPx)), int(math.Floor(d.y / radiusPx))}
		cells[cell] = append(cells[cell], i)
	}
	support := make([]map[string]struct{}, len(valid))
	for i := range support {
		support[i] = make(map[string]struct{})
		d := valid[i]
		cell := consensusCell{int(math.Floor(d.x / radiusPx)), int(math.Floor(d.y / radiusPx))}
		for dx := -1; dx <= 1; dx++ {
			for dy := -1; dy <= 1; dy++ {
				for _, j := range cells[consensusCell{cell.x + dx, cell.y + dy}] {
					other := valid[j]
					if math.Hypot(d.x-other.x, d.y-other.y) <= radiusPx {
						support[i][exposures[other.frame]] = struct{}{}
					}
				}
			}
		}
	}

	byCandidate := make(map[[2]int]int, len(valid))
	for i, d := range valid {
		key := [2]int{d.frame, d.star}
		if _, exists := byCandidate[key]; !exists || len(support[i]) > len(support[byCandidate[key]]) {
			byCandidate[key] = i
		}
	}
	for frame, catalog := range candidates {
		stats[frame].Candidates = len(catalog)
		supportedCount := 0
		for i := range catalog {
			if j, ok := byCandidate[[2]int{frame, i}]; ok && len(support[j]) >= 2 {
				supportedCount++
			}
		}
		if len(valid) == 0 || supportedCount < 4 {
			stats[frame].Fallback = true
			selected[frame] = cloneCapped(catalog, maxStars)
			stats[frame].Selected = len(selected[frame])
			continue
		}
		type ranked struct{ index, support int }
		rankedCandidates := make([]ranked, 0, len(catalog))
		for i := range catalog {
			if j, ok := byCandidate[[2]int{frame, i}]; ok {
				s := len(support[j])
				if s >= 2 {
					stats[frame].Corroborated++
					if s >= 3 {
						stats[frame].Strong++
					}
					rankedCandidates = append(rankedCandidates, ranked{i, s})
				}
			}
		}
		for i := 1; i < len(rankedCandidates); i++ {
			v := rankedCandidates[i]
			j := i - 1
			for j >= 0 && (rankedCandidates[j].support < v.support || (rankedCandidates[j].support == v.support && rankedCandidates[j].index > v.index)) {
				rankedCandidates[j+1] = rankedCandidates[j]
				j--
			}
			rankedCandidates[j+1] = v
		}
		if len(rankedCandidates) > maxStars {
			rankedCandidates = rankedCandidates[:maxStars]
		}
		selected[frame] = make([]processing.Star, 0, len(rankedCandidates))
		for _, r := range rankedCandidates {
			selected[frame] = append(selected[frame], catalog[r.index])
		}
		stats[frame].Selected = len(selected[frame])
	}
	return selected, stats
}

type consensusCell struct{ x, y int }

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func cloneCapped(stars []processing.Star, max int) []processing.Star {
	if len(stars) > max {
		stars = stars[:max]
	}
	return append([]processing.Star(nil), stars...)
}
