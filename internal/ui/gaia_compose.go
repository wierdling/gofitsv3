package ui

// Gaia orchestration lives in this file rather than in the widget callback so
// it can be exercised with an in-memory provider.  The service deliberately
// reports coarse stages; providers remain responsible for cancellation.
import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gofitsv3/internal/catalog/gaia"
	"gofitsv3/internal/debuglog"
	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
)

const defaultGaiaMatchRadiusArcsec = 2

func normalizeGaiaMatchRadiusArcsec(radius float64) float64 {
	if radius == 0 {
		return defaultGaiaMatchRadiusArcsec
	}
	return radius
}

func parseGaiaMatchRadiusArcsec(raw string) (float64, error) {
	radius, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || radius <= 0 || math.IsNaN(radius) || math.IsInf(radius, 0) {
		return 0, fmt.Errorf("Gaia match radius must be a positive finite number")
	}
	return radius, nil
}

func gaiaRequestSettings(settings models.GaiaCalibrationSettings, matchRadius, epoch, magnitude float64) processing.GaiaCalibrationSettings {
	return processing.GaiaCalibrationSettings{
		Release:           settings.Release,
		XPRepresentation:  settings.XPRepresentation,
		Passbands:         []string{"G", "BP", "RP"},
		AlgorithmVersion:  "ui-v1",
		QualitySelector:   settings.QualitySelector,
		MagnitudeLimit:    magnitude,
		MatchRadiusArcsec: matchRadius,
		ObservationEpoch:  epoch,
	}
}

type GaiaJobStage string

const (
	GaiaStageDetect     GaiaJobStage = "detect"
	GaiaStageQuery      GaiaJobStage = "query"
	GaiaStageSpectra    GaiaJobStage = "fetch spectra"
	GaiaStagePhotometry GaiaJobStage = "photometry"
	GaiaStageFit        GaiaJobStage = "fit"
	GaiaStageSave       GaiaJobStage = "save"
	GaiaStageRender     GaiaJobStage = "render"
)

type GaiaJobProgress struct {
	Stage       GaiaJobStage
	Done, Total int
}

type GaiaJobRequest struct {
	Provider    gaia.Provider
	Query       gaia.FieldQuery
	Settings    processing.GaiaCalibrationSettings
	Calibration processing.GaiaCalibrationRequest
}

type GaiaJobService interface {
	Run(context.Context, GaiaJobRequest, func(GaiaJobProgress)) (processing.GaiaCalibrationResult, error)
}

type defaultGaiaJobService struct{}

// composeGaiaJobService is replaceable by UI tests and embedding applications
// that provide a controlled catalog backend.
var composeGaiaJobService GaiaJobService = defaultGaiaJobService{}

var composeGaiaCacheOpener = gaia.OpenCache

func (defaultGaiaJobService) Run(ctx context.Context, req GaiaJobRequest, progress func(GaiaJobProgress)) (processing.GaiaCalibrationResult, error) {
	debuglog.Log(fmt.Sprintf("Gaia job start: provider=%T release=%s xp=%s radius=%.3f epoch=%.3f detected=%d", req.Provider, req.Settings.Release, req.Settings.XPRepresentation, req.Settings.MatchRadiusArcsec, req.Settings.ObservationEpoch, len(req.Calibration.DetectedStars)))
	if progress == nil {
		progress = func(GaiaJobProgress) {}
	}
	if err := ctx.Err(); err != nil {
		return processing.GaiaCalibrationResult{Status: models.CalibrationCancelled}, err
	}
	// Provider discovery, spectra retrieval, photometry, and fitting occur
	// inside PrepareGaiaCalibration; report the boundary rather than claiming
	// completion for stages that have not run yet.
	progress(GaiaJobProgress{Stage: GaiaStageQuery})
	req.Calibration.Provider, req.Calibration.Query, req.Calibration.Settings = req.Provider, req.Query, req.Settings
	result, err := processing.PrepareGaiaCalibration(ctx, req.Calibration)
	if err != nil {
		debuglog.Log(fmt.Sprintf("Gaia job failed: status=%s error=%v", result.Status, err))
	} else {
		debuglog.Log(fmt.Sprintf("Gaia job fit complete: matched=%d accepted=%d", result.Diagnostics.MatchedStars, result.Diagnostics.AcceptedStars))
	}
	if err == nil {
		progress(GaiaJobProgress{Stage: GaiaStageFit, Done: 1, Total: 1})
	}
	return result, err
}

func deriveGaiaFieldQuery(img *models.LoadedImage, settings models.GaiaCalibrationSettings) (gaia.FieldQuery, func(float64, float64) (gaia.Coordinate, error), error) {
	if img == nil || img.HDU.Data.Width <= 0 || img.HDU.Data.Height <= 0 {
		return gaia.FieldQuery{}, nil, fmt.Errorf("Gaia requires a non-empty image")
	}
	read := func(key string) (float64, bool) {
		f, ok := fitsio.HeaderFloat(img.HDU.Header, key)
		return f, ok && isFinite(f)
	}
	ra, ok1 := read("CRVAL1")
	dec, ok2 := read("CRVAL2")
	if !ok1 || !ok2 {
		return gaia.FieldQuery{}, nil, fmt.Errorf("Gaia requires verified CRVAL WCS metadata")
	}
	var a, b, c, d float64
	if cd11, x1 := read("CD1_1"); x1 {
		var x2, x3, x4 bool
		b, x2 = read("CD1_2")
		c, x3 = read("CD2_1")
		d, x4 = read("CD2_2")
		if !x2 || !x3 || !x4 {
			return gaia.FieldQuery{}, nil, fmt.Errorf("incomplete CD WCS matrix")
		}
		a = cd11
	} else {
		sx, x1 := read("CDELT1")
		sy, x2 := read("CDELT2")
		if !x1 || !x2 {
			return gaia.FieldQuery{}, nil, fmt.Errorf("Gaia requires verified linear WCS metadata")
		}
		p11, p12, p21, p22 := 1.0, 0.0, 0.0, 1.0
		if v, ok := read("PC1_1"); ok {
			p11 = v
		}
		if v, ok := read("PC1_2"); ok {
			p12 = v
		}
		if v, ok := read("PC2_1"); ok {
			p21 = v
		}
		if v, ok := read("PC2_2"); ok {
			p22 = v
		}
		a, b, c, d = p11*sx, p12*sx, p21*sy, p22*sy
	}
	if a == 0 || d == 0 || !isFinite(a) || !isFinite(b) || !isFinite(c) || !isFinite(d) || math.Abs(dec) > 90 {
		return gaia.FieldQuery{}, nil, fmt.Errorf("invalid WCS scale")
	}
	crpix1, okx := read("CRPIX1")
	crpix2, oky := read("CRPIX2")
	if !okx {
		crpix1 = 1
	}
	if !oky {
		crpix2 = 1
	}
	ctype1 := strings.ToUpper(strings.TrimSpace(fitsio.HeaderString(img.HDU.Header, "CTYPE1", "")))
	ctype2 := strings.ToUpper(strings.TrimSpace(fitsio.HeaderString(img.HDU.Header, "CTYPE2", "")))
	isTAN := strings.HasPrefix(ctype1, "RA---TAN") && strings.HasPrefix(ctype2, "DEC--TAN")
	pixelToSky := func(x, y float64) (gaia.Coordinate, error) {
		dx, dy := (x+1)-crpix1, (y+1)-crpix2
		if !isTAN {
			r := math.Mod(ra+dx*a+dy*b+360, 360)
			d := dec + dx*c + dy*d
			if d < -90 || d > 90 {
				return gaia.Coordinate{}, fmt.Errorf("projected coordinate outside sky")
			}
			return gaia.Coordinate{RA: r, Dec: d}, nil
		}
		xi := (dx*a + dy*b) * math.Pi / 180
		eta := (dx*c + dy*d) * math.Pi / 180
		ra0, dec0 := ra*math.Pi/180, dec*math.Pi/180
		rho := math.Hypot(xi, eta)
		if rho == 0 {
			return gaia.Coordinate{RA: math.Mod(ra+360, 360), Dec: dec}, nil
		}
		cc := math.Atan(rho)
		sinC, cosC := math.Sin(cc), math.Cos(cc)
		decRad := math.Asin(cosC*math.Sin(dec0) + eta*sinC*math.Cos(dec0)/rho)
		raRad := ra0 + math.Atan2(xi*sinC, rho*math.Cos(dec0)*cosC-eta*math.Sin(dec0)*sinC)
		raDeg := math.Mod(raRad*180/math.Pi+360, 360)
		return gaia.Coordinate{RA: raDeg, Dec: decRad * 180 / math.Pi}, nil
	}
	centerX, centerY := float64(img.HDU.Data.Width-1)/2, float64(img.HDU.Data.Height-1)/2
	center, err := pixelToSky(centerX, centerY)
	if err != nil {
		return gaia.FieldQuery{}, nil, err
	}
	radius := 0.0
	for _, corner := range [][2]float64{{0, 0}, {float64(img.HDU.Data.Width - 1), 0}, {0, float64(img.HDU.Data.Height - 1)}, {float64(img.HDU.Data.Width - 1), float64(img.HDU.Data.Height - 1)}} {
		p, err := pixelToSky(corner[0], corner[1])
		if err != nil {
			return gaia.FieldQuery{}, nil, err
		}
		dist := angularSeparationDeg(center, p)
		if dist > radius {
			radius = dist
		}
	}
	if radius <= 0 || radius > 20 {
		return gaia.FieldQuery{}, nil, fmt.Errorf("image Gaia footprint is outside supported size")
	}
	epoch := settings.ObservationEpoch
	rawDate := strings.TrimSpace(fitsio.HeaderString(img.HDU.Header, "DATE-OBS", "DATEOBS"))
	if rawDate == "" {
		rawDate = strings.TrimSpace(fitsio.HeaderString(img.Primary, "DATE-OBS", "DATEOBS"))
	}
	if rawDate != "" {
		parsed, err := parseGaiaObservationEpoch(rawDate)
		if err != nil {
			return gaia.FieldQuery{}, nil, err
		}
		epoch = parsed
	} else if epoch == 0 {
		return gaia.FieldQuery{}, nil, fmt.Errorf("Gaia requires DATE-OBS or explicit observation epoch")
	}
	q := gaia.FieldQuery{Footprint: gaia.Footprint{Center: center, RadiusDeg: radius}, Release: settings.Release, ObservationEpoch: epoch, MagnitudeLimit: settings.MagnitudeLimit, QualitySelector: settings.QualitySelector}
	if q.MagnitudeLimit == 0 {
		q.MagnitudeLimit = 18
	}
	if err := q.Validate(); err != nil {
		return gaia.FieldQuery{}, nil, err
	}
	debuglog.Log(fmt.Sprintf("Gaia WCS setup: center_ra=%.7f center_dec=%.7f footprint=%.5f deg crpix=[%.3f %.3f] size=%dx%d tan=%t scale=[%.6g %.6g %.6g %.6g] epoch=%.3f", q.Footprint.Center.RA, q.Footprint.Center.Dec, q.Footprint.RadiusDeg, crpix1, crpix2, img.HDU.Data.Width, img.HDU.Data.Height, isTAN, a, b, c, d, epoch))
	return q, pixelToSky, nil
}

func angularSeparationDeg(a, b gaia.Coordinate) float64 {
	ra1, ra2 := a.RA*math.Pi/180, b.RA*math.Pi/180
	dec1, dec2 := a.Dec*math.Pi/180, b.Dec*math.Pi/180
	s := math.Sin((dec2-dec1)/2)*math.Sin((dec2-dec1)/2) + math.Cos(dec1)*math.Cos(dec2)*math.Sin((ra2-ra1)/2)*math.Sin((ra2-ra1)/2)
	return 2 * math.Asin(math.Sqrt(math.Min(1, math.Max(0, s)))) * 180 / math.Pi
}

func parseGaiaObservationEpoch(raw string) (float64, error) {
	raw = strings.TrimSpace(strings.Trim(raw, "'"))
	for _, layout := range []string{time.RFC3339, "2006-01-02", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return float64(t.Year()) + float64(t.YearDay()-1)/365.2425, nil
		}
	}
	return 0, fmt.Errorf("unsupported DATE-OBS %q", raw)
}

func isFinite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func gaiaCachePath(settings models.GaiaCalibrationSettings, appData string) (string, error) {
	if settings.CachePath != "" {
		return filepath.Abs(settings.CachePath)
	}
	return gaia.DefaultCachePath(appData)
}

const gaiaCachePathPreferenceKey = "compose.gaiaCachePath"

func applyGaiaCachePathPreference(settings models.GaiaCalibrationSettings, preference string) models.GaiaCalibrationSettings {
	if settings.CachePath == "" {
		settings.CachePath = preference
	}
	return settings
}

func resolveGaiaSettings(settings models.GaiaCalibrationSettings) models.GaiaCalibrationSettings {
	if settings.Release == "" {
		settings.Release = "DR3"
	}
	if settings.XPRepresentation == "" {
		settings.XPRepresentation = "xp-v1"
	}
	if settings.Endpoint == "" {
		settings.Endpoint = gaia.DefaultESATAPEndpoint
	}
	return settings
}

func newGaiaProvider(endpoint string, mode gaia.AccessMode, release, xpRepresentation string, cache *gaia.Cache) (gaia.Provider, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		endpoint = gaia.DefaultESATAPEndpoint
	}
	if gaia.IsDefaultESATAPEndpoint(endpoint) {
		return gaia.NewESAProvider(gaia.ESAConfig{Endpoint: gaia.DefaultESATAPEndpoint, Release: release, XPRepresentation: xpRepresentation, Mode: mode, Cache: cache})
	}
	return gaia.NewRemoteProvider(gaia.RemoteConfig{Endpoint: strings.TrimRight(endpoint, "/"), Release: release, XPRepresentation: xpRepresentation, Mode: mode, Cache: cache})
}

func gaiaCacheStatus(path string) (exists bool, bytes int64, err error) {
	st, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, err
	}
	return true, st.Size(), nil
}
func clearGaiaCache(path string) error {
	if path == "" {
		return fmt.Errorf("Gaia cache path is required")
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
