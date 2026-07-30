package ui

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"

	"gofitsv3/internal/catalog/gaia"
	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
)

func TestAlignedArtifactReadWindowSamplesOnlyRequestedSourceRange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.rawf32")
	a, err := fitsio.CreateFloat32Artifact(path, 100, 4)
	if err != nil {
		t.Fatal(err)
	}
	for y := 0; y < 4; y++ {
		row := make([]float32, 100)
		for x := range row {
			row[x] = float32(y*100 + x)
		}
		if err := a.WriteRow(y, row); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	inst := &fitsio.ArtifactInstrumentation{}
	fitsio.SetArtifactInstrumentation(inst)
	defer fitsio.SetArtifactInstrumentation(nil)
	r := &alignedArtifactRowReader{path: path, source: models.LoadedImage{}, reference: models.LoadedImage{}, width: 100, height: 4}
	defer r.Close()
	dst := make([]float32, 3)
	if err := r.ReadWindow(1, 20, 23, dst); err != nil {
		t.Fatal(err)
	}
	if got := inst.LargestBuffer.Load(); got >= 100*4 {
		t.Fatalf("window read used full source row buffer: %d bytes", got)
	}
	if math.IsNaN(float64(dst[0])) {
		t.Fatal("valid window returned NaN")
	}
}

func TestGaiaMatchRadiusParsingAndNormalization(t *testing.T) {
	if got := normalizeGaiaMatchRadiusArcsec(0); got != 10 {
		t.Fatalf("unset radius = %v, want 10", got)
	}
	for _, radius := range []float64{-1, math.NaN(), math.Inf(1)} {
		got := normalizeGaiaMatchRadiusArcsec(radius)
		if (math.IsNaN(radius) && !math.IsNaN(got)) || (!math.IsNaN(radius) && got != radius) {
			t.Errorf("invalid persisted radius %v was normalized to %v", radius, got)
		}
	}
	for _, raw := range []string{"", "0", "-1", "NaN", "+Inf", "not-a-number"} {
		if _, err := parseGaiaMatchRadiusArcsec(raw); err == nil {
			t.Errorf("parseGaiaMatchRadiusArcsec(%q) unexpectedly succeeded", raw)
		}
	}
	for _, tc := range []struct {
		raw  string
		want float64
	}{
		{raw: " 2.5 ", want: 2.5},
		{raw: "1e-3", want: 1e-3},
	} {
		got, err := parseGaiaMatchRadiusArcsec(tc.raw)
		if err != nil || got != tc.want {
			t.Errorf("parseGaiaMatchRadiusArcsec(%q) = %v, %v; want %v", tc.raw, got, err, tc.want)
		}
	}
}

func TestGaiaRequestSettingsPreservesMatchRadius(t *testing.T) {
	settings := gaiaRequestSettings(models.GaiaCalibrationSettings{Release: "DR3", XPRepresentation: "xp-v1"}, 2.75, 2024, 18)
	if settings.MatchRadiusArcsec != 2.75 {
		t.Fatalf("request match radius = %v, want 2.75", settings.MatchRadiusArcsec)
	}
}

type uiGaiaProvider struct{}

func (uiGaiaProvider) Mode() gaia.AccessMode { return gaia.AccessCacheOnly }
func (uiGaiaProvider) Provenance(context.Context) (gaia.Provenance, error) {
	return gaia.Provenance{Release: "DR3", XPRepresentation: "xp-v1", Provider: "test"}, nil
}
func (uiGaiaProvider) DiscoverSources(context.Context, gaia.FieldQuery) ([]gaia.Source, error) {
	return nil, nil
}
func (uiGaiaProvider) RetrieveXPSpectra(context.Context, string, string, []uint64) ([]gaia.XPSpectrum, error) {
	return nil, nil
}

func TestGaiaJobServiceReportsStagesAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stages := []GaiaJobStage{}
	_, err := (defaultGaiaJobService{}).Run(ctx, GaiaJobRequest{Provider: uiGaiaProvider{}}, func(p GaiaJobProgress) { stages = append(stages, p.Stage); cancel() })
	if err == nil {
		t.Fatal("expected cancellation")
	}
	if len(stages) != 1 || stages[0] != GaiaStageQuery {
		t.Fatalf("stages=%v, want query then cancellation", stages)
	}
}

func TestClearGaiaCacheRemovesSQLiteSidecars(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gaia.db")
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.WriteFile(p, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := clearGaiaCache(path); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s remains: %v", p, err)
		}
	}
}

func TestDeriveGaiaFieldQueryUsesDateObsEpochAndRotation(t *testing.T) {
	img := &models.LoadedImage{HDU: fitsio.HDU{Header: fitsio.Header{Cards: map[string]string{
		"CRVAL1": "10", "CRVAL2": "20", "CRPIX1": "1", "CRPIX2": "1",
		"CD1_1": "1", "CD1_2": "0.5", "CD2_1": "-0.5", "CD2_2": "1", "DATE-OBS": "2024-01-01T00:00:00Z",
	}}, Data: fitsio.ImageData{Width: 2, Height: 2}}}
	q, proj, err := deriveGaiaFieldQuery(img, models.GaiaCalibrationSettings{Release: "DR3", MagnitudeLimit: 18})
	if err != nil {
		t.Fatal(err)
	}
	if q.ObservationEpoch < 2024 || q.ObservationEpoch >= 2025 {
		t.Fatalf("epoch=%v", q.ObservationEpoch)
	}
	got, err := proj(2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.RA == 10 {
		t.Fatal("rotation matrix was ignored")
	}
}

func TestDeriveGaiaFieldQueryUsesOneBasedWCSProjection(t *testing.T) {
	img := &models.LoadedImage{HDU: fitsio.HDU{Header: fitsio.Header{Cards: map[string]string{
		"CRVAL1": "10", "CRVAL2": "20", "CRPIX1": "1", "CRPIX2": "1",
		"CD1_1": "1", "CD1_2": "0.5", "CD2_1": "-0.5", "CD2_2": "1", "DATE-OBS": "2024-01-01",
	}}, Data: fitsio.ImageData{Width: 2, Height: 2}}}
	_, proj, err := deriveGaiaFieldQuery(img, models.GaiaCalibrationSettings{Release: "DR3"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := proj(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.RA != 10 || got.Dec != 20 {
		t.Fatalf("projection at FITS reference pixel = %+v, want RA=10 Dec=20", got)
	}
	got, err = proj(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.RA != 11 || got.Dec != 19.5 {
		t.Fatalf("projection at next x pixel = %+v, want RA=11 Dec=19.5", got)
	}
}

func TestDeriveGaiaFieldQueryUsesTANProjectionAndGeometricFootprint(t *testing.T) {
	img := &models.LoadedImage{HDU: fitsio.HDU{Header: fitsio.Header{Cards: map[string]string{
		"CRVAL1": "116.73101182695", "CRVAL2": "39.025082290628", "CRPIX1": "2028.354937", "CRPIX2": "1226.439365",
		"CTYPE1": "RA---TAN", "CTYPE2": "DEC--TAN", "CD1_1": "4.79342542333e-06", "CD1_2": "-1.2741944547e-05", "CD2_1": "-1.29854994213e-05", "CD2_2": "-5.73504303172e-06", "DATE-OBS": "2020-11-20",
	}}, Data: fitsio.ImageData{Width: 4122, Height: 4298}}}
	q, proj, err := deriveGaiaFieldQuery(img, models.GaiaCalibrationSettings{Release: "DR3"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := proj(100, 201)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(got.RA-116.73592433978472) > 1e-10 || math.Abs(got.Dec-39.05598505485143) > 1e-10 {
		t.Fatalf("TAN projection = %+v, want independent inverse-TAN result", got)
	}
	center, err := proj(float64(img.HDU.Data.Width-1)/2, float64(img.HDU.Data.Height-1)/2)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(q.Footprint.Center.RA-center.RA) > 1e-12 || math.Abs(q.Footprint.Center.Dec-center.Dec) > 1e-12 {
		t.Fatalf("footprint center=%+v, want geometric center=%+v", q.Footprint.Center, center)
	}
	if math.Abs(q.Footprint.Center.RA-116.71607784379417) > 1e-10 || math.Abs(q.Footprint.Center.Dec-39.01935714099151) > 1e-10 || q.Footprint.RadiusDeg < 0.04 || q.Footprint.RadiusDeg > 0.1 {
		t.Fatalf("footprint=%+v, want center near independent TAN result and radius in expected range", q.Footprint)
	}
	for _, corner := range [][2]float64{{0, 0}, {float64(img.HDU.Data.Width - 1), 0}, {0, float64(img.HDU.Data.Height - 1)}, {float64(img.HDU.Data.Width - 1), float64(img.HDU.Data.Height - 1)}} {
		p, err := proj(corner[0], corner[1])
		if err != nil {
			t.Fatal(err)
		}
		if angularSeparationDeg(center, p) > q.Footprint.RadiusDeg+1e-12 {
			t.Fatalf("corner %+v outside radius %.9f", corner, q.Footprint.RadiusDeg)
		}
	}
}

func TestDeriveGaiaFieldProjectionRoundTripsTAN(t *testing.T) {
	img := &models.LoadedImage{HDU: fitsio.HDU{Header: fitsio.Header{Cards: map[string]string{
		"CRVAL1": "116.73101182695", "CRVAL2": "39.025082290628", "CRPIX1": "2028.354937", "CRPIX2": "1226.439365",
		"CTYPE1": "RA---TAN", "CTYPE2": "DEC--TAN", "CD1_1": "4.79342542333e-06", "CD1_2": "-1.2741944547e-05", "CD2_1": "-1.29854994213e-05", "CD2_2": "-5.73504303172e-06", "DATE-OBS": "2020-11-20",
	}}, Data: fitsio.ImageData{Width: 4122, Height: 4298}}}
	_, toSky, toPixel, err := deriveGaiaFieldProjection(img, models.GaiaCalibrationSettings{Release: "DR3"})
	if err != nil {
		t.Fatal(err)
	}
	x, y := 100.0, 201.0
	c, err := toSky(x, y)
	if err != nil {
		t.Fatal(err)
	}
	rx, ry, err := toPixel(c)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(rx-x) > 1e-6 || math.Abs(ry-y) > 1e-6 {
		t.Fatalf("round trip=(%v,%v), want (%v,%v)", rx, ry, x, y)
	}
}

func TestDeriveGaiaFieldQueryAcceptsRotatedCD(t *testing.T) {
	img := &models.LoadedImage{HDU: fitsio.HDU{Header: fitsio.Header{Cards: map[string]string{
		"CRVAL1": "10", "CRVAL2": "20", "CRPIX1": "1", "CRPIX2": "1",
		"CD1_1": "0", "CD1_2": "0.001", "CD2_1": "-0.001", "CD2_2": "0", "DATE-OBS": "2024-01-01",
	}}, Data: fitsio.ImageData{Width: 10, Height: 10}}}
	_, proj, err := deriveGaiaFieldQuery(img, models.GaiaCalibrationSettings{Release: "DR3"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := proj(1, 0)
	if err != nil || math.Abs(got.RA-10) > 1e-12 || math.Abs(got.Dec-19.999) > 1e-12 {
		t.Fatalf("rotated projection=%+v err=%v", got, err)
	}
}

func TestResolveGaiaSettingsDefaultsPersistableValues(t *testing.T) {
	got := resolveGaiaSettings(models.GaiaCalibrationSettings{AccessMode: "online"})
	if got.Release != "DR3" || got.XPRepresentation != "xp-v1" || got.Endpoint == "" || got.ObservationEpoch != 0 {
		t.Fatalf("resolved defaults = %+v", got)
	}
	cache := resolveGaiaSettings(models.GaiaCalibrationSettings{AccessMode: "cacheOnly"})
	if cache.Endpoint != gaia.DefaultESATAPEndpoint {
		t.Fatalf("cache endpoint = %q, want %q", cache.Endpoint, gaia.DefaultESATAPEndpoint)
	}
}

func TestApplyGaiaCachePathPreferencePrecedenceAndClear(t *testing.T) {
	project := models.GaiaCalibrationSettings{}
	resolved := applyGaiaCachePathPreference(project, "A.db")
	if project.CachePath != "" || resolved.CachePath != "A.db" {
		t.Fatalf("preference fallback mutated project state: project=%q resolved=%q", project.CachePath, resolved.CachePath)
	}
	if got := applyGaiaCachePathPreference(models.GaiaCalibrationSettings{CachePath: "project.db"}, "pref.db").CachePath; got != "project.db" {
		t.Fatalf("project path did not override preference: %q", got)
	}
	if got := applyGaiaCachePathPreference(models.GaiaCalibrationSettings{}, "pref.db").CachePath; got != "pref.db" {
		t.Fatalf("blank project path did not use preference: %q", got)
	}
	if got := applyGaiaCachePathPreference(models.GaiaCalibrationSettings{}, "").CachePath; got != "" {
		t.Fatalf("cleared preference did not restore default path: %q", got)
	}
}

func TestNewGaiaProviderSelectsNativeESAForOfficialEndpoint(t *testing.T) {
	for _, endpoint := range []string{"  " + gaia.DefaultESATAPEndpoint + "///  ", "https://gea.esac.esa.int/tap-server/tap/?x=1"} {
		for _, mode := range []gaia.AccessMode{gaia.AccessOnline, gaia.AccessCacheOnly} {
			provider, err := newGaiaProvider(endpoint, mode, "DR3", "xp-v1", nil)
			if err != nil {
				t.Fatalf("endpoint %q mode %s: %v", endpoint, mode, err)
			}
			provenance, err := provider.Provenance(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if provenance.Provider != "gaia-esa-tap" || provenance.EndpointSemantics != gaia.DefaultESATAPEndpoint {
				t.Fatalf("endpoint %q mode %s provenance = %+v", endpoint, mode, provenance)
			}
		}
	}
}

func TestNewGaiaProviderSelectsRemoteForCustomEndpoint(t *testing.T) {
	provider, err := newGaiaProvider("https://example.invalid/gaia-json///", gaia.AccessOnline, "DR3", "xp-v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	provenance, err := provider.Provenance(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if provenance.Provider != "gaia-remote" || provenance.EndpointSemantics != "https://example.invalid/gaia-json" {
		t.Fatalf("provenance = %+v", provenance)
	}
}

func TestGaiaJobServiceRequiresProviderContract(t *testing.T) {
	_, err := (defaultGaiaJobService{}).Run(context.Background(), GaiaJobRequest{Provider: uiGaiaProvider{}, Calibration: processing.GaiaCalibrationRequest{}}, nil)
	if err == nil {
		t.Fatal("expected settings/provider validation error")
	}
}
