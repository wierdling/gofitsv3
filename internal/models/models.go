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
}

type ComposeProject struct {
	Channels [3]ChannelState `json:"channels"`
	Flip     bool            `json:"flip"`
}

// DrizzleSettings holds the mosaic.Options fields that the user configures
// via the Settings > Drizzle dialog. All kernel/method values are stored as
// ints so they round-trip through JSON without importing the mosaic package.
type DrizzleSettings struct {
	// FinalScale is the desired output plate scale in arcsec/pixel (AstroDrizzle
	// final_scale semantics).  When > 0, the internal multiplier is computed from
	// the reference image WCS.  Scale is used as a raw multiplier fallback when
	// FinalScale is zero.
	FinalScale      float64 `json:"finalScale"`
	Scale           float64 `json:"scale"`
	PixFrac         float64 `json:"pixFrac"`
	CRMethod        int     `json:"crMethod"`
	SepKernel       int     `json:"sepKernel"`
	FinalKernel     int     `json:"finalKernel"`
	WeightingMode   int     `json:"weightingMode"`
	UseERRWeighting bool    `json:"useERRWeighting,omitempty"`
	// CRSeedSNR and CRDerivScale control the drizzle-style CR detection thresholds.
	// CRSeedSNR is the signal-to-noise ratio threshold for seeding a CR candidate.
	// CRDerivScale scales the derivative (sharpness) term in the rejection test.
	CRSeedSNR    float64 `json:"crSeedSNR"`
	CRDerivScale float64 `json:"crDerivScale"`
}

// AlignmentSettings holds the star-alignment controls configured through the
// standalone Mosaic > Alignment Settings dialog.
type AlignmentSettings struct {
	AlignmentMode      int     `json:"alignmentMode"`
	SearchRadiusArcsec float64 `json:"searchRadiusArcsec"`
	NumRefs            int     `json:"numRefs"`
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
}

type MosaicInputState struct {
	Path         string  `json:"path"`
	SCIExt       int     `json:"sciExt,omitempty"`
	OffsetX      float64 `json:"offsetX"`
	OffsetY      float64 `json:"offsetY"`
	HasTransform bool    `json:"hasTransform"`
	Locked       bool    `json:"locked,omitempty"`
	TransformA   float64 `json:"transformA,omitempty"`
	TransformB   float64 `json:"transformB,omitempty"`
	TransformC   float64 `json:"transformC,omitempty"`
	TransformD   float64 `json:"transformD,omitempty"`
	TransformE   float64 `json:"transformE,omitempty"`
	TransformF   float64 `json:"transformF,omitempty"`
}

type MosaicProject struct {
	Inputs               []MosaicInputState `json:"inputs"`
	ReferencePath        string             `json:"referencePath,omitempty"`
	ReferenceSCIExt      int                `json:"referenceSciExt,omitempty"`
	DrizzleSettings      DrizzleSettings    `json:"drizzleSettings"`
	DrizzleSettingsSet   bool               `json:"drizzleSettingsSet"`
	AlignmentSettings    AlignmentSettings  `json:"alignmentSettings"`
	AlignmentSettingsSet bool               `json:"alignmentSettingsSet"`
	SkysubSettings       SkysubSettings     `json:"skysubSettings"`
	SkysubSettingsSet    bool               `json:"skysubSettingsSet"`
	ActiveFilter         string             `json:"activeFilter,omitempty"`
}

type ChannelControl struct {
	Content         fyne.CanvasObject
	ModeSelect      *widget.Select
	BackgroundEntry NumberField
	PeakEntry       NumberField
	ScaledPeakEntry NumberField
	ShowClip        CheckField
}

type RgbLevels struct {
	Min [3]float64
	Max [3]float64
}
