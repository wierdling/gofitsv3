package ui

import (
	"context"
	"image"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/histogram"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
)

// composeWorkspace holds the Compose tab state. newComposeWorkspace builds it;
// behaviour lives in methods across internal/ui/compose_*.go (see the file map
// in CLAUDE.md and docs/compose-workspace-refactor-plan.md).
type composeWorkspace struct {
	app fyne.App
	win fyne.Window

	// --- channel images and views ---
	imgs        []*models.LoadedImage
	origPixels  [][]float32
	viewports   []*viewport
	headerWins  []fyne.Window
	controlSets []*models.ChannelControl
	dedicatedL  *models.LoadedImage
	levels      *models.RgbLevels
	levelsWin   *rgbLevelsWindow

	// --- composition settings ---
	psfSettings         models.PSFSettings
	lrgbSettings        models.LRGBSettings
	compositionMode     models.ComposeMode
	mixWeights          []models.ComposeMixWeight
	starWhitening       models.StarWhiteningState
	starGeometryBlinkID string
	starTreatments      *composeStarTreatments
	lrgbMu              sync.RWMutex
	lrgbGeneration      uint64

	// --- rendering ---
	suspendRefresh bool
	genCancel      context.CancelFunc
	previewMu      sync.Mutex
	previewSeq     int
	renderMu       sync.Mutex
	renderCache    []composeRenderCache
	latestRGBStats [3]histogram.Stats

	// --- overlay layers ---
	overlayLayers   []*overlayLayer
	nextLayerNumber int

	// --- large-file mode ---
	largeStore             *composeLargeStore
	largeMode              bool
	largePreviews          map[int]*image.RGBA
	largeArtifacts         map[int]composeArtifactDescriptor
	largeMu                sync.RWMutex
	largeRuntime           *largeChannelRuntime
	largeLoadGenerations   map[string]uint64
	largeSessionGeneration uint64
	editSnapshotMu         sync.Mutex
	editSnapshotCancel     context.CancelFunc

	// --- blink ---
	blinkExcludedIdx int
	blinkChannels    []int
	blinkMu          sync.Mutex
	blinkPrepared    []composeBlinkFrame
	blinkSeq         int
	blinkFrame       int
	blinkStatus      *widget.Label

	// --- picker and measurement ---
	activePicker   composePicker
	measureEnabled bool
	measureStart   *imagePoint
	measureEnd     *imagePoint
	measureLabel   *widget.Label

	// --- star treatment diagnostics overlay ---
	starDiagEnabled    bool
	starDiagPoints     []processing.StarTreatmentDiagnostic    // composite-grid, recomputed on every applied render
	starDiagChannelPts [3][]processing.StarTreatmentDiagnostic // each channel's own grid, its own star map only
	starDiagLabel      *widget.Label

	// --- option widgets ---
	sharedHistCheck     *Toggle
	buildCompositeCheck *Toggle
	blinkCheck          *Toggle
	measureCheck        *Toggle
	starDiagCheck       *Toggle
	largeFilesCheck     *Toggle
	histScaleStatus     *widget.Label
	composeMagicPreset  *widget.Select
	magicAll            *widget.Button

	// --- menu items toggled by updateMenus ---
	saveProjectItem    *fyne.MenuItem
	exportRGBItem      *fyne.MenuItem
	viewHeaderItems    []*fyne.MenuItem
	saveHeaderItems    []*fyne.MenuItem
	copySettingsItem   *fyne.MenuItem
	matchStretchItem   *fyne.MenuItem
	normalizeScaleItem *fyne.MenuItem
	sendToEditItem     *fyne.MenuItem
	alignChannelsItem  *fyne.MenuItem
	cleanChannelsItem  *fyne.MenuItem
	resetDataItem      *fyne.MenuItem
}
