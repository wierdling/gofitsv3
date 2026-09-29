package ui

import (
	"context"
	"fmt"
	"sort"

	"gofitsv3/internal/models"
)

var composeBlinkFilterNames = []string{"Blue", "Green", "Red"}

type composeBlinkFrame struct {
	ProjectIndex int
	Name         string
	Preview      composeViewportPreview
}

// buildComposeBlinkFrames prepares all selected Blink sources from the same
// immutable render snapshot used by the compose preview generation. RGB frames
// reuse the already-stretched previews; overlay frames are stretched here in
// the worker goroutine and never from the ticker/UI callback.
func buildComposeBlinkFrames(ctx context.Context, imgs []*models.LoadedImage, sources []composeBlinkSource, selection []int, base [4]composeViewportPreview) []composeBlinkFrame {
	frames := make([]composeBlinkFrame, 0, len(selection))
	for _, projectIndex := range selection {
		if err := composeMagicCanceled(ctx); err != nil {
			return nil
		}
		var source *composeBlinkSource
		for i := range sources {
			if sources[i].ProjectIndex == projectIndex {
				source = &sources[i]
				break
			}
		}
		if source == nil || source.RuntimeIndex < 0 || source.RuntimeIndex >= len(imgs) || imgs[source.RuntimeIndex] == nil {
			continue
		}
		preview := composeViewportPreview{}
		if source.RuntimeIndex < 3 {
			preview = base[source.RuntimeIndex]
		} else {
			data, err := buildComposeOverlayPreviewData(ctx, imgs[source.RuntimeIndex])
			if err != nil {
				return nil
			}
			preview = composeViewportPreview{Image: data.image, Bins: data.bins, StatsText: fmt.Sprintf("Sky %.3f  μ %.3f  σ %.3f", data.sky, data.mean, data.std), FilterText: data.filterText, OrigW: data.width, OrigH: data.height, Black: imgs[source.RuntimeIndex].Black, White: imgs[source.RuntimeIndex].White}
		}
		frames = append(frames, composeBlinkFrame{ProjectIndex: projectIndex, Name: source.Name, Preview: preview})
	}
	return frames
}

func composeBlinkFilterIndex(name string) int {
	for i, filterName := range composeBlinkFilterNames {
		if name == filterName {
			return i
		}
	}
	return 0
}

func clampComposeBlinkFilter(idx int) int {
	if idx < 0 || idx >= len(composeBlinkFilterNames) {
		return 0
	}
	return idx
}

func composeBlinkPair(excluded int) (int, int) {
	excluded = clampComposeBlinkFilter(excluded)
	pair := [2]int{-1, -1}
	next := 0
	for i := 0; i < 3; i++ {
		if i == excluded {
			continue
		}
		pair[next] = i
		next++
	}
	return pair[0], pair[1]
}

// composeBlinkOverlaySource describes an overlay's sparse runtime slot. The
// project index is assigned later from the stable source enumeration, so a
// removed overlay cannot shift the meaning of a saved selection unexpectedly.
type composeBlinkOverlaySource struct {
	RuntimeIndex int
	Name         string
	Path         string
	BlinkID      string
}

type composeBlinkSource struct {
	Name         string
	Key          string
	RuntimeIndex int
	ProjectIndex int
}

// enumerateComposeBlinkSources returns loaded RGB channels followed by loaded
// overlays in runtime-slot order. ProjectIndex is compact and deterministic;
// RuntimeIndex retains the sparse slot used by the live compose workspace.
func enumerateComposeBlinkSources(imgs []*models.LoadedImage, overlays []composeBlinkOverlaySource) []composeBlinkSource {
	sources := make([]composeBlinkSource, 0, len(imgs))
	for i := 0; i < len(composeBlinkFilterNames) && i < len(imgs); i++ {
		if imgs[i] == nil {
			continue
		}
		sources = append(sources, composeBlinkSource{
			Name:         composeBlinkFilterNames[i],
			Key:          fmt.Sprintf("rgb:%d", i),
			RuntimeIndex: i,
			ProjectIndex: len(sources),
		})
	}
	ordered := append([]composeBlinkOverlaySource(nil), overlays...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].RuntimeIndex < ordered[j].RuntimeIndex })
	for _, overlay := range ordered {
		if overlay.RuntimeIndex < len(composeBlinkFilterNames) || overlay.RuntimeIndex < 0 || overlay.RuntimeIndex >= len(imgs) || imgs[overlay.RuntimeIndex] == nil {
			continue
		}
		name := overlay.Name
		if name == "" {
			name = fmt.Sprintf("Overlay %d", overlay.RuntimeIndex-len(composeBlinkFilterNames)+1)
		}
		key := fmt.Sprintf("overlay-slot:%d", overlay.RuntimeIndex)
		if overlay.BlinkID != "" {
			key = "overlay-id:" + overlay.BlinkID
		}
		sources = append(sources, composeBlinkSource{
			Name:         name,
			Key:          key,
			RuntimeIndex: overlay.RuntimeIndex,
			ProjectIndex: len(sources),
		})
	}
	return sources
}

func resolveComposeBlinkKeys(keys []string, sources []composeBlinkSource) []int {
	byKey := make(map[string]int, len(sources))
	for _, source := range sources {
		if source.Key != "" {
			byKey[source.Key] = source.ProjectIndex
		}
	}
	result := make([]int, 0, len(keys))
	seen := make(map[int]bool)
	for _, key := range keys {
		if index, ok := byKey[key]; ok && !seen[index] {
			result = append(result, index)
			seen[index] = true
		}
	}
	return result
}

// filterComposeBlinkSelection drops stale project indices and duplicate
// entries while preserving the user's ordering.
func filterComposeBlinkSelection(selection []int, sources []composeBlinkSource) []int {
	valid := make(map[int]bool, len(sources))
	for _, source := range sources {
		valid[source.ProjectIndex] = true
	}
	result := make([]int, 0, len(selection))
	seen := make(map[int]bool, len(selection))
	for _, index := range selection {
		if valid[index] && !seen[index] {
			result = append(result, index)
			seen[index] = true
		}
	}
	return result
}

// resolveComposeBlinkSelection applies a saved custom selection, or migrates
// the legacy two-of-three RGB setting when no generalized selection exists.
// New projects default to all currently loaded sources.
func resolveComposeBlinkSelection(sources []composeBlinkSource, custom []int, legacyEnabled bool, legacyExcluded int) []int {
	if custom != nil {
		return filterComposeBlinkSelection(custom, sources)
	}
	if legacyEnabled {
		result := make([]int, 0, len(composeBlinkFilterNames)-1)
		excluded := clampComposeBlinkFilter(legacyExcluded)
		for _, source := range sources {
			if source.RuntimeIndex < len(composeBlinkFilterNames) && source.RuntimeIndex != excluded {
				result = append(result, source.ProjectIndex)
			}
		}
		return result
	}
	result := make([]int, len(sources))
	for i := range sources {
		result[i] = sources[i].ProjectIndex
	}
	return result
}

func composeBlinkRuntimeIndices(selection []int, sources []composeBlinkSource) []int {
	selected := filterComposeBlinkSelection(selection, sources)
	byProject := make(map[int]int, len(sources))
	for _, source := range sources {
		byProject[source.ProjectIndex] = source.RuntimeIndex
	}
	result := make([]int, 0, len(selected))
	for _, index := range selected {
		result = append(result, byProject[index])
	}
	return result
}

// remapComposeBlinkSelection carries a selection across runtime changes (for
// example, removing a sparse overlay slot) by runtime identity. Slots absent
// from the new source list are dropped, so a later slot reuse is not selected.
func remapComposeBlinkSelection(selection []int, oldSources, newSources []composeBlinkSource) []int {
	oldRuntime := composeBlinkRuntimeIndices(selection, oldSources)
	byRuntime := make(map[int]int, len(newSources))
	for _, source := range newSources {
		byRuntime[source.RuntimeIndex] = source.ProjectIndex
	}
	remapped := make([]int, 0, len(oldRuntime))
	for _, runtimeIndex := range oldRuntime {
		if projectIndex, ok := byRuntime[runtimeIndex]; ok {
			remapped = append(remapped, projectIndex)
		}
	}
	return filterComposeBlinkSelection(remapped, newSources)
}

// cycleComposeBlinkSelection returns the next selected project index after
// current. It wraps and starts at the first selection when current is stale.
func cycleComposeBlinkSelection(selection []int, current int) (int, bool) {
	if len(selection) == 0 {
		return 0, false
	}
	for i, index := range selection {
		if index == current {
			return selection[(i+1)%len(selection)], true
		}
	}
	return selection[0], true
}
