package ui

import (
	"context"
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
	"gofitsv3/internal/mosaic"
	"gofitsv3/internal/stretch"
	"sync"
)

// mosaicWorkspace holds the shared state and widget references for the mosaic
// workspace. It exists so the workspace's behavior can be split across several
// files as methods rather than living in one giant closure-heavy constructor.
//
// Fields are populated during construction in newMosaicWorkspace; the event
// handlers (which historically were closures capturing locals) are methods on
// this type. Methods may run on the Fyne main thread or from goroutines, mirror
// the original closure behavior exactly.
type mosaicWorkspace struct {
	app   fyne.App
	win   fyne.Window
	state *mosaicState

	// --- mutable value state (reassigned during the session) ---
	activeFilter        string
	lastProjectName     string
	currentProjectPath  string
	queueRunning        bool
	alignmentMu         sync.Mutex
	alignmentGeneration uint64
	alignmentCancel     context.CancelFunc
	buildMu             sync.Mutex
	buildGeneration     uint64
	buildCancel         context.CancelFunc
	gmosMu              sync.Mutex
	gmosGeneration      uint64
	gmosCancel          context.CancelFunc
	// inputMu guards replacement/reload of inputs and the generation map. Long
	// running exports take short read snapshots so a project reload cannot race
	// their validation or final publication.
	inputMu sync.RWMutex
	// inputGenerations advances whenever an input is reloaded/replaced. It is
	// keyed by the stable source identity so an editor cannot export a mask for
	// a same-path, same-dimensions replacement.
	inputGenerations    map[string]uint64
	queueWindow         fyne.Window
	zoomLevel           float64
	zoomFitMode         bool
	levelsSet           bool
	stretchMode         stretch.Mode
	mtfMidtone          float64
	mosaicBins          [256]int
	zoomCustomOption    string
	zoomSelectSyncing   bool
	gmosCalibrationItem *fyne.MenuItem
	gmosMenu            *fyne.Menu

	// --- mode pointers (non-nil only while in star/measure mode) ---
	activePicker      *starPickerWidget
	activeMeasure     *measurePickerWidget
	starModeRefResult *mosaic.Result
	inputFramesWindow fyne.Window

	// --- containers swapped during mode changes ---
	leftStack          *fyne.Container
	previewSwap        *fyne.Container
	previewScroll      *container.Scroll
	pickerScroll       *container.Scroll
	controlsScroll     *container.Scroll
	starPanelScroll    *container.Scroll
	measurePanelScroll *container.Scroll

	// --- preview + status widgets ---
	preview          *canvas.Image
	statsLabel       *widget.Label
	statusLabel      *widget.Label
	mosaicHistogram  *canvas.Raster
	offsetControls   *fyne.Container
	offsetScroll     *container.Scroll
	offsetHeader     *fyne.Container
	saveBtn          *widget.Button
	sendToExamineBtn *widget.Button
	saveOffsetsBtn   *widget.Button
	loadOffsetsBtn   *widget.Button
	batchBtn         *widget.Button
	directoryBtn     *widget.Button
	buildBtn         *widget.Button
	clearBtn         *widget.Button
	setRefBtn        *widget.Button
	clearRefBtn      *widget.Button
	refLabel         *widget.Label

	// --- level entry widgets ---
	blackEntry      *NumberEntry
	whiteEntry      *NumberEntry
	bgEntry         *NumberEntry
	peakEntry       *NumberEntry
	scaledPeakEntry *NumberEntry
	mtfMidtoneEntry *NumberEntry
	modeSelect      *SafeSelect

	// --- star mode widgets ---
	starCountLabel *widget.Label

	// --- measure mode widgets ---
	measureStatusLabel   *widget.Label
	measureCentroidCheck *widget.Check

	// --- zoom widgets ---
	zoomSelect      *SafeSelect
	zoomCustomEntry *widget.Entry
	zoomPresets     []string
}

func (ws *mosaicWorkspace) beginMosaicBuild() (context.Context, uint64, bool) {
	ws.buildMu.Lock()
	defer ws.buildMu.Unlock()
	if ws.buildCancel != nil {
		return nil, 0, false
	}
	ws.buildGeneration++
	ctx, cancel := context.WithCancel(context.Background())
	ws.buildCancel = cancel
	return ctx, ws.buildGeneration, true
}

func (ws *mosaicWorkspace) finishMosaicBuild(generation uint64) bool {
	ws.buildMu.Lock()
	defer ws.buildMu.Unlock()
	if generation != ws.buildGeneration {
		return false
	}
	ws.buildCancel = nil
	return true
}

func (ws *mosaicWorkspace) cancelMosaicBuild() {
	ws.buildMu.Lock()
	if ws.buildCancel != nil {
		ws.buildCancel()
		ws.buildCancel = nil
	}
	ws.buildGeneration++
	ws.buildMu.Unlock()
}

func (ws *mosaicWorkspace) beginGMOSCalibration() (context.Context, uint64, bool) {
	ws.gmosMu.Lock()
	defer ws.gmosMu.Unlock()
	if ws.gmosCancel != nil {
		return nil, 0, false
	}
	ws.gmosGeneration++
	ctx, cancel := context.WithCancel(context.Background())
	ws.gmosCancel = cancel
	return ctx, ws.gmosGeneration, true
}

func (ws *mosaicWorkspace) finishGMOSCalibration(generation uint64) bool {
	ws.gmosMu.Lock()
	defer ws.gmosMu.Unlock()
	if generation != ws.gmosGeneration {
		return false
	}
	ws.gmosCancel = nil
	return true
}

func (ws *mosaicWorkspace) cancelGMOSCalibration() {
	ws.gmosMu.Lock()
	if ws.gmosCancel != nil {
		ws.gmosCancel()
		ws.gmosCancel = nil
	}
	ws.gmosGeneration++
	ws.gmosMu.Unlock()
}

func (ws *mosaicWorkspace) beginMosaicAlignment() (context.Context, uint64, bool) {
	ws.alignmentMu.Lock()
	defer ws.alignmentMu.Unlock()
	if ws.alignmentCancel != nil {
		return nil, 0, false
	}
	ws.alignmentGeneration++
	ctx, cancel := context.WithCancel(context.Background())
	ws.alignmentCancel = cancel
	return ctx, ws.alignmentGeneration, true
}

func (ws *mosaicWorkspace) finishMosaicAlignment(generation uint64) bool {
	ws.alignmentMu.Lock()
	defer ws.alignmentMu.Unlock()
	if generation != ws.alignmentGeneration {
		return false
	}
	ws.alignmentCancel = nil
	return true
}

func (ws *mosaicWorkspace) cancelMosaicAlignment() {
	ws.alignmentMu.Lock()
	if ws.alignmentCancel != nil {
		ws.alignmentCancel()
		ws.alignmentCancel = nil
	}
	ws.alignmentGeneration++
	ws.alignmentMu.Unlock()
}
