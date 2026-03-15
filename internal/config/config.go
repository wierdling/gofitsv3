package config

// Settings holds user preferences persisted between sessions.
type Settings struct {
	LastOpenDir   string
	GPUEnabled    bool
	MosaicScale   float64
	ExportQuality int
}
