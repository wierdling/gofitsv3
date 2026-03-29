package models

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/stretch"
)

type LoadedImage struct {
	Path           string
	HDU            fitsio.HDU
	Primary        fitsio.Header
	Mode           stretch.Mode
	Black          float64
	White          float64
	Background     float64
	Peak           float64
	ScaledPeak     float64
	ShowClip       bool
	OriginalPixels []float64
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

type ChannelControl struct {
	Content         fyne.CanvasObject
	ModeSelect      *widget.Select
	BackgroundEntry *widget.Entry
	PeakEntry       *widget.Entry
	ScaledPeakEntry *widget.Entry
	ShowClip        *widget.Check
}

type RgbLevels struct {
	Min [3]float64
	Max [3]float64
}
