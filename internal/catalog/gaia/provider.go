// Package gaia defines the dependency-free contract used by Gaia calibration.
// Implementations may use a local cache, a remote service, or an in-memory
// fixture; processing never needs to know which one it received.
package gaia

import (
	"context"
	"fmt"
	"math"
	"sort"
)

type AccessMode string

const (
	AccessOnline    AccessMode = "online"
	AccessCacheOnly AccessMode = "cacheOnly"
)

type Provenance struct {
	Release           string
	XPRepresentation  string
	Provider          string
	ProviderVersion   string
	EndpointSemantics string
}

func (p Provenance) Validate() error {
	if p.Release == "" || p.XPRepresentation == "" || p.Provider == "" {
		return fmt.Errorf("gaia provenance requires release, XP representation, and provider")
	}
	return nil
}

type Coordinate struct {
	RA  float64
	Dec float64
}

type Footprint struct {
	// Exactly one shape is required. Polygon vertices are in degrees and are
	// interpreted in order; a cone uses RadiusDeg around Center.
	Center    Coordinate
	RadiusDeg float64
	Polygon   []Coordinate
}

func (f Footprint) Validate() error {
	if err := validateCoordinate(f.Center); err != nil {
		return fmt.Errorf("footprint center: %w", err)
	}
	cone := f.RadiusDeg > 0 && finite(f.RadiusDeg)
	polygon := len(f.Polygon) >= 3
	if cone == polygon {
		return fmt.Errorf("footprint must contain exactly one valid cone or polygon")
	}
	if polygon {
		for i, p := range f.Polygon {
			if err := validateCoordinate(p); err != nil {
				return fmt.Errorf("footprint polygon vertex %d: %w", i, err)
			}
		}
	}
	return nil
}

type FieldQuery struct {
	Footprint        Footprint
	ObservationEpoch float64
	Release          string
	MagnitudeLimit   float64
	QualitySelector  string
}

func (q FieldQuery) Validate() error {
	if err := q.Footprint.Validate(); err != nil {
		return err
	}
	if !finite(q.ObservationEpoch) || q.ObservationEpoch < 1900 || q.ObservationEpoch > 2200 {
		return fmt.Errorf("invalid observation epoch")
	}
	if q.Release == "" {
		return fmt.Errorf("gaia release is required")
	}
	if !finite(q.MagnitudeLimit) || q.MagnitudeLimit <= 0 {
		return fmt.Errorf("invalid magnitude limit")
	}
	return nil
}

type Source struct {
	Release              string
	SourceID             uint64
	RA                   float64
	Dec                  float64
	ReferenceEpoch       float64
	ProperMotionRA       float64
	ProperMotionDec      float64
	PositionError        float64
	ProperMotionErrorRA  float64
	ProperMotionErrorDec float64
	G                    float64
	BP                   float64
	RP                   float64
	GError               float64
	BPError              float64
	RPError              float64
	QualityFlags         []string
	VariabilityFlags     []string
	ContaminationFlags   []string
}

func (s Source) Validate(expectedRelease string) error {
	if s.Release == "" || (expectedRelease != "" && s.Release != expectedRelease) {
		return fmt.Errorf("source %d has mismatched Gaia release", s.SourceID)
	}
	if s.SourceID == 0 {
		return fmt.Errorf("source ID is required")
	}
	if err := validateCoordinate(Coordinate{RA: s.RA, Dec: s.Dec}); err != nil {
		return err
	}
	if !finite(s.ReferenceEpoch) || s.ReferenceEpoch < 1900 || s.ReferenceEpoch > 2200 {
		return fmt.Errorf("source %d has invalid reference epoch", s.SourceID)
	}
	for name, v := range map[string]float64{"properMotionRA": s.ProperMotionRA, "properMotionDec": s.ProperMotionDec, "positionError": s.PositionError, "properMotionErrorRA": s.ProperMotionErrorRA, "properMotionErrorDec": s.ProperMotionErrorDec} {
		if !finite(v) || (name != "properMotionRA" && name != "properMotionDec" && v <= 0) {
			return fmt.Errorf("source %d has invalid %s", s.SourceID, name)
		}
	}
	for name, v := range map[string]float64{"G": s.G, "BP": s.BP, "RP": s.RP, "GError": s.GError, "BPError": s.BPError, "RPError": s.RPError} {
		if !finite(v) || (name != "G" && name != "BP" && name != "RP" && v <= 0) {
			return fmt.Errorf("source %d has invalid %s photometry", s.SourceID, name)
		}
	}
	return nil
}

type XPSpectrum struct {
	Release               string
	SourceID              uint64
	RepresentationVersion string
	Wavelengths           []float64
	Flux                  []float64
	FluxErrors            []float64
	CalibrationVersion    string
}

func (s XPSpectrum) Validate(expectedRelease, expectedRepresentation string) error {
	if s.SourceID == 0 || s.Release == "" || (expectedRelease != "" && s.Release != expectedRelease) {
		return fmt.Errorf("XP spectrum has invalid source or release")
	}
	if s.RepresentationVersion == "" || (expectedRepresentation != "" && s.RepresentationVersion != expectedRepresentation) {
		return fmt.Errorf("XP spectrum %d has mismatched representation", s.SourceID)
	}
	if len(s.Wavelengths) < 2 || len(s.Wavelengths) != len(s.Flux) || len(s.FluxErrors) != len(s.Flux) {
		return fmt.Errorf("XP spectrum %d is incomplete", s.SourceID)
	}
	for i := range s.Wavelengths {
		if !finite(s.Wavelengths[i]) || !finite(s.Flux[i]) || !finite(s.FluxErrors[i]) || s.FluxErrors[i] < 0 || (i > 0 && s.Wavelengths[i] <= s.Wavelengths[i-1]) {
			return fmt.Errorf("XP spectrum %d contains invalid samples", s.SourceID)
		}
	}
	return nil
}

// Provider is deliberately free of HTTP, database/sql, and filesystem types.
// Implementations must honor context cancellation and may return records in
// any order. Callers sort by SourceID before fitting.
type Provider interface {
	Provenance(ctx context.Context) (Provenance, error)
	Mode() AccessMode
	DiscoverSources(ctx context.Context, query FieldQuery) ([]Source, error)
	RetrieveXPSpectra(ctx context.Context, release, representation string, sourceIDs []uint64) ([]XPSpectrum, error)
}

func NormalizeSources(sources []Source, release string) ([]Source, error) {
	out := append([]Source(nil), sources...)
	for _, s := range out {
		if err := s.Validate(release); err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SourceID < out[j].SourceID })
	for i := 1; i < len(out); i++ {
		if out[i-1].SourceID == out[i].SourceID {
			return nil, fmt.Errorf("duplicate Gaia source ID %d", out[i].SourceID)
		}
	}
	return out, nil
}

func NormalizeXPSpectra(spectra []XPSpectrum, release, representation string) ([]XPSpectrum, error) {
	out := append([]XPSpectrum(nil), spectra...)
	for _, s := range out {
		if err := s.Validate(release, representation); err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SourceID < out[j].SourceID })
	for i := 1; i < len(out); i++ {
		if out[i-1].SourceID == out[i].SourceID {
			return nil, fmt.Errorf("duplicate XP spectrum source ID %d", out[i].SourceID)
		}
	}
	return out, nil
}

func validateCoordinate(c Coordinate) error {
	if !finite(c.RA) || !finite(c.Dec) || c.RA < 0 || c.RA >= 360 || c.Dec < -90 || c.Dec > 90 {
		return fmt.Errorf("invalid sky coordinate")
	}
	return nil
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
