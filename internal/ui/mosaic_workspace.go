package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
	"gofitsv3/internal/mosaic"
	"gofitsv3/internal/stretch"
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
	activeFilter       string
	lastProjectName    string
	currentProjectPath string
	zoomLevel          float64
	zoomFitMode        bool
	levelsSet          bool
	stretchMode        stretch.Mode
	mtfMidtone         float64
	mosaicBins         [256]int
	zoomCustomOption   string
	zoomSelectSyncing  bool

	// --- mode pointers (non-nil only while in star/measure mode) ---
	activePicker      *starPickerWidget
	activeMeasure     *measurePickerWidget
	starModeRefResult *mosaic.Result

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
