package processing

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"

	"gofitsv3/internal/catalog/gaia"
	"gofitsv3/internal/debuglog"
	"gofitsv3/internal/models"
)

// GaiaPassband is the immutable throughput contract consumed by the fitter.
// Throughput assets and synthetic photometry are added in Step 12; this step
// only establishes names, versions, and supported wavelength coverage.
type GaiaPassband struct {
	Name            string
	Version         string
	MinWavelengthNm float64
	MaxWavelengthNm float64
	Throughput      []float64
	// ThroughputWavelengths optionally gives the wavelength (nm) for each
	// throughput sample. When omitted, throughput is interpreted as flat.
	ThroughputWavelengths []float64
}

var gaiaPassbands = map[string]GaiaPassband{
	"G":     {Name: "G", Version: "gaia-passband-v2", MinWavelengthNm: 330, MaxWavelengthNm: 1050, Throughput: []float64{0, 1, 1, 0}, ThroughputWavelengths: []float64{330, 450, 850, 1050}},
	"BP":    {Name: "BP", Version: "gaia-passband-v2", MinWavelengthNm: 330, MaxWavelengthNm: 680, Throughput: []float64{0, 1, 0}, ThroughputWavelengths: []float64{330, 500, 680}},
	"RP":    {Name: "RP", Version: "gaia-passband-v2", MinWavelengthNm: 630, MaxWavelengthNm: 1050, Throughput: []float64{0, 1, 0}, ThroughputWavelengths: []float64{630, 850, 1050}},
	"F435W": {Name: "F435W", Version: "hst-passband-v2", MinWavelengthNm: 380, MaxWavelengthNm: 500, Throughput: []float64{0, 1, 0}, ThroughputWavelengths: []float64{380, 435, 500}},
	"F606W": {Name: "F606W", Version: "hst-passband-v2", MinWavelengthNm: 480, MaxWavelengthNm: 720, Throughput: []float64{0, 1, 0}, ThroughputWavelengths: []float64{480, 600, 720}},
	"F814W": {Name: "F814W", Version: "hst-passband-v2", MinWavelengthNm: 690, MaxWavelengthNm: 960, Throughput: []float64{0, 1, 0}, ThroughputWavelengths: []float64{690, 810, 960}},
}

// IntegrateXPSpectrum computes deterministic trapezoidal synthetic photometry.
func IntegrateXPSpectrum(s gaia.XPSpectrum, p GaiaPassband) (float64, error) {
	if len(s.Wavelengths) < 2 || len(s.Wavelengths) != len(s.Flux) {
		return 0, fmt.Errorf("invalid XP samples")
	}
	var num, den float64
	for i := 1; i < len(s.Wavelengths); i++ {
		a, b := s.Wavelengths[i-1], s.Wavelengths[i]
		lo, hi := a, b
		if hi <= p.MinWavelengthNm || lo >= p.MaxWavelengthNm {
			continue
		}
		if lo < p.MinWavelengthNm {
			lo = p.MinWavelengthNm
		}
		if hi > p.MaxWavelengthNm {
			hi = p.MaxWavelengthNm
		}
		if hi <= lo {
			continue
		}
		f0, f1 := s.Flux[i-1], s.Flux[i]
		t := (lo - a) / (b - a)
		u := (hi - a) / (b - a)
		v0, v1 := f0+(f1-f0)*t, f0+(f1-f0)*u
		q0 := passbandThroughput(p, lo)
		q1 := passbandThroughput(p, hi)
		num += (hi - lo) * (v0*q0 + v1*q1) / 2
		den += hi - lo
	}
	if den == 0 {
		return 0, fmt.Errorf("XP spectrum outside passband")
	}
	return num / den, nil
}

func passbandThroughput(p GaiaPassband, wavelength float64) float64 {
	if len(p.ThroughputWavelengths) != len(p.Throughput) || len(p.Throughput) < 2 {
		return 1
	}
	if wavelength <= p.ThroughputWavelengths[0] {
		return p.Throughput[0]
	}
	last := len(p.Throughput) - 1
	if wavelength >= p.ThroughputWavelengths[last] {
		return p.Throughput[last]
	}
	for i := 1; i < len(p.ThroughputWavelengths); i++ {
		if wavelength <= p.ThroughputWavelengths[i] {
			a, b := p.ThroughputWavelengths[i-1], p.ThroughputWavelengths[i]
			u := (wavelength - a) / (b - a)
			return p.Throughput[i-1] + u*(p.Throughput[i]-p.Throughput[i-1])
		}
	}
	return 0
}

// SyntheticPhotometry is the named Step-12 API for deterministic XP/passband
// integration. It rejects non-positive throughput and spectra with no overlap.
func SyntheticPhotometry(s gaia.XPSpectrum, p GaiaPassband) (float64, error) {
	for _, v := range p.Throughput {
		if !finiteFloat(v) || v < 0 {
			return 0, fmt.Errorf("invalid passband throughput")
		}
	}
	return IntegrateXPSpectrum(s, p)
}

// PropagateGaiaPosition applies proper motion (mas/yr) to an observation epoch.
func PropagateGaiaPosition(s gaia.Source, epoch float64) gaia.Coordinate {
	dt := epoch - s.ReferenceEpoch
	dec := s.Dec + s.ProperMotionDec*dt/(3600000.0)
	ra := s.RA + s.ProperMotionRA*dt/(3600000.0*math.Max(0.01, math.Cos(s.Dec*math.Pi/180)))
	ra = math.Mod(ra, 360)
	if ra < 0 {
		ra += 360
	}
	return gaia.Coordinate{RA: ra, Dec: dec}
}

type GaiaMatch struct {
	Star           Star
	Source         gaia.Source
	DistanceArcsec float64
}

// CrossmatchGaiaStarsWithWCS matches detections after projecting pixel
// coordinates through a verified WCS callback. The callback must return RA/Dec
// in degrees; keeping projection outside this package avoids guessing header
// conventions while making translation/rotation tests deterministic.
func CrossmatchGaiaStarsWithWCS(stars []Star, sources []gaia.Source, radiusArcsec, epoch float64, pixelToSky func(x, y float64) (gaia.Coordinate, error)) ([]GaiaMatch, error) {
	return crossmatchGaiaStarsWithWCSContext(context.Background(), stars, sources, radiusArcsec, epoch, pixelToSky)
}
func crossmatchGaiaStarsWithWCSContext(ctx context.Context, stars []Star, sources []gaia.Source, radiusArcsec, epoch float64, pixelToSky func(x, y float64) (gaia.Coordinate, error)) ([]GaiaMatch, error) {
	if pixelToSky == nil || radiusArcsec <= 0 || !finiteFloat(radiusArcsec) {
		return nil, fmt.Errorf("WCS projection and positive radius are required")
	}
	out := make([]GaiaMatch, 0)
	used := map[uint64]bool{}
	projected, candidates, ambiguous := 0, 0, 0
	samples := make([]float64, 0, 3)
	nearestCatalog := make([]float64, 0, 3)
	for _, st := range stars {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		coord, err := pixelToSky(st.X, st.Y)
		if err != nil {
			return nil, err
		}
		projected++
		best, second := GaiaMatch{}, math.Inf(1)
		nearest := math.Inf(1)
		found := false
		for _, src := range sources {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if used[src.SourceID] {
				continue
			}
			pos := PropagateGaiaPosition(src, epoch)
			dra := math.Mod(pos.RA-coord.RA+540, 360) - 180
			d := math.Hypot(dra*3600*math.Cos(coord.Dec*math.Pi/180), (pos.Dec-coord.Dec)*3600)
			if d < nearest {
				nearest = d
			}
			if d <= radiusArcsec {
				candidates++
				if !found || d < best.DistanceArcsec || (d == best.DistanceArcsec && src.SourceID < best.Source.SourceID) {
					if found {
						second = best.DistanceArcsec
					}
					best = GaiaMatch{Star: st, Source: src, DistanceArcsec: d}
					found = true
				} else if d < second {
					second = d
				}
			}
		}
		if len(nearestCatalog) < cap(nearestCatalog) {
			nearestCatalog = append(nearestCatalog, nearest)
		}
		if found && second < math.Inf(1) && second-best.DistanceArcsec < 0.1 {
			ambiguous++
			continue
		} // ambiguous at astrometric precision
		if found {
			used[best.Source.SourceID] = true
			out = append(out, best)
			if len(samples) < cap(samples) {
				samples = append(samples, best.DistanceArcsec)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source.SourceID < out[j].Source.SourceID })
	debuglog.Log(fmt.Sprintf("Gaia WCS crossmatch: stars=%d sources=%d projected=%d candidates=%d matched=%d ambiguous=%d radius=%.3f arcsec matched_distance_samples=%v nearest_catalog_samples=%v", len(stars), len(sources), projected, candidates, len(out), ambiguous, radiusArcsec, samples, nearestCatalog))
	return out, nil
}

// CrossmatchGaiaStars performs deterministic nearest-neighbour matching.
func CrossmatchGaiaStars(stars []Star, sources []gaia.Source, radiusArcsec, epoch float64) []GaiaMatch {
	out := make([]GaiaMatch, 0)
	used := map[uint64]bool{}
	for _, st := range stars {
		best := GaiaMatch{}
		found := false
		for _, src := range sources {
			if used[src.SourceID] {
				continue
			}
			pos := PropagateGaiaPosition(src, epoch)
			dx := (pos.RA - st.X) * 3600 * math.Cos(pos.Dec*math.Pi/180)
			dy := (pos.Dec - st.Y) * 3600
			d := math.Hypot(dx, dy)
			if d <= radiusArcsec && (!found || d < best.DistanceArcsec || (d == best.DistanceArcsec && src.SourceID < best.Source.SourceID)) {
				best = GaiaMatch{Star: st, Source: src, DistanceArcsec: d}
				found = true
			}
		}
		if found {
			used[best.Source.SourceID] = true
			out = append(out, best)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source.SourceID < out[j].Source.SourceID })
	return out
}

func GaiaPassbandByName(name string) (GaiaPassband, error) {
	p, ok := gaiaPassbands[name]
	if !ok {
		return GaiaPassband{}, fmt.Errorf("unsupported Gaia passband %q", name)
	}
	return p, nil
}

// GaiaOverlayScalar returns a scalar normalization only for an exact supported passband.
func GaiaOverlayScalar(passband string, spectrum gaia.XPSpectrum, observed float64) (float64, error) {
	p, err := GaiaPassbandByName(passband)
	if err != nil {
		return 0, err
	}
	if len(spectrum.Wavelengths) < 2 || len(spectrum.Wavelengths) != len(spectrum.Flux) || spectrum.Wavelengths[0] > p.MinWavelengthNm || spectrum.Wavelengths[len(spectrum.Wavelengths)-1] < p.MaxWavelengthNm {
		return 0, fmt.Errorf("unsupported overlay passband %q: incomplete XP coverage", passband)
	}
	expected, err := IntegrateXPSpectrum(spectrum, p)
	if err != nil || observed <= 0 {
		return 0, fmt.Errorf("unsupported overlay passband %q", passband)
	}
	return expected / observed, nil
}

type GaiaCalibrationSettings struct {
	Release           string
	XPRepresentation  string
	Passbands         []string
	AlgorithmVersion  string
	QualitySelector   string
	MagnitudeLimit    float64
	MatchRadiusArcsec float64
	ObservationEpoch  float64
}

func (s GaiaCalibrationSettings) Validate() error {
	if s.Release == "" || s.XPRepresentation == "" || s.AlgorithmVersion == "" {
		return fmt.Errorf("Gaia release, XP representation, and algorithm version are required")
	}
	if len(s.Passbands) == 0 || !finiteFloat(s.MagnitudeLimit) || s.MagnitudeLimit <= 0 || !finiteFloat(s.MatchRadiusArcsec) || s.MatchRadiusArcsec <= 0 || !finiteFloat(s.ObservationEpoch) {
		return fmt.Errorf("invalid Gaia calibration settings")
	}
	seen := make(map[string]bool, len(s.Passbands))
	for _, name := range s.Passbands {
		if seen[name] {
			return fmt.Errorf("duplicate Gaia passband %q", name)
		}
		seen[name] = true
		if _, err := GaiaPassbandByName(name); err != nil {
			return err
		}
	}
	return nil
}

type GaiaStarResidual struct {
	SourceID  uint64
	Residuals [3]float64
}

type GaiaFitDiagnostics struct {
	DetectedStars    int
	MatchedStars     int
	AcceptedStars    int
	RejectedStars    int
	RejectedByReason map[string]int
	Residuals        []GaiaStarResidual
	RobustScatter    [3]float64
	Gains            [3]float64
	SourceIDs        []uint64
	CatalogVersion   string
	PassbandVersions []string
	AlgorithmVersion string
	RejectedSources  []GaiaSourceRejection
}
type GaiaSourceRejection struct {
	SourceID  uint64
	Reason    string
	Residuals [3]float64
}

func (d GaiaFitDiagnostics) Copy() GaiaFitDiagnostics {
	d.RejectedByReason = copyIntMap(d.RejectedByReason)
	d.Residuals = append([]GaiaStarResidual(nil), d.Residuals...)
	d.SourceIDs = append([]uint64(nil), d.SourceIDs...)
	d.PassbandVersions = append([]string(nil), d.PassbandVersions...)
	d.RejectedSources = append([]GaiaSourceRejection(nil), d.RejectedSources...)
	return d
}

type GaiaCalibrationRequest struct {
	Provider gaia.Provider
	Query    gaia.FieldQuery
	Settings GaiaCalibrationSettings
	// Optional aligned detections and measured linear channel fluxes. A valid
	// Gaia result requires these inputs; provider staging alone is insufficient.
	Measurements  []GaiaChannelMeasurement
	DetectedStars []Star
	Planes        [3]GaiaPlane
	PixelToSky    func(x, y float64) (gaia.Coordinate, error)
	Aperture      GaiaApertureConfig
}

type GaiaPlane struct {
	Pixels        []float32
	Valid         []bool
	Width, Height int
}
type GaiaApertureConfig struct{ Radius, AnnulusInner, AnnulusOuter, Saturation float64 }

type GaiaCalibrationResult struct {
	Status              models.CalibrationStatus
	Diagnostics         GaiaFitDiagnostics
	Provenance          models.CalibrationProvenance
	SettingsFingerprint string
	SourceFingerprint   string
}

// GaiaChannelMeasurement is an observed linear aperture flux paired with a Gaia source.
type GaiaChannelMeasurement struct {
	SourceID                                   uint64
	Flux                                       [3]float64
	SNR                                        float64
	Saturated, Blended, Variable, Contaminated bool
	ApertureError                              bool
}

// FitGaiaGains estimates relative channel gains in log-ratio space.
func FitGaiaGains(ctx context.Context, measurements []GaiaChannelMeasurement, expected map[uint64][3]float64, minStars int) (GaiaCalibrationResult, error) {
	debuglog.Log(fmt.Sprintf("Gaia fit: measurements=%d expected=%d min_stars=%d", len(measurements), len(expected), minStars))
	if minStars <= 0 {
		minStars = 3
	}
	vals := [3][]float64{}
	validIDs := make([]uint64, 0, len(measurements))
	d := GaiaFitDiagnostics{RejectedByReason: map[string]int{}}
	seen := make(map[uint64]struct{}, len(measurements))
	for _, m := range measurements {
		if err := ctx.Err(); err != nil {
			return GaiaCalibrationResult{Status: models.CalibrationCancelled}, err
		}
		if _, ok := seen[m.SourceID]; ok {
			d.RejectedByReason["duplicate"]++
			d.RejectedSources = append(d.RejectedSources, GaiaSourceRejection{SourceID: m.SourceID, Reason: "duplicate"})
			return GaiaCalibrationResult{Status: models.CalibrationUnsupported, Diagnostics: d}, fmt.Errorf("duplicate Gaia measurement source ID: %d", m.SourceID)
		}
		seen[m.SourceID] = struct{}{}
		if m.Variable || m.Contaminated || m.Blended || m.Saturated || m.ApertureError || m.SNR < 3 || !finiteFloat(m.SNR) {
			d.RejectedByReason["quality"]++
			switch {
			case m.Variable:
				d.RejectedByReason["variable"]++
			case m.Contaminated:
				d.RejectedByReason["contaminated"]++
			case m.Blended:
				d.RejectedByReason["blend"]++
			case m.Saturated:
				d.RejectedByReason["saturation"]++
			case m.ApertureError:
				d.RejectedByReason["aperture"]++
			default:
				d.RejectedByReason["low_snr"]++
			}
			reason := "low_snr"
			if m.Variable {
				reason = "variable"
			} else if m.Contaminated {
				reason = "contaminated"
			} else if m.Blended {
				reason = "blend"
			} else if m.Saturated {
				reason = "saturation"
			} else if m.ApertureError {
				reason = "aperture"
			}
			d.RejectedSources = append(d.RejectedSources, GaiaSourceRejection{SourceID: m.SourceID, Reason: reason})
			continue
		}
		if !gaiaObservedFluxValid(m.Flux) {
			d.RejectedByReason["invalid_flux"]++
			d.RejectedSources = append(d.RejectedSources, GaiaSourceRejection{SourceID: m.SourceID, Reason: "invalid_flux"})
			continue
		}
		e, ok := expected[m.SourceID]
		if !ok {
			d.RejectedByReason["unmatched"]++
			d.RejectedSources = append(d.RejectedSources, GaiaSourceRejection{SourceID: m.SourceID, Reason: "unmatched"})
			continue
		}
		good := true
		for c := 0; c < 3; c++ {
			if e[c] <= 0 || !finiteFloat(e[c]) {
				good = false
			}
		}
		if !good {
			d.RejectedByReason["invalid_flux"]++
			d.RejectedSources = append(d.RejectedSources, GaiaSourceRejection{SourceID: m.SourceID, Reason: "invalid_flux"})
			continue
		}
		for c := 0; c < 3; c++ {
			vals[c] = append(vals[c], math.Log(e[c]/m.Flux[c]))
		}
		validIDs = append(validIDs, m.SourceID)
	}
	if len(validIDs) < minStars {
		debuglog.Log(fmt.Sprintf("Gaia fit rejected: valid=%d min_stars=%d reasons=%v", len(validIDs), minStars, d.RejectedByReason))
		return GaiaCalibrationResult{Status: models.CalibrationUnsupported, Diagnostics: d}, fmt.Errorf("insufficient Gaia stars: %d", len(validIDs))
	}
	if minStars >= 3 {
		minColor, maxColor := math.Inf(1), math.Inf(-1)
		for _, id := range validIDs {
			e := expected[id]
			if e[1] <= 0 || e[2] <= 0 {
				continue
			}
			color := math.Log(e[1] / e[2])
			if color < minColor {
				minColor = color
			}
			if color > maxColor {
				maxColor = color
			}
		}
		if !finiteFloat(minColor) || maxColor-minColor < 0.02 {
			return GaiaCalibrationResult{Status: models.CalibrationUnsupported, Diagnostics: d}, fmt.Errorf("insufficient Gaia color span")
		}
	}
	// Iterative robust rejection in log-ratio space until the inlier set
	// stabilizes (bounded to keep cancellation responsive).
	medians := [3]float64{}
	for c := 0; c < 3; c++ {
		sort.Float64s(vals[c])
		medians[c] = vals[c][len(vals[c])/2]
		d.Gains[c] = math.Exp(medians[c])
	}
	accepted := append([]uint64(nil), validIDs...)
	for iter := 0; iter < 8; iter++ {
		prev := append([]uint64(nil), accepted...)
		accepted = accepted[:0]
		for _, id := range prev {
			m := measurementsByID(measurements, id)
			keep := m != nil
			for c := 0; keep && c < 3; c++ {
				r := math.Log(expected[id][c] / m.Flux[c])
				if math.Abs(r-medians[c]) > 0.35 {
					keep = false
				}
			}
			if keep {
				accepted = append(accepted, id)
			}
		}
		for c := 0; c < 3; c++ {
			inliers := make([]float64, 0, len(accepted))
			if len(accepted) == 0 {
				break
			}
			for _, id := range accepted {
				m := measurementsByID(measurements, id)
				inliers = append(inliers, math.Log(expected[id][c]/m.Flux[c]))
			}
			sort.Float64s(inliers)
			medians[c] = inliers[len(inliers)/2]
		}
		if len(prev) == len(accepted) {
			same := true
			for i := range prev {
				if prev[i] != accepted[i] {
					same = false
					break
				}
			}
			if same {
				break
			}
		}
	}
	for _, id := range validIDs {
		found := false
		for _, a := range accepted {
			if a == id {
				found = true
				break
			}
		}
		if !found {
			d.RejectedByReason["outlier"]++
			var rr [3]float64
			if m := measurementsByID(measurements, id); m != nil {
				for c := 0; c < 3; c++ {
					rr[c] = math.Log(expected[id][c]/m.Flux[c]) - medians[c]
				}
			}
			d.RejectedSources = append(d.RejectedSources, GaiaSourceRejection{SourceID: id, Reason: "outlier", Residuals: rr})
		}
	}
	if len(accepted) < minStars {
		debuglog.Log(fmt.Sprintf("Gaia fit rejected after robust filtering: accepted=%d min_stars=%d reasons=%v", len(accepted), minStars, d.RejectedByReason))
		return GaiaCalibrationResult{Status: models.CalibrationUnsupported, Diagnostics: d}, fmt.Errorf("insufficient inlier Gaia stars: %d", len(accepted))
	}
	for c := 0; c < 3; c++ {
		inliers := make([]float64, 0, len(accepted))
		for _, id := range accepted {
			m := measurementsByID(measurements, id)
			inliers = append(inliers, math.Log(expected[id][c]/m.Flux[c]))
		}
		sort.Float64s(inliers)
		medians[c] = inliers[len(inliers)/2]
		d.Gains[c] = math.Exp(medians[c])
	}
	d.SourceIDs = accepted
	g := math.Exp((math.Log(d.Gains[0]) + math.Log(d.Gains[1]) + math.Log(d.Gains[2])) / 3)
	for c := 0; c < 3; c++ {
		d.Gains[c] /= g
	}
	d.AcceptedStars = len(d.SourceIDs)
	d.RejectedStars = 0
	for k, n := range d.RejectedByReason {
		if k != "quality" {
			d.RejectedStars += n
		}
	}
	d.MatchedStars = 0
	for _, m := range measurements {
		if _, ok := expected[m.SourceID]; ok {
			d.MatchedStars++
		}
	}
	for _, id := range d.SourceIDs {
		m := measurementsByID(measurements, id)
		e := expected[id]
		var r GaiaStarResidual
		r.SourceID = id
		for c := 0; c < 3; c++ {
			r.Residuals[c] = math.Log(e[c]/m.Flux[c]) - medians[c]
		}
		d.Residuals = append(d.Residuals, r)
	}
	for c := 0; c < 3; c++ {
		var sum float64
		for _, r := range d.Residuals {
			sum += r.Residuals[c] * r.Residuals[c]
		}
		if len(d.Residuals) > 0 {
			d.RobustScatter[c] = math.Sqrt(sum / float64(len(d.Residuals)))
		}
	}
	debuglog.Log(fmt.Sprintf("Gaia fit complete: matched=%d accepted=%d rejected=%d gains=[%.6g %.6g %.6g] scatter=[%.6g %.6g %.6g] reasons=%v", d.MatchedStars, d.AcceptedStars, d.RejectedStars, d.Gains[0], d.Gains[1], d.Gains[2], d.RobustScatter[0], d.RobustScatter[1], d.RobustScatter[2], d.RejectedByReason))
	return GaiaCalibrationResult{Status: models.CalibrationValid, Diagnostics: d}, nil
}

func gaiaObservedFluxValid(flux [3]float64) bool {
	for _, value := range flux {
		if value <= 0 || !finiteFloat(value) {
			return false
		}
	}
	return true
}

func gaiaMeasurementEligible(m GaiaChannelMeasurement) bool {
	return !m.Variable && !m.Contaminated && !m.Blended && !m.Saturated && !m.ApertureError && m.SNR >= 3 && finiteFloat(m.SNR) && gaiaObservedFluxValid(m.Flux)
}

func measurementsByID(ms []GaiaChannelMeasurement, id uint64) *GaiaChannelMeasurement {
	for i := range ms {
		if ms[i].SourceID == id {
			return &ms[i]
		}
	}
	return nil
}

// PrepareGaiaCalibration validates and deterministically stages provider data.
// The numerical stellar fit is intentionally Step 12; this contract-level
// operation makes online, cache-only, and test providers interchangeable.
func PrepareGaiaCalibration(ctx context.Context, req GaiaCalibrationRequest) (GaiaCalibrationResult, error) {
	debuglog.Log(fmt.Sprintf("Gaia prepare: release=%s xp=%s passbands=%v radius=%.3f epoch=%.3f detected=%d measurements=%d", req.Settings.Release, req.Settings.XPRepresentation, req.Settings.Passbands, req.Settings.MatchRadiusArcsec, req.Settings.ObservationEpoch, len(req.DetectedStars), len(req.Measurements)))
	if req.Provider == nil {
		return GaiaCalibrationResult{Status: models.CalibrationUnsupported}, fmt.Errorf("Gaia provider is required")
	}
	if err := req.Settings.Validate(); err != nil {
		return GaiaCalibrationResult{Status: models.CalibrationUnsupported}, err
	}
	q := req.Query
	q.Release = req.Settings.Release
	q.MagnitudeLimit = req.Settings.MagnitudeLimit
	q.ObservationEpoch = req.Settings.ObservationEpoch
	if err := q.Validate(); err != nil {
		return GaiaCalibrationResult{Status: models.CalibrationUnsupported}, err
	}
	prov, err := req.Provider.Provenance(ctx)
	if err != nil {
		return GaiaCalibrationResult{Status: gaiaErrorStatus(err)}, err
	}
	if err := prov.Validate(); err != nil || prov.Release != req.Settings.Release || prov.XPRepresentation != req.Settings.XPRepresentation {
		if err == nil {
			err = fmt.Errorf("provider provenance does not match Gaia settings")
		}
		return GaiaCalibrationResult{Status: models.CalibrationUnsupported}, err
	}
	sources, err := req.Provider.DiscoverSources(ctx, q)
	if err != nil {
		return GaiaCalibrationResult{Status: gaiaErrorStatus(err)}, err
	}
	sources, err = gaia.NormalizeSources(sources, req.Settings.Release)
	if err != nil {
		return GaiaCalibrationResult{Status: models.CalibrationUnsupported}, err
	}
	if len(sources) == 0 {
		return GaiaCalibrationResult{Status: models.CalibrationUnsupported}, fmt.Errorf("Gaia field contains no usable sources")
	}
	debuglog.Log(fmt.Sprintf("Gaia catalog: provider=%s release=%s sources=%d", prov.Provider, prov.Release, len(sources)))
	if err := ctx.Err(); err != nil {
		return GaiaCalibrationResult{Status: models.CalibrationCancelled}, err
	}
	minStars := 3
	detectedCount := len(sources)
	matchedCount := len(req.Measurements)
	if len(req.Measurements) == 0 {
		if len(req.DetectedStars) > 0 && req.PixelToSky != nil {
			matches, merr := crossmatchGaiaStarsWithWCSContext(ctx, req.DetectedStars, sources, req.Settings.MatchRadiusArcsec, req.Settings.ObservationEpoch, req.PixelToSky)
			if merr != nil {
				debuglog.Log(fmt.Sprintf("Gaia crossmatch failed: %v", merr))
				return GaiaCalibrationResult{Status: gaiaErrorStatus(merr)}, merr
			}
			detectedCount = len(req.DetectedStars)
			matchedCount = len(matches)
			debuglog.Log(fmt.Sprintf("Gaia crossmatch accepted=%d detected=%d", matchedCount, detectedCount))
			cfg := req.Aperture
			if cfg.Radius <= 0 {
				cfg = GaiaApertureConfig{Radius: 3, AnnulusInner: 5, AnnulusOuter: 8, Saturation: math.Inf(1)}
			}
			req.Measurements = make([]GaiaChannelMeasurement, 0, len(matches))
			apertureRejects := 0
			apertureRejectSamples := make([]string, 0, 4)
			for _, match := range matches {
				if err := ctx.Err(); err != nil {
					return GaiaCalibrationResult{Status: models.CalibrationCancelled}, err
				}
				m := GaiaChannelMeasurement{SourceID: match.Source.SourceID, SNR: math.Inf(1)}
				m.Variable = len(match.Source.VariabilityFlags) > 0
				m.Contaminated = len(match.Source.ContaminationFlags) > 0
				m.Blended = match.Star.Area > 100
				if m.Variable || m.Contaminated || m.Blended {
					apertureRejects++
					if len(apertureRejectSamples) < cap(apertureRejectSamples) {
						apertureRejectSamples = append(apertureRejectSamples, fmt.Sprintf("%d:quality", m.SourceID))
					}
					req.Measurements = append(req.Measurements, m)
					continue
				}
				valid := true
				for c := 0; c < 3; c++ {
					if err := ctx.Err(); err != nil {
						return GaiaCalibrationResult{Status: models.CalibrationCancelled}, err
					}
					if req.Planes[c].Width <= 0 {
						valid = false
						break
					}
					ap, aerr := MeasureApertureAnnulus(req.Planes[c].Pixels, req.Planes[c].Width, req.Planes[c].Height, match.Star.X, match.Star.Y, cfg.Radius, cfg.AnnulusInner, cfg.AnnulusOuter, cfg.Saturation)
					if aerr != nil || ap.Saturated {
						apertureRejects++
						if len(apertureRejectSamples) < cap(apertureRejectSamples) {
							reason := "error"
							if ap.Saturated {
								reason = "saturation"
							}
							apertureRejectSamples = append(apertureRejectSamples, fmt.Sprintf("%d:%s", m.SourceID, reason))
						}
						if ap.Saturated {
							m.Saturated = true
						} else {
							m.ApertureError = true
						}
						valid = false
						break
					}
					m.Flux[c], m.SNR = ap.Flux, math.Min(m.SNR, ap.SNR)
				}
				if valid {
					req.Measurements = append(req.Measurements, m)
				} else {
					req.Measurements = append(req.Measurements, m)
				}
			}
			debuglog.Log(fmt.Sprintf("Gaia aperture summary: matches=%d rejected=%d samples=%v", len(matches), apertureRejects, apertureRejectSamples))
		}
		if len(req.Measurements) == 0 {
			if len(req.DetectedStars) == 0 {
				return GaiaCalibrationResult{Status: models.CalibrationUnsupported}, fmt.Errorf("Gaia calibration found no detected stars; verify star detection and image alignment")
			}
			if matchedCount == 0 {
				return GaiaCalibrationResult{Status: models.CalibrationUnsupported}, fmt.Errorf("Gaia calibration found no Gaia crossmatches for %d detected stars; verify WCS alignment and increase the match radius if needed", len(req.DetectedStars))
			}
			return GaiaCalibrationResult{Status: models.CalibrationUnsupported}, fmt.Errorf("Gaia calibration produced no measured aligned aperture fluxes from %d Gaia crossmatches; verify aperture geometry and image alignment", matchedCount)
		}
	}
	if err := ctx.Err(); err != nil {
		return GaiaCalibrationResult{Status: models.CalibrationCancelled}, err
	}
	if duplicateID, ok := duplicateGaiaMeasurementID(req.Measurements); ok {
		return GaiaCalibrationResult{Status: models.CalibrationUnsupported}, fmt.Errorf("duplicate Gaia measurement source ID: %d", duplicateID)
	}
	// Restrict the calibration set to discovered sources that have a measured
	// aperture flux (including measurements produced by the WCS crossmatch).
	measured := make(map[uint64]struct{}, len(req.Measurements))
	discovered := make(map[uint64]struct{}, len(sources))
	for _, source := range sources {
		discovered[source.SourceID] = struct{}{}
	}
	matchedCount = 0
	for _, m := range req.Measurements {
		if _, ok := discovered[m.SourceID]; ok {
			matchedCount++
		}
		if gaiaMeasurementEligible(m) {
			measured[m.SourceID] = struct{}{}
		}
	}
	selectedSources := make([]gaia.Source, 0, len(measured))
	for _, source := range sources {
		if _, ok := measured[source.SourceID]; ok {
			selectedSources = append(selectedSources, source)
		}
	}
	ids := make([]uint64, len(selectedSources))
	for i, source := range selectedSources {
		ids[i] = source.SourceID
	}
	if len(ids) == 0 {
		debuglog.Log(fmt.Sprintf("Gaia calibration terminal: no usable aperture measurements matched=%d", matchedCount))
		return GaiaCalibrationResult{Status: models.CalibrationUnsupported}, fmt.Errorf("Gaia calibration produced no usable aperture measurements from %d Gaia crossmatches; verify aperture geometry, saturation, and image alignment", matchedCount)
	}
	spectra, err := req.Provider.RetrieveXPSpectra(ctx, req.Settings.Release, req.Settings.XPRepresentation, ids)
	if err != nil {
		debuglog.Log(fmt.Sprintf("Gaia XP retrieval failed: ids=%d error=%v", len(ids), err))
		return GaiaCalibrationResult{Status: gaiaErrorStatus(err)}, err
	}
	debuglog.Log(fmt.Sprintf("Gaia XP retrieval: requested=%d returned=%d", len(ids), len(spectra)))
	spectra, err = gaia.NormalizeXPSpectra(spectra, req.Settings.Release, req.Settings.XPRepresentation)
	if err != nil {
		return GaiaCalibrationResult{Status: models.CalibrationUnsupported}, err
	}
	if len(spectra) == 0 {
		return GaiaCalibrationResult{Status: models.CalibrationUnsupported}, fmt.Errorf("Gaia field contains no usable XP spectra")
	}
	if len(spectra) != len(ids) {
		return GaiaCalibrationResult{Status: models.CalibrationUnsupported}, fmt.Errorf("Gaia provider returned %d spectra for %d sources", len(spectra), len(ids))
	}
	for i, id := range ids {
		if spectra[i].SourceID != id {
			return GaiaCalibrationResult{Status: models.CalibrationUnsupported}, fmt.Errorf("Gaia source %d has no matching XP spectrum", id)
		}
	}
	expected := make(map[uint64][3]float64, len(spectra))
	for _, xp := range spectra {
		var e [3]float64
		for c, name := range req.Settings.Passbands {
			if c >= 3 {
				break
			}
			p := gaiaPassbands[name]
			v, ierr := IntegrateXPSpectrum(xp, p)
			if ierr != nil {
				continue
			}
			e[c] = v
		}
		for c := len(req.Settings.Passbands); c < 3; c++ {
			e[c] = e[0]
		}
		expected[xp.SourceID] = e
	}
	fit, ferr := FitGaiaGains(ctx, req.Measurements, expected, minStars)
	fit.Diagnostics.DetectedStars = detectedCount
	fit.Diagnostics.MatchedStars = matchedCount
	if detectedCount > matchedCount {
		fit.Diagnostics.RejectedByReason["unmatched"] += detectedCount - matchedCount
		fit.Diagnostics.RejectedStars += detectedCount - matchedCount
	}
	if ferr != nil {
		debuglog.Log(fmt.Sprintf("Gaia calibration terminal: fit failed matched=%d accepted=%d error=%v", fit.Diagnostics.MatchedStars, fit.Diagnostics.AcceptedStars, ferr))
		return fit, ferr
	}
	d := fit.Diagnostics.Copy()
	d.DetectedStars = detectedCount
	d.MatchedStars = matchedCount
	d.CatalogVersion = prov.Release
	d.AlgorithmVersion = req.Settings.AlgorithmVersion
	for _, p := range req.Settings.Passbands {
		d.PassbandVersions = append(d.PassbandVersions, gaiaPassbands[p].Version)
	}
	sort.Strings(d.PassbandVersions)
	debuglog.Log(fmt.Sprintf("Gaia calibration terminal: valid detected=%d matched=%d accepted=%d rejected=%d", d.DetectedStars, d.MatchedStars, d.AcceptedStars, d.RejectedStars))
	return GaiaCalibrationResult{Status: models.CalibrationValid, Diagnostics: d, Provenance: models.CalibrationProvenance{AlgorithmVersion: req.Settings.AlgorithmVersion, CatalogVersion: prov.Release, ProviderVersion: prov.ProviderVersion, Source: prov.Provider, PassbandVersions: append([]string(nil), d.PassbandVersions...), SourceIDs: append([]uint64(nil), ids...)}, SettingsFingerprint: GaiaCalibrationFingerprint(req.Settings, prov), SourceFingerprint: GaiaSourceFingerprint(selectedSources, spectra)}, nil
}

func duplicateGaiaMeasurementID(measurements []GaiaChannelMeasurement) (uint64, bool) {
	seen := make(map[uint64]struct{}, len(measurements))
	for _, measurement := range measurements {
		if _, ok := seen[measurement.SourceID]; ok {
			return measurement.SourceID, true
		}
		seen[measurement.SourceID] = struct{}{}
	}
	return 0, false
}

func GaiaCalibrationFingerprint(s GaiaCalibrationSettings, p gaia.Provenance) string {
	h := sha256.New()
	h.Write([]byte(GaiaSettingsFingerprint(s)))
	h.Write([]byte{0})
	h.Write([]byte(p.Provider))
	h.Write([]byte{0})
	h.Write([]byte(p.ProviderVersion))
	h.Write([]byte{0})
	h.Write([]byte(p.EndpointSemantics))
	return hex.EncodeToString(h.Sum(nil))
}

func GaiaSettingsFingerprint(s GaiaCalibrationSettings) string {
	h := sha256.New()
	h.Write([]byte(s.Release))
	h.Write([]byte{0})
	h.Write([]byte(s.XPRepresentation))
	h.Write([]byte{0})
	h.Write([]byte(s.AlgorithmVersion))
	h.Write([]byte{0})
	h.Write([]byte(s.QualitySelector))
	h.Write([]byte{0})
	for _, p := range s.Passbands {
		h.Write([]byte(p))
		h.Write([]byte{0})
		h.Write([]byte(gaiaPassbands[p].Version))
		h.Write([]byte{0})
	}
	for _, v := range []float64{s.MagnitudeLimit, s.MatchRadiusArcsec, s.ObservationEpoch} {
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], math.Float64bits(v))
		h.Write(b[:])
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func GaiaSourceFingerprint(sources []gaia.Source, spectra []gaia.XPSpectrum) string {
	h := sha256.New()
	for _, s := range sources {
		binary.Write(h, binary.BigEndian, s.SourceID)
		h.Write([]byte(s.Release))
		for _, v := range []float64{s.RA, s.Dec, s.ReferenceEpoch, s.ProperMotionRA, s.ProperMotionDec, s.PositionError, s.ProperMotionErrorRA, s.ProperMotionErrorDec, s.G, s.BP, s.RP, s.GError, s.BPError, s.RPError} {
			binary.Write(h, binary.BigEndian, v)
		}
		writeStrings(h, "quality", s.QualityFlags)
		writeStrings(h, "variability", s.VariabilityFlags)
		writeStrings(h, "contamination", s.ContaminationFlags)
	}
	for _, s := range spectra {
		binary.Write(h, binary.BigEndian, s.SourceID)
		h.Write([]byte(s.Release))
		h.Write([]byte(s.RepresentationVersion))
		h.Write([]byte(s.CalibrationVersion))
		for _, v := range s.Wavelengths {
			binary.Write(h, binary.BigEndian, v)
		}
		for _, v := range s.Flux {
			binary.Write(h, binary.BigEndian, v)
		}
		for _, v := range s.FluxErrors {
			binary.Write(h, binary.BigEndian, v)
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func finiteFloat(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func copyIntMap(in map[string]int) map[string]int {
	out := make(map[string]int, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func writeStrings(h interface{ Write([]byte) (int, error) }, domain string, values []string) {
	sorted := append([]string(nil), values...)
	sort.Strings(sorted)
	h.Write([]byte(domain))
	var count [8]byte
	binary.BigEndian.PutUint64(count[:], uint64(len(sorted)))
	h.Write(count[:])
	for _, value := range sorted {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		h.Write(length[:])
		h.Write([]byte(value))
	}
}

func gaiaErrorStatus(err error) models.CalibrationStatus {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return models.CalibrationCancelled
	}
	return models.CalibrationFailed
}
