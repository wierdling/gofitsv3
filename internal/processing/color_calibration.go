package processing

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"gofitsv3/internal/models"
)

// BackgroundConfig controls the deterministic scalar background estimator.
// TileUniformityThreshold is the maximum tile-median spread relative to the
// global median (default 0.05); scalar subtraction is refused above it.
type BackgroundConfig struct {
	MinSamples              int
	TileSize                int
	TileUniformityThreshold float64
	SigmaClip               float64
	MaxIterations           int
}

// BackgroundPlane is an aligned plane. Valid and StarMask are optional; a
// missing entry is treated as invalid/unsmasked respectively.
type BackgroundPlane struct {
	Pixels   []float32
	Width    int
	Height   int
	Valid    []bool
	StarMask []bool
	ROI      *models.CalibrationROI
}

type BackgroundEstimate struct {
	Transform       models.LinearTransform
	Dispersion      float64
	Samples         int
	Accepted        int
	Rejected        int
	TileCount       int
	TileSpread      float64
	Status          models.CalibrationStatus
	RejectionReason string
}

// UnsupportedCalibrationError identifies an explicit calibration boundary
// (for example an invalid ROI or a non-uniform background). Callers can
// surface its reason without treating the result as a generic calculation
// failure.
type UnsupportedCalibrationError struct{ Reason string }

func (e *UnsupportedCalibrationError) Error() string { return "unsupported calibration: " + e.Reason }

func IsUnsupportedCalibration(err error) bool {
	_, ok := err.(*UnsupportedCalibrationError)
	return ok
}

func DefaultBackgroundConfig() BackgroundConfig {
	return BackgroundConfig{MinSamples: 32, TileSize: 32, TileUniformityThreshold: 0.05, SigmaClip: 3.0, MaxIterations: 5}
}

// EstimateBackground computes a scalar offset without modifying the input.
// It deliberately reports unsupported for gradients and underconstrained data.
func EstimateBackgroundPlane(ctx context.Context, plane BackgroundPlane, cfg BackgroundConfig) (BackgroundEstimate, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	d := DefaultBackgroundConfig()
	if cfg.MinSamples > 0 {
		d.MinSamples = cfg.MinSamples
	}
	if cfg.TileSize > 0 {
		d.TileSize = cfg.TileSize
	}
	if cfg.TileUniformityThreshold > 0 {
		d.TileUniformityThreshold = cfg.TileUniformityThreshold
	}
	if cfg.SigmaClip > 0 {
		d.SigmaClip = cfg.SigmaClip
	}
	if cfg.MaxIterations > 0 {
		d.MaxIterations = cfg.MaxIterations
	}
	if plane.Width <= 0 || plane.Height <= 0 {
		return BackgroundEstimate{Status: models.CalibrationUnsupported, RejectionReason: "invalid dimensions"}, nil
	}
	type sample struct {
		index int
		value float64
	}
	samples := make([]sample, 0, len(plane.Pixels))
	for i, raw := range plane.Pixels {
		if i%(d.TileSize*4) == 0 {
			select {
			case <-ctx.Done():
				return BackgroundEstimate{Status: models.CalibrationCancelled, RejectionReason: "cancelled"}, ctx.Err()
			default:
			}
		}
		x, y := i%plane.Width, i/plane.Width
		if y >= plane.Height || (plane.Valid != nil && (i >= len(plane.Valid) || !plane.Valid[i])) || (plane.StarMask != nil && i < len(plane.StarMask) && plane.StarMask[i]) || !inBackgroundROI(x, y, plane.ROI, plane.Width, plane.Height) {
			continue
		}
		v := float64(raw)
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		samples = append(samples, sample{index: i, value: v})
	}
	result := BackgroundEstimate{Samples: len(samples), Status: models.CalibrationUnsupported}
	if len(samples) < d.MinSamples {
		result.RejectionReason = fmt.Sprintf("insufficient samples: %d < %d", len(samples), d.MinSamples)
		return result, nil
	}
	values := make([]float64, len(samples))
	for i, s := range samples {
		values[i] = s.value
	}
	median := calibrationMedianFloat64(ctx, values)
	if ctx.Err() != nil {
		return BackgroundEstimate{Status: models.CalibrationCancelled, RejectionReason: "cancelled"}, ctx.Err()
	}
	for iter := 0; iter < d.MaxIterations; iter++ {
		mad := calibrationMedianAbs(ctx, values, median)
		if ctx.Err() != nil {
			return BackgroundEstimate{Status: models.CalibrationCancelled, RejectionReason: "cancelled"}, ctx.Err()
		}
		sigma := 1.4826 * mad
		if sigma == 0 {
			keepSamples := samples[:0]
			for i, v := range values {
				if i%1024 == 0 {
					select {
					case <-ctx.Done():
						return BackgroundEstimate{Status: models.CalibrationCancelled, RejectionReason: "cancelled"}, ctx.Err()
					default:
					}
				}
				if v == median {
					keepSamples = append(keepSamples, samples[i])
				}
			}
			if len(keepSamples) == len(values) {
				break
			}
			samples = keepSamples
			values = make([]float64, len(samples))
			for i, s := range samples {
				values[i] = s.value
			}
			if len(values) < d.MinSamples {
				result.RejectionReason = "zero-dispersion clipping left insufficient samples"
				result.Rejected = result.Samples - len(values)
				return result, nil
			}
			median = calibrationMedianFloat64(ctx, values)
			continue
		}
		keepSamples := samples[:0]
		for i, v := range values {
			if i%1024 == 0 {
				select {
				case <-ctx.Done():
					return BackgroundEstimate{Status: models.CalibrationCancelled, RejectionReason: "cancelled"}, ctx.Err()
				default:
				}
			}
			if math.Abs(v-median) <= d.SigmaClip*sigma {
				keepSamples = append(keepSamples, samples[i])
			}
		}
		if len(keepSamples) == len(values) {
			break
		}
		samples = keepSamples
		values = make([]float64, len(samples))
		for i, s := range samples {
			values[i] = s.value
		}
		median = calibrationMedianFloat64(ctx, values)
		if len(values) < d.MinSamples {
			result.RejectionReason = "sigma clipping left insufficient samples"
			result.Rejected = result.Samples - len(values)
			return result, nil
		}
	}
	result.Accepted, result.Rejected = len(values), result.Samples-len(values)
	result.Dispersion = 1.4826 * calibrationMedianAbs(ctx, values, median)
	accepted := make(map[int]struct{}, len(samples))
	for _, s := range samples {
		accepted[s.index] = struct{}{}
	}
	spread, tiles := backgroundTileSpreadAccepted(plane, d, accepted, ctx)
	if spread < 0 {
		result.Status = models.CalibrationCancelled
		result.RejectionReason = "cancelled"
		return result, ctx.Err()
	}
	result.TileSpread, result.TileCount = spread, tiles
	scale := math.Max(math.Abs(median), result.Dispersion)
	if scale < 1e-12 {
		scale = 1
	}
	if tiles < 2 || spread/scale > d.TileUniformityThreshold {
		result.RejectionReason = "background is spatially non-uniform"
		return result, nil
	}
	result.Transform = models.LinearTransform{Offset: median, Gain: 1}
	result.Status = models.CalibrationValid
	return result, nil
}

func inBackgroundROI(x, y int, roi *models.CalibrationROI, w, h int) bool {
	if roi == nil {
		return true
	}
	return roi.Width > 0 && roi.Height > 0 && x >= roi.X && y >= roi.Y && x < roi.X+roi.Width && y < roi.Y+roi.Height && x < w && y < h
}

func calibrationMedianFloat64(ctx context.Context, v []float64) float64 {
	select {
	case <-ctx.Done():
		return 0
	default:
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	select {
	case <-ctx.Done():
		return 0
	default:
	}
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}
func calibrationMedianAbs(ctx context.Context, v []float64, m float64) float64 {
	a := make([]float64, len(v))
	for i, x := range v {
		a[i] = math.Abs(x - m)
	}
	return calibrationMedianFloat64(ctx, a)
}

func backgroundTileSpreadAccepted(p BackgroundPlane, c BackgroundConfig, accepted map[int]struct{}, ctx context.Context) (float64, int) {
	mins := []float64{}
	ts := c.TileSize
	for ty := 0; ty < p.Height; ty += ts {
		for tx := 0; tx < p.Width; tx += ts {
			vals := []float64{}
			for y := ty; y < ty+ts && y < p.Height; y++ {
				for x := tx; x < tx+ts && x < p.Width; x++ {
					i := y*p.Width + x
					if _, ok := accepted[i]; !ok {
						continue
					}
					v := float64(p.Pixels[i])
					if math.IsNaN(v) || math.IsInf(v, 0) {
						continue
					}
					vals = append(vals, v)
				}
			}
			if len(vals) > 0 {
				mins = append(mins, calibrationMedianFloat64(ctx, vals))
			}
			select {
			case <-ctx.Done():
				return -1, len(mins)
			default:
			}
		}
	}
	if len(mins) < 2 {
		return 0, len(mins)
	}
	sort.Float64s(mins)
	return mins[len(mins)-1] - mins[0], len(mins)
}

// CalibrationResult is an immutable snapshot of a completed calculation. It
// contains no pixel buffers; callers pass the transforms into rendering.
type CalibrationResult struct {
	Base                [3]models.LinearTransform
	Overlays            []models.OverlayCalibrationState
	Status              models.CalibrationStatus
	Diagnostics         models.CalibrationDiagnostics
	Provenance          models.CalibrationProvenance
	SourceFingerprint   string
	SettingsFingerprint string
}

// CalibrationInput is the immutable identity and data snapshot used for a
// calculation. Pixels are hashed by their exact float32 bit patterns.
type CalibrationInput struct {
	SourceIdentity string
	Width, Height  int
	Pixels         []float32
	Valid          []bool
	StarMask       []bool
	Alignment      string
	Metadata       InstrumentMetadata
	Background     models.LinearTransform
	Photometry     *InstrumentPhotometry
}

// CalibrationSettings contains only behavior-affecting calculation options.
// UI-only state must not be added here because it would change fingerprints.
type CalibrationSettings struct {
	PhotometricMode      models.PhotometricMode
	NeutralizeBackground bool
	BackgroundSelection  models.BackgroundSelection
	BackgroundROI        models.CalibrationROI
	WhiteReference       models.WhiteReference
	LinkedStretch        models.CalibrationStretchSettings
	Overlays             []models.OverlayCalibrationState
	AlgorithmVersion     string
	ReferenceVersion     string
	RuntimeRevision      string
	// Gaia is included in the canonical settings fingerprint whenever Gaia
	// mode is selected. CachePath and CacheMaxBytes are intentionally omitted
	// by callers when they should not affect rendered output.
	Gaia models.GaiaCalibrationSettings
}

// NormalizeCalibrationGains removes arbitrary global luminance scale while
// preserving channel ratios. Disabled (non-positive) gains are left alone.
func NormalizeCalibrationGains(gains [3]float64) ([3]float64, error) {
	var out [3]float64
	product := 1.0
	count := 0
	for _, g := range gains {
		if math.IsNaN(g) || math.IsInf(g, 0) || g < 0 {
			return out, fmt.Errorf("invalid gain %v", g)
		}
		if g > 0 {
			product *= g
			count++
		}
	}
	if count == 0 {
		return gains, nil
	}
	mean := math.Pow(product, 1/float64(count))
	if !finitePositive(mean) {
		return out, fmt.Errorf("invalid gain geometric mean")
	}
	for i, g := range gains {
		if g > 0 {
			out[i] = g / mean
		}
	}
	return out, nil
}

// CalculateCalibration computes transforms and fingerprints without changing
// any input pixels. Background offsets are applied before photometric gains.
func CalculateCalibration(inputs []CalibrationInput, settings CalibrationSettings) (CalibrationResult, error) {
	if len(inputs) > 3 {
		return CalibrationResult{Status: models.CalibrationUnsupported}, fmt.Errorf("at most three base calibration channels are supported")
	}
	if err := validateCalibrationFingerprintInputs(inputs, settings); err != nil {
		return CalibrationResult{Status: models.CalibrationUnsupported}, err
	}
	if len(inputs) == 0 {
		return CalibrationResult{Status: models.CalibrationUnsupported}, fmt.Errorf("no calibration inputs")
	}
	var result CalibrationResult
	var raw [3]float64
	var offsets [3]float64
	for i := range result.Base {
		result.Base[i] = models.LinearTransform{Gain: 1}
	}
	for i := 0; i < len(inputs) && i < len(raw); i++ {
		in := inputs[i]
		if in.Width < 0 || in.Height < 0 || len(in.Pixels) < in.Width*in.Height {
			return CalibrationResult{Status: models.CalibrationUnsupported}, fmt.Errorf("invalid channel dimensions")
		}
		gain := 1.0
		if in.Photometry != nil {
			gain = in.Photometry.Gain
		}
		offset := 0.0
		if settings.NeutralizeBackground && in.Background.Gain != 0 {
			// Background estimate uses Gain=1; retain its offset and validate it.
			if math.IsNaN(in.Background.Offset) || math.IsInf(in.Background.Offset, 0) {
				return result, fmt.Errorf("invalid background offset")
			}
			offset = in.Background.Offset
		} else {
			offset = 0
		}
		if !finitePositive(gain) {
			return result, fmt.Errorf("invalid photometric gain")
		}
		raw[i] = gain
		offsets[i] = offset
	}
	normalized, err := NormalizeCalibrationGains(raw)
	if err != nil {
		return result, err
	}
	for i := range result.Base {
		if i < len(inputs) {
			gain := normalized[i]
			if gain > 0 {
				result.Base[i] = models.LinearTransform{Offset: offsets[i], Gain: gain}
			}
		}
	}
	result.Status = models.CalibrationValid
	result.SourceFingerprint, result.SettingsFingerprint = CalibrationFingerprints(inputs, settings)
	result.Provenance = models.CalibrationProvenance{AlgorithmVersion: settings.AlgorithmVersion, ReferenceVersion: settings.ReferenceVersion, Source: "local"}
	return result, nil
}

// CalibrationFingerprints returns stable source and settings SHA-256 hashes.
func CalibrationFingerprints(inputs []CalibrationInput, settings CalibrationSettings) (string, string) {
	var source canonicalHash
	source.stringValue("color-calibration-source-v1")
	for _, in := range inputs {
		source.input(in, settings.NeutralizeBackground)
	}
	var config canonicalHash
	config.stringValue("color-calibration-settings-v1")
	config.jsonValue(settings)
	return source.sum(), config.sum()
}

func validateCalibrationFingerprintInputs(inputs []CalibrationInput, settings CalibrationSettings) error {
	finite := func(name string, v float64) error {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("non-finite %s", name)
		}
		return nil
	}
	for i, in := range inputs {
		if err := finite(fmt.Sprintf("channel %d background offset", i), in.Background.Offset); err != nil {
			return err
		}
		if err := finite(fmt.Sprintf("channel %d background gain", i), in.Background.Gain); err != nil {
			return err
		}
		if in.Photometry != nil {
			if err := finite(fmt.Sprintf("channel %d photometric gain", i), in.Photometry.Gain); err != nil {
				return err
			}
			if err := finite(fmt.Sprintf("channel %d exposure", i), in.Photometry.ExposureSeconds); err != nil {
				return err
			}
			if err := finite(fmt.Sprintf("channel %d pivot", i), in.Photometry.PivotWavelengthAngstrom); err != nil {
				return err
			}
		}
	}
	for _, item := range []struct {
		name  string
		value float64
	}{
		{"stretch black", settings.LinkedStretch.Black}, {"stretch white", settings.LinkedStretch.White}, {"stretch midtone", settings.LinkedStretch.Midtone},
	} {
		name, value := item.name, item.value
		if err := finite(name, value); err != nil {
			return err
		}
	}
	for i, overlay := range settings.Overlays {
		if err := finite(fmt.Sprintf("overlay %d offset", i), overlay.Transform.Offset); err != nil {
			return err
		}
		if err := finite(fmt.Sprintf("overlay %d gain", i), overlay.Transform.Gain); err != nil {
			return err
		}
		if err := finite(fmt.Sprintf("overlay %d strength", i), overlay.Strength); err != nil {
			return err
		}
	}
	return nil
}

// CalibrationResultStale compares persisted fingerprints with current inputs.
func CalibrationResultStale(result CalibrationResult, inputs []CalibrationInput, settings CalibrationSettings) bool {
	if result.Status != models.CalibrationValid {
		return true
	}
	source, config := CalibrationFingerprints(inputs, settings)
	return source != result.SourceFingerprint || config != result.SettingsFingerprint
}

// IsCalibrationStale is a descriptive alias for CalibrationResultStale.
func IsCalibrationStale(result CalibrationResult, inputs []CalibrationInput, settings CalibrationSettings) bool {
	return CalibrationResultStale(result, inputs, settings)
}

type canonicalHash struct{ h hashWriter }
type hashWriter struct{ b strings.Builder }

func (c *canonicalHash) stringValue(v string) { c.h.b.WriteString(fmt.Sprintf("s:%d:%s;", len(v), v)) }
func (c *canonicalHash) bytesValue(v []byte) {
	c.h.b.WriteString(fmt.Sprintf("b:%d:", len(v)))
	c.h.b.Write(v)
	c.h.b.WriteByte(';')
}
func (c *canonicalHash) jsonValue(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		c.stringValue("json-marshal-error:" + err.Error())
		return
	}
	c.bytesValue(b)
}
func (c *canonicalHash) sum() string {
	s := sha256.Sum256([]byte(c.h.b.String()))
	return hex.EncodeToString(s[:])
}
func (c *canonicalHash) input(in CalibrationInput, includeBackground bool) {
	c.stringValue(in.SourceIdentity)
	c.h.b.WriteString(fmt.Sprintf("d:%d:%d;", in.Width, in.Height))
	c.stringValue(in.Alignment)
	for _, p := range in.Pixels {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], math.Float32bits(p))
		c.bytesValue(b[:])
	}
	c.h.b.WriteString(fmt.Sprintf("valid-mask:%d;", len(in.Valid)))
	for _, v := range in.Valid {
		if v {
			c.stringValue("1")
		} else {
			c.stringValue("0")
		}
	}
	c.h.b.WriteString(fmt.Sprintf("star-mask:%d;", len(in.StarMask)))
	for _, v := range in.StarMask {
		if v {
			c.stringValue("1")
		} else {
			c.stringValue("0")
		}
	}
	// Header maps are encoded by encoding/json with sorted keys.
	c.jsonValue(in.Metadata)
	if includeBackground {
		c.jsonValue(in.Background)
	}
	if in.Photometry != nil {
		c.jsonValue(*in.Photometry)
	} else {
		c.stringValue("no-photometry")
	}
}

// ValidateLinearTransform rejects values that could produce non-deterministic
// or non-finite calibrated output.
func ValidateLinearTransform(t models.LinearTransform) error {
	if !t.Valid() {
		return fmt.Errorf("invalid linear transform: offset=%v gain=%v", t.Offset, t.Gain)
	}
	return nil
}

// ApplyLinearTransform applies offset-before-gain without changing the input.
func ApplyLinearTransform(value float32, t models.LinearTransform) (float32, error) {
	if err := ValidateLinearTransform(t); err != nil {
		return 0, err
	}
	v := (float64(value) - t.Offset) * t.Gain
	if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > math.MaxFloat32 {
		return 0, fmt.Errorf("linear transform produced non-finite value")
	}
	return float32(v), nil
}

// CopyCalibrationResult returns a detached snapshot suitable for publication
// across a calculation generation. Subsequent caller mutations cannot alter
// the original result.
func CopyCalibrationResult(in CalibrationResult) CalibrationResult {
	out := in
	out.Overlays = append([]models.OverlayCalibrationState(nil), in.Overlays...)
	out.Diagnostics.Warnings = append([]string(nil), in.Diagnostics.Warnings...)
	out.Provenance.PassbandVersions = append([]string(nil), in.Provenance.PassbandVersions...)
	out.Provenance.SourceIDs = append([]uint64(nil), in.Provenance.SourceIDs...)
	for i := range out.Overlays {
		out.Overlays[i].Diagnostics.Warnings = append([]string(nil), in.Overlays[i].Diagnostics.Warnings...)
		out.Overlays[i].Provenance.PassbandVersions = append([]string(nil), in.Overlays[i].Provenance.PassbandVersions...)
		out.Overlays[i].Provenance.SourceIDs = append([]uint64(nil), in.Overlays[i].Provenance.SourceIDs...)
	}
	return out
}

// EffectiveOverlay applies the compatibility default for old project files.
func EffectiveOverlay(in models.OverlayCalibrationState) models.OverlayCalibrationState {
	out := in
	if out.Mode == "" {
		out.Mode = models.OverlayArtistic
	}
	if out.Status == "" {
		out.Status = models.CalibrationDisabled
	}
	return out
}
