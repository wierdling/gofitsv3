package ui

import (
	"fmt"

	"fyne.io/fyne/v2"

	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
)

// maxOverlayLayers bounds how many colored overlay layers can exist at once.
// imgs/origPixels are pre-allocated with room for the 3 RGB base channels plus
// this many overlay slots so that appending an overlay never reallocates the
// backing array — the RGB channelControls capture the imgs slice header by
// value and must keep pointing at the same array.
const maxOverlayLayers = 16

// overlayLayer is one user-added colored layer (formerly the fixed Orange/Yellow
// windows): a grayscale image assigned a tint, screen/additively blended onto the
// base RGB composite. idx is its stable index into imgs/origPixels.
type overlayLayer struct {
	idx      int
	name     string
	settings models.OrangeLayerState
	win      fyne.Window
	viewport *viewport
	control  *models.ChannelControl
}

// overlayLayerPalette seeds the tint of a newly added colored layer. Values are
// starting points the user can freely recolor; the cycle just avoids every new
// layer defaulting to the same hue. First two entries preserve the old
// Orange/Yellow defaults.
var overlayLayerPalette = [][3]uint8{
	{159, 140, 80},  // orange
	{255, 220, 90},  // yellow
	{237, 80, 80},   // red
	{100, 149, 237}, // blue
	{80, 200, 120},  // green
	{200, 110, 200}, // magenta
	{110, 200, 200}, // cyan
	{240, 160, 60},  // amber
}

func defaultOverlayLayerSettings(n int) models.OrangeLayerState {
	c := overlayLayerPalette[n%len(overlayLayerPalette)]
	return models.OrangeLayerState{
		ColorR:           c[0],
		ColorG:           c[1],
		ColorB:           c[2],
		Opacity:          1,
		HighlightProtect: 0.5,
	}
}

func normalizeComposeOverlayState(s models.OrangeLayerState, index int) models.OrangeLayerState {
	if s.ColorR == 0 && s.ColorG == 0 && s.ColorB == 0 && s.Opacity == 0 {
		s = defaultOverlayLayerSettings(index)
	}
	if s.BlinkID == "" {
		s.BlinkID = fmt.Sprintf("overlay-legacy-%d", index)
	}
	return s
}

func transformedComposeOverlaySource(sources []*models.LoadedImage, layer *overlayLayer) (processing.OverlayLayer, bool) {
	if layer == nil || layer.idx < 0 || layer.idx >= len(sources) || sources[layer.idx] == nil {
		return processing.OverlayLayer{}, false
	}
	return processing.OverlayLayer{Image: sources[layer.idx], Settings: layer.settings}, true
}

func artisticComposeOverlaySource(sources []*models.LoadedImage, layer *overlayLayer) (processing.OverlayLayer, bool) {
	if layer == nil || layer.idx < 0 || layer.idx >= len(sources) || sources[layer.idx] == nil {
		return processing.OverlayLayer{}, false
	}
	return processing.OverlayLayer{Image: sources[layer.idx], Settings: layer.settings}, true
}

// composeOverlaySources keeps Artistic overlays on the rendered (aligned and
// manually offset) source grid, while weighted overlays retain their optional
// PSF-matched source grid.
func composeOverlaySources(renderedSources, weightedSources []*models.LoadedImage, layers []*overlayLayer, active func(*overlayLayer) bool) (weighted, artistic []processing.OverlayLayer) {
	for _, layer := range layers {
		if layer == nil || (active != nil && !active(layer)) {
			continue
		}
		if overlay, ok := transformedComposeOverlaySource(weightedSources, layer); ok {
			weighted = append(weighted, overlay)
		}
		if overlay, ok := artisticComposeOverlaySource(renderedSources, layer); ok {
			artistic = append(artistic, overlay)
		}
	}
	return weighted, artistic
}
