package astroio

import "context"

// PlaneID is a stable identifier for one image plane in a source file.
type PlaneID string

// PixelToICRS evaluates an input's native detector transform in ICRS
// longitude/latitude degrees. It is intentionally small so Mosaic can use
// native GWCS without depending on ASDF internals.
type PixelToICRS interface {
	PixelToICRS(x, y float64) (ra, dec float64, err error)
}

// Plane describes an image plane without exposing a format-specific file
// representation.
type Plane struct {
	ID      PlaneID
	Name    string
	Kind    string
	Width   int
	Height  int
	Data    []float32
	ExactDQ []uint32
}

// PlaneInfo is the lightweight description returned during inspection.
type PlaneInfo struct {
	ID     PlaneID
	Name   string
	Kind   string
	Width  int
	Height int
}

// Metadata contains source-level metadata and the available image planes.
// Cards are intentionally plain strings so callers do not depend on FITS.
type Metadata struct {
	Format     string
	Path       string
	Cards      map[string]string
	Planes     []PlaneInfo
	NativeGWCS PixelToICRS
	// NativeGWCSProfile identifies the registered native model used by the
	// evaluator (for example NIRCam/module-B). Empty means no native model.
	NativeGWCSProfile string
}

// ReadOptions controls decoding of a selected plane.
type ReadOptions struct {
	// ExactIntegerDQ requests integer DQ samples in Plane.ExactDQ. It is
	// rejected for non-DQ planes and avoids losing bit-mask values to float32.
	ExactIntegerDQ bool
}

// Row is a bounded view of one decoded row. The slices are valid until the
// writer returns and must be consumed or copied by the writer.
type Row struct {
	Y       int
	Float32 []float32
	ExactDQ []uint32
}

// RowWriter is the consumer-owned destination for streamed rows.
type RowWriter interface {
	WriteRow(context.Context, Row) error
}

// Source is the format-neutral API used by Examine and later Mosaic stages.
type Source interface {
	Metadata(context.Context) (Metadata, error)
	ReadPlane(context.Context, PlaneID, ReadOptions) (Plane, error)
	StreamPlane(context.Context, PlaneID, ReadOptions, RowWriter) error
}

// Decoder opens one file format.
type Decoder interface {
	Name() string
	Probe(string) (bool, error)
	Open(context.Context, string) (Source, error)
}
