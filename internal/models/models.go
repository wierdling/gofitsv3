package models

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/stretch"
)

// NumberField is implemented by numeric input widgets (e.g. NumberEntry).
type NumberField interface {
	fyne.CanvasObject
	SetValue(float64)
	Value() float64
}

// CheckField is implemented by any toggle/checkbox widget.
type CheckField interface {
	SetChecked(bool)
}

type LoadedImage struct {
	Path       string
	HDU        fitsio.HDU
	Primary    fitsio.Header
	Mode       stretch.Mode
	Black      float64
	White      float64
	Background float64
	Peak       float64
	ScaledPeak float64
	ShowClip   bool
	// Stretch-specific tuning parameters. Zero values are treated as "unset"
	// and replaced with sensible defaults at render time.
	AsinhScale  float64 // Asinh softening beta
	MTFMidtone  float64 // MTF midtone m in (0,1)
	GHSStretch  float64 // GHS strength D
	GHSLocal    float64 // GHS local intensity b
	GHSSymmetry float64 // GHS symmetry point SP in [0,1]
	// Rotation90 is the number of clockwise quarter turns baked into the image
	// data by Compose. It is persisted so a project can restore the same view.
	Rotation90 int

	// AlignTransform is the backward (output→source) sampling affine produced by
	// Compose "Align to Channel 2". When HasAlignTransform is set it is applied at
	// render time underneath the Manual Offset, preserving the full fitted affine
	// (scale and skew included) instead of reducing it to translation+rotation.
	// Stored as plain coefficients to avoid a models→processing import.
	HasAlignTransform                              bool
	AlignA, AlignB, AlignC, AlignD, AlignE, AlignF float64
}

type ChannelState struct {
	Path       string  `json:"path"`
	Mode       string  `json:"mode"`
	Black      float64 `json:"black"`
	White      float64 `json:"white"`
	Background float64 `json:"background"`
	Peak       float64 `json:"peak"`
	ScaledPeak float64 `json:"scaledPeak"`
	ShowClip   bool    `json:"showClip"`
	OffsetX    float64 `json:"offsetX,omitempty"`
	OffsetY    float64 `json:"offsetY,omitempty"`
	OffsetRot  float64 `json:"offsetRot,omitempty"`
	Rotation90 int     `json:"rotation90,omitempty"`

	// Full star-alignment affine (backward sampling) from "Align to Channel 2",
	// applied underneath the Manual Offset at render time. See LoadedImage.
	HasAlign bool    `json:"hasAlign,omitempty"`
	AlignA   float64 `json:"alignA,omitempty"`
	AlignB   float64 `json:"alignB,omitempty"`
	AlignC   float64 `json:"alignC,omitempty"`
	AlignD   float64 `json:"alignD,omitempty"`
	AlignE   float64 `json:"alignE,omitempty"`
	AlignF   float64 `json:"alignF,omitempty"`

	AsinhScale  float64 `json:"asinhScale,omitempty"`
	MTFMidtone  float64 `json:"mtfMidtone,omitempty"`
	GHSStretch  float64 `json:"ghsStretch,omitempty"`
	GHSLocal    float64 `json:"ghsLocal,omitempty"`
	GHSSymmetry float64 `json:"ghsSymmetry,omitempty"`
}

type ComposeProject struct {
	Channels [3]ChannelState `json:"channels"`
	// OverlayLayers holds the arbitrary set of colored overlay layers. OrangeLayer
	// and YellowLayer are legacy fields kept only so projects saved before the
	// generic layer system still load (migrated into OverlayLayers on read).
	OverlayLayers        []OrangeLayerState      `json:"overlayLayers,omitempty"`
	OrangeLayer          OrangeLayerState        `json:"orangeLayer,omitempty"`
	YellowLayer          OrangeLayerState        `json:"yellowLayer,omitempty"`
	SharedHistogramScale bool                    `json:"sharedHistogramScale,omitempty"`
	DisableComposite     bool                    `json:"disableComposite,omitempty"`
	MeasureComposite     bool                    `json:"measureComposite,omitempty"`
	BlinkFilters         bool                    `json:"blinkFilters,omitempty"`
	BlinkExcludedFilter  int                     `json:"blinkExcludedFilter,omitempty"`
	StarlessSettings     StarlessComposeSettings `json:"starlessSettings"`
}

type OrangeLayerState struct {
	Open    bool         `json:"open,omitempty"`
	Channel ChannelState `json:"channel"`
	ColorR  uint8        `json:"colorR"`
	ColorG  uint8        `json:"colorG"`
	ColorB  uint8        `json:"colorB"`
	Opacity float64      `json:"opacity"`
	// HighlightProtect (0..1) controls the overlay blend: the tinted layer is
	// combined additively as out = base + layer - k*base*layer. k=1 reproduces a
	// screen blend (soft, never clips), k=0 is pure additive (max detail, may
	// clip). Lower values keep more overlay detail in bright regions.
	HighlightProtect float64 `json:"highlightProtect,omitempty"`
}

type StarlessComposeSettings struct {
	Enabled                 bool    `json:"enabled"`
	DetectionMode           string  `json:"detectionMode"`
	DetectionPreprocessMode string  `json:"detectionPreprocessMode"`
	DetectionMergeMode      string  `json:"detectionMergeMode"`
	ThresholdSigma          float64 `json:"thresholdSigma"`
	BackgroundTileSize      int     `json:"backgroundTileSize"`
	UseNoDataFloor          bool    `json:"useNoDataFloor"`
	NoDataFloor             float64 `json:"noDataFloor"`
	SeedMinProminence       float64 `json:"seedMinProminence"`
	MinDetectedChannels     int     `json:"minDetectedChannels"`
	MinSeedFootprintArea    int     `json:"minSeedFootprintArea"`
	MinSharedChannels       int     `json:"minSharedChannels"`
	SuppressionRadius       int     `json:"suppressionRadius"`
	MaskBaseRadius          int     `json:"maskBaseRadius"`
	MaxRadius               int     `json:"maxRadius"`
	FeatherRadius           int     `json:"featherRadius"`
	InpaintRadius           int     `json:"inpaintRadius"`
	StarBrightness          float64 `json:"starBrightness"`
	StarSaturation          float64 `json:"starSaturation"`
	ExportDebugMasks        bool    `json:"exportDebugMasks"`
}

// DrizzleSettings holds the mosaic.Options fields that the user configures
// via the Settings > Drizzle dialog. All kernel/method values are stored as
// ints so they round-trip through JSON without importing the mosaic package.
type DrizzleSettings struct {
	// FinalScale is the desired output plate scale in arcsec/pixel (AstroDrizzle
	// final_scale semantics).  When > 0, the internal multiplier is computed from
	// the reference image WCS.  Scale is used as a raw multiplier fallback when
	// FinalScale is zero.
	FinalScale float64 `json:"finalScale"`
	// LockToReferenceFrame pins the output canvas (dimensions, origin, and plate
	// scale) to the reference baseline frame so separately-drizzled channels come
	// out pixel-identical for compositing. Overrides FinalScale/Scale. Requires a
	// reference baseline to be set.
	LockToReferenceFrame bool    `json:"lockToReferenceFrame,omitempty"`
	Scale                float64 `json:"scale"`
	PixFrac              float64 `json:"pixFrac"`
	CRMethod             int     `json:"crMethod"`
	SepKernel            int     `json:"sepKernel"`
	FinalKernel          int     `json:"finalKernel"`
	WeightingMode        int     `json:"weightingMode"`
	UseERRWeighting      bool    `json:"useERRWeighting,omitempty"`
	// SurfaceBrightnessNorm normalizes mixed-scale chips by their mapped pixel
	// area before drizzle. Useful for WFPC2 PC+WF mosaics.
	SurfaceBrightnessNorm bool `json:"surfaceBrightnessNorm,omitempty"`
	// CRSeedSNR and CRDerivScale control the drizzle-style CR detection thresholds.
	// CRSeedSNR is the signal-to-noise ratio threshold for seeding a CR candidate.
	// CRDerivScale scales the derivative (sharpness) term in the rejection test.
	CRSeedSNR    float64 `json:"crSeedSNR"`
	CRDerivScale float64 `json:"crDerivScale"`
	// DebugOutputDir, when set, is the directory where the drizzle process
	// writes individual output-scale chip images for blinking.
	DebugOutputDir string `json:"debugOutputDir,omitempty"`
}

// AlignmentSettings holds the star-alignment controls configured through the
// standalone Mosaic > Alignment Settings dialog.
type AlignmentSettings struct {
	AlignmentMode      int     `json:"alignmentMode"`
	SearchRadiusArcsec float64 `json:"searchRadiusArcsec"`
	NumRefs            int     `json:"numRefs"`
	DebugAlignment     bool    `json:"debugAlignment"`
}

// SkysubSettings holds the AstroDrizzle-style sky-subtraction controls
// configured through the standalone Mosaic > Skysub Settings dialog.
type SkysubSettings struct {
	Enabled bool `json:"enabled"`
	// SkyMethod stores the skysub algorithm as an int so it round-trips
	// through JSON without importing the mosaic package.
	SkyMethod int `json:"skyMethod"`
	// SkyStat stores the sky statistic selector as an int for JSON stability.
	SkyStat int `json:"skyStat"`
	// SkyWidth is the histogram bin width in sigma used when SkyStat is mode.
	SkyWidth    float64 `json:"skyWidth"`
	SkyLower    float64 `json:"skyLower"`
	SkyUpper    float64 `json:"skyUpper"`
	SkyLowerSet bool    `json:"skyLowerSet"`
	SkyUpperSet bool    `json:"skyUpperSet"`
	SkyClip     int     `json:"skyClip"`
	SkyLSigma   float64 `json:"skyLSigma"`
	SkyUSigma   float64 `json:"skyUSigma"`
	// AmpPedestal enables NIRCam per-amplifier pedestal removal. Independent
	// of Enabled/SkyMethod: it fixes an intra-chip readout artifact, not
	// inter-chip sky level.
	AmpPedestal bool `json:"ampPedestal"`
	// RowDestripe enables NIRCam per-amplifier 1/f row-banding removal,
	// independent of Enabled/SkyMethod for the same reason as AmpPedestal.
	RowDestripe bool `json:"rowDestripe"`
	// RowDestripeMaskPath is an optional binary FITS mask. Non-zero finite
	// pixels are excluded from row statistics.
	RowDestripeMaskPath string `json:"rowDestripeMaskPath,omitempty"`
	// RowDestripeMaskDir contains per-input masks named
	// <input-stem>_rowmask.fits. Non-zero finite pixels are excluded from row
	// statistics for the matching calibrated NIRCam input only.
	RowDestripeMaskDir string `json:"rowDestripeMaskDir,omitempty"`
	// RowDestripeMaskSigma is the automatic positive-residual source-mask
	// threshold. Zero loads the default.
	RowDestripeMaskSigma float64 `json:"rowDestripeMaskSigma,omitempty"`
	// RowDestripeTrendWindow is the row smoothing window. Zero loads the default.
	RowDestripeTrendWindow int `json:"rowDestripeTrendWindow,omitempty"`
	// RowDestripeDirection is reserved for future column support; current value
	// is blank or "rows".
	RowDestripeDirection string `json:"rowDestripeDirection,omitempty"`
	// NIRCamWisp enables local template subtraction for detector-fixed NIRCam
	// wisps before sky matching. It is default-off and never downloads templates.
	NIRCamWisp bool `json:"nircamWisp"`
	// NIRCamWispTemplateDir is a local directory containing files named like
	// nircam_wisp_nrcb4_f200w.fits.
	NIRCamWispTemplateDir string `json:"nircamWispTemplateDir,omitempty"`
	// NIRCamWispAutoScale fits a non-negative template scale when enabled.
	NIRCamWispAutoScale bool `json:"nircamWispAutoScale"`
	// NIRCamWispScale is used only when NIRCamWispAutoScale is false.
	NIRCamWispScale float64 `json:"nircamWispScale,omitempty"`
	// MIRIArtifactMask enables user-provided masks for calibrated MIRI artifacts.
	MIRIArtifactMask bool `json:"miriArtifactMask"`
	// MIRIArtifactMaskPath is an optional binary FITS mask applied to MIRI inputs.
	MIRIArtifactMaskPath string `json:"miriArtifactMaskPath,omitempty"`
	// MIRIArtifactMaskDir contains per-input masks named <input-stem>_miri_mask.fits.
	MIRIArtifactMaskDir string `json:"miriArtifactMaskDir,omitempty"`
}

type ArtifactMaskSourceMode string

const (
	ArtifactMaskSourceInput  ArtifactMaskSourceMode = "input"
	ArtifactMaskSourceMosaic ArtifactMaskSourceMode = "mosaic"
)

type ArtifactMaskPurpose string

const (
	ArtifactMaskPurposeMIRIArtifact ArtifactMaskPurpose = "miriArtifact"
	ArtifactMaskPurposeRowDestripe  ArtifactMaskPurpose = "rowDestripe"
)

type ArtifactMaskOperationMode string

const (
	ArtifactMaskOperationAdd   ArtifactMaskOperationMode = "add"
	ArtifactMaskOperationErase ArtifactMaskOperationMode = "erase"
)

type ArtifactMaskRegionKind string

const (
	ArtifactMaskRegionRaster     ArtifactMaskRegionKind = "raster"
	ArtifactMaskRegionBrush      ArtifactMaskRegionKind = "brush"
	ArtifactMaskRegionRectangle  ArtifactMaskRegionKind = "rectangle"
	ArtifactMaskRegionPolygon    ArtifactMaskRegionKind = "polygon"
	ArtifactMaskRegionMorphology ArtifactMaskRegionKind = "morphology"
)

type ArtifactMaskPoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type ArtifactMaskTarget struct {
	Key      string `json:"key"`
	Path     string `json:"path"`
	SCIExt   int    `json:"sciExt,omitempty"`
	Selected bool   `json:"selected"`
}

type ArtifactMaskOperation struct {
	Mode   ArtifactMaskOperationMode `json:"mode"`
	Kind   ArtifactMaskRegionKind    `json:"kind"`
	X      int                       `json:"x,omitempty"`
	Y      int                       `json:"y,omitempty"`
	Width  int                       `json:"width,omitempty"`
	Height int                       `json:"height,omitempty"`
	Radius int                       `json:"radius,omitempty"`
	Points []ArtifactMaskPoint       `json:"points,omitempty"`
	Mask   []byte                    `json:"mask,omitempty"`
}

type ArtifactMaskDocument struct {
	ID         string                  `json:"id,omitempty"`
	Name       string                  `json:"name,omitempty"`
	Purpose    ArtifactMaskPurpose     `json:"purpose,omitempty"`
	SourceMode ArtifactMaskSourceMode  `json:"sourceMode"`
	SourceKey  string                  `json:"sourceKey,omitempty"`
	Width      int                     `json:"width"`
	Height     int                     `json:"height"`
	Targets    []ArtifactMaskTarget    `json:"targets,omitempty"`
	Operations []ArtifactMaskOperation `json:"operations,omitempty"`
	Stale      bool                    `json:"stale,omitempty"`
}

type ArtifactMaskProject struct {
	Version   int                    `json:"version,omitempty"`
	Documents []ArtifactMaskDocument `json:"documents,omitempty"`
}

type MosaicInputState struct {
	Path   string `json:"path"`
	SCIExt int    `json:"sciExt,omitempty"`
	// Combined marks an entry whose Path is the original multi-chip source file
	// that gets drizzled into a single working image on load. Absent (false) for
	// ordinary single-chip inputs and for pre-combine legacy projects.
	Combined          bool    `json:"combined,omitempty"`
	OffsetX           float64 `json:"offsetX"`
	OffsetY           float64 `json:"offsetY"`
	HasTransform      bool    `json:"hasTransform"`
	Locked            bool    `json:"locked,omitempty"`
	Excluded          bool    `json:"excluded,omitempty"`
	NormalizeExposure bool    `json:"normalizeExposure,omitempty"`
	ExposureScale     float64 `json:"exposureScale,omitempty"`
	TransformA        float64 `json:"transformA,omitempty"`
	TransformB        float64 `json:"transformB,omitempty"`
	TransformC        float64 `json:"transformC,omitempty"`
	TransformD        float64 `json:"transformD,omitempty"`
	TransformE        float64 `json:"transformE,omitempty"`
	TransformF        float64 `json:"transformF,omitempty"`
}

type MosaicProject struct {
	Inputs               []MosaicInputState   `json:"inputs"`
	ReferencePath        string               `json:"referencePath,omitempty"`
	ReferenceSCIExt      int                  `json:"referenceSciExt,omitempty"`
	DrizzleSettings      DrizzleSettings      `json:"drizzleSettings"`
	DrizzleSettingsSet   bool                 `json:"drizzleSettingsSet"`
	AlignmentSettings    AlignmentSettings    `json:"alignmentSettings"`
	AlignmentSettingsSet bool                 `json:"alignmentSettingsSet"`
	SkysubSettings       SkysubSettings       `json:"skysubSettings"`
	SkysubSettingsSet    bool                 `json:"skysubSettingsSet"`
	ActiveFilter         string               `json:"activeFilter,omitempty"`
	ArtifactMasks        *ArtifactMaskProject `json:"artifactMasks,omitempty"`
	ExposureNormMode     int                  `json:"exposureNormMode,omitempty"`
}

type ChannelControl struct {
	Content           fyne.CanvasObject
	ModeSelect        *widget.Select
	BackgroundEntry   NumberField
	PeakEntry         NumberField
	ScaledPeakEntry   NumberField
	AsinhScaleEntry   NumberField
	MTFMidtoneEntry   NumberField
	GHSStretchEntry   NumberField
	GHSLocalEntry     NumberField
	GHSSymmetryEntry  NumberField
	MagicPresetSelect *widget.Select
	XOffsetEntry      NumberField
	YOffsetEntry      NumberField
	RotOffsetEntry    NumberField
	ShowClip          CheckField
}

type RgbLevels struct {
	Min [3]float64
	Max [3]float64
}
