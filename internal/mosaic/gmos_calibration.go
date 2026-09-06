package mosaic

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"gofitsv3/internal/fitsio"
)

type GMOSFrameKind string

const (
	GMOSScience     GMOSFrameKind = "science"
	GMOSBias        GMOSFrameKind = "bias"
	GMOSTwilight    GMOSFrameKind = "twilight-flat"
	GMOSBPM         GMOSFrameKind = "bpm"
	GMOSUnsupported GMOSFrameKind = "unsupported"
)

type GMOSCalibrationFrame struct {
	Path         string
	Kind         GMOSFrameKind
	Filter       string
	Date         string
	Key          string
	ExposureTime float64
	Anomalous    bool
}
type GMOSCalibrationManifest struct{ Science, Bias, Twilight, BPM []GMOSCalibrationFrame }
type GMOSCalibrationSelection struct {
	Bias               *GMOSCalibrationFrame
	Flat               *GMOSCalibrationFrame
	BPM                *GMOSCalibrationFrame
	Warning            string
	BiasFrames         []GMOSCalibrationFrame
	FlatFrames         []GMOSCalibrationFrame
	BPMFrames          []GMOSCalibrationFrame
	AllowAnomalousBias bool
	RecipeFingerprint  string
}

type GMOSMaster struct {
	Width, Height int
	Pixels        []float32
	Fingerprint   string
}

func FilterStringForInput(in Input) string { return fitsio.FilterString(in.PrimaryHeader) }
func GMOSInputCompatibilityKey(in Input) string {
	return gmosCompatibilityKeyPair(in.PrimaryHeader, in.HDU.Header, in.HDU.Data.Width, in.HDU.Data.Height)
}

// GMOSCompatibilityKeyForPath returns the composite detector/readout key for
// a raw GMOS file, including all image chips. Working combined files must not
// be used for calibration matching because they no longer describe chip
// geometry.
func GMOSCompatibilityKeyForPath(path string) string {
	h, err := fitsio.LoadPrimaryHeader(path)
	if err != nil {
		return ""
	}
	return gmosFrameKey(path, h)
}

var gmosMasterCache = struct {
	key        string
	bias, flat []GMOSMaster
}{}
var gmosMasterCacheLock = make(chan struct{}, 1)

func cachedGMOSMasters(ctx context.Context, s GMOSCalibrationSelection, progress ...func()) (bias, flat []GMOSMaster, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	key := GMOSCalibrationFingerprint(s)
	select {
	case gmosMasterCacheLock <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	if gmosMasterCache.key == key {
		<-gmosMasterCacheLock
		return gmosMasterCache.bias, gmosMasterCache.flat, nil
	}
	<-gmosMasterCacheLock
	bias, flat, err = BuildGMOSMastersFromSelectionContextProgress(ctx, s, progress...)
	if err == nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		select {
		case gmosMasterCacheLock <- struct{}{}:
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
		defer func() { <-gmosMasterCacheLock }()
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		if gmosMasterCache.key == key {
			return gmosMasterCache.bias, gmosMasterCache.flat, nil
		}
		gmosMasterCache.key, gmosMasterCache.bias, gmosMasterCache.flat = key, bias, flat
	}
	return
}

// BuildGMOSMastersFromSelection loads each selected calibration ensemble via
// the normal prepared GMOS loader and builds one master per detector chip.
func BuildGMOSMastersFromSelection(selection GMOSCalibrationSelection) (bias, flat []GMOSMaster, err error) {
	return BuildGMOSMastersFromSelectionContext(context.Background(), selection)
}

func BuildGMOSMastersFromSelectionContext(ctx context.Context, selection GMOSCalibrationSelection) (bias, flat []GMOSMaster, err error) {
	return BuildGMOSMastersFromSelectionContextProgress(ctx, selection)
}

func BuildGMOSMastersFromSelectionContextProgress(ctx context.Context, selection GMOSCalibrationSelection, progress ...func()) (bias, flat []GMOSMaster, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err = ValidateGMOSSelection(selection); err != nil {
		return nil, nil, err
	}
	if len(selection.BiasFrames) == 0 || len(selection.FlatFrames) == 0 {
		return nil, nil, fmt.Errorf("GMOS calibration selection has no ensemble frames")
	}

	firstBias, err := LoadInputsFromPath(selection.BiasFrames[0].Path)
	if err != nil {
		return nil, nil, err
	}
	chips := len(firstBias)
	if chips != 3 {
		return nil, nil, fmt.Errorf("GMOS bias frame does not contain exactly three prepared chips")
	}

	for chip := 0; chip < chips; chip++ {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		chipBiasData := make([]fitsio.ImageData, 0, len(selection.BiasFrames))
		for _, f := range selection.BiasFrames {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			v, e := LoadInputsFromPath(f.Path)
			if e != nil {
				return nil, nil, e
			}
			if len(v) != chips {
				return nil, nil, fmt.Errorf("GMOS bias frame does not contain exactly three prepared chips")
			}
			chipBiasData = append(chipBiasData, v[chip].HDU.Data)
		}
		b, e := BuildGMOSMasterBias(chipBiasData)
		if e != nil {
			return nil, nil, e
		}
		bias = append(bias, b)
		chipBiasData = nil
		runtime.GC()
		for _, cb := range progress {
			if cb != nil {
				cb()
			}
		}
	}

	for chip := 0; chip < chips; chip++ {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		chipFlatData := make([]fitsio.ImageData, 0, len(selection.FlatFrames))
		for _, f := range selection.FlatFrames {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			v, e := LoadInputsFromPath(f.Path)
			if e != nil {
				return nil, nil, e
			}
			if len(v) != chips {
				return nil, nil, fmt.Errorf("GMOS flat frame does not contain exactly three prepared chips")
			}
			chipFlatData = append(chipFlatData, v[chip].HDU.Data)
		}
		f, e := buildGMOSMaster(chipFlatData, &bias[chip], false)
		if e != nil {
			return nil, nil, e
		}
		flat = append(flat, f)
		chipFlatData = nil
		runtime.GC()
		for _, cb := range progress {
			if cb != nil {
				cb()
			}
		}
	}

	if err := normalizeGMOSMasterFlats(flat); err != nil {
		return nil, nil, err
	}
	return bias, flat, nil
}

// ApplyGMOSMastersToInputs applies already-built per-chip masters to loaded
// science inputs. It mutates only the in-memory inputs, never source files.
func ApplyGMOSMastersToInputs(inputs []Input, bias, flat []GMOSMaster, bpm [][]bool) error {
	if len(inputs) != len(bias) || len(inputs) != len(flat) {
		return fmt.Errorf("GMOS master chip count does not match science")
	}
	for i := range inputs {
		var mask []bool
		if i < len(bpm) {
			mask = bpm[i]
		}
		data, e := ApplyGMOSCalibration(inputs[i].HDU.Data, bias[i], flat[i], mask)
		if e != nil {
			return e
		}
		inputs[i].HDU.Data = data
	}
	return nil
}

// loadGMOSBPMInputs reads BPM pixels as categorical data. BPM values must not
// receive bias/overscan subtraction; only DATASEC trimming is applied.
func loadGMOSBPMInputs(path string) ([]Input, error) {
	f, err := fitsio.LoadFile(path)
	if err != nil {
		return nil, err
	}
	if len(f.HDUs) == 0 {
		return nil, fmt.Errorf("empty BPM file")
	}
	chips := scienceHDUs(f, f.HDUs[0].Header)
	if len(chips) == 0 {
		return nil, fmt.Errorf("BPM has no image chips")
	}
	out := make([]Input, 0, len(chips))
	for _, h := range chips {
		data := h.Data
		if secRaw := fitsio.HeaderString(h.Header, "DATASEC"); secRaw != "" {
			sec, e := parseGMOSSection(secRaw)
			if e != nil {
				return nil, e
			}
			if data.Width < sec.x2 || data.Height < sec.y2 || len(data.Pixels) != data.Width*data.Height {
				return nil, fmt.Errorf("BPM geometry is invalid")
			}
			p := make([]float32, (sec.x2-sec.x1+1)*(sec.y2-sec.y1+1))
			for y := sec.y1; y <= sec.y2; y++ {
				copy(p[(y-sec.y1)*(sec.x2-sec.x1+1):], data.Pixels[(y-1)*data.Width+sec.x1-1:(y-1)*data.Width+sec.x2])
			}
			data = fitsio.ImageData{Width: sec.x2 - sec.x1 + 1, Height: sec.y2 - sec.y1 + 1, Pixels: p}
		}
		out = append(out, Input{HDU: fitsio.HDU{Header: h.Header, Data: data}})
	}
	return out, nil
}

func BuildGMOSMasterBias(frames []fitsio.ImageData) (GMOSMaster, error) {
	return buildGMOSMaster(frames, nil, false)
}
func BuildGMOSMasterFlat(frames []fitsio.ImageData, bias GMOSMaster) (GMOSMaster, error) {
	return buildGMOSMaster(frames, &bias, true)
}

// normalizeGMOSMasterFlats applies one scalar to all chip flats.  Only
// positive finite samples participate in the scalar; unusable samples are
// represented as NaN and never become a valid calibration value.
func normalizeGMOSMasterFlats(flats []GMOSMaster) error {
	if len(flats) == 0 {
		return fmt.Errorf("no GMOS master flats")
	}
	values := make([]float32, 0)
	for i := range flats {
		if flats[i].Width <= 0 || flats[i].Height <= 0 || len(flats[i].Pixels) != flats[i].Width*flats[i].Height {
			return fmt.Errorf("GMOS master flat dimensions are invalid")
		}
		chipValues := 0
		for _, v := range flats[i].Pixels {
			if isFinite32(v) && v > 0 {
				values = append(values, v)
				chipValues++
			}
		}
		if chipValues == 0 {
			return fmt.Errorf("GMOS master flat chip %d has no positive finite samples", i+1)
		}
	}
	scalar := finiteMedian(values)
	if !isFinite32(scalar) || scalar <= 0 {
		return fmt.Errorf("GMOS master flats have no positive finite samples")
	}
	for i := range flats {
		for j, v := range flats[i].Pixels {
			if isFinite32(v) && v > 0 {
				flats[i].Pixels[j] = v / scalar
			} else {
				flats[i].Pixels[j] = float32(math.NaN())
			}
		}
	}
	return nil
}

func buildGMOSMaster(frames []fitsio.ImageData, bias *GMOSMaster, normalize bool) (GMOSMaster, error) {
	if len(frames) == 0 {
		return GMOSMaster{}, fmt.Errorf("no GMOS calibration frames")
	}
	w, h := frames[0].Width, frames[0].Height
	if bias != nil && (bias.Width != w || bias.Height != h || len(bias.Pixels) != w*h) {
		return GMOSMaster{}, fmt.Errorf("master bias dimensions do not match")
	}
	if w <= 0 || h <= 0 {
		return GMOSMaster{}, fmt.Errorf("invalid GMOS calibration dimensions")
	}
	for _, f := range frames {
		if f.Width != w || f.Height != h || len(f.Pixels) != w*h {
			return GMOSMaster{}, fmt.Errorf("GMOS calibration dimensions do not match")
		}
	}
	out := fitsio.ImageData{Width: w, Height: h, Pixels: make([]float32, w*h)}
	vals := make([]float32, len(frames))
	for i := range out.Pixels {
		n := 0
		for _, f := range frames {
			v := f.Pixels[i]
			if bias != nil {
				if len(bias.Pixels) != w*h {
					return GMOSMaster{}, fmt.Errorf("master bias dimensions do not match")
				}
				v -= bias.Pixels[i]
			}
			if isFinite32(v) {
				vals[n] = v
				n++
			}
		}
		if n == 0 {
			out.Pixels[i] = float32(math.NaN())
			continue
		}
		out.Pixels[i] = finiteMedian(vals[:n])
	}
	if normalize {
		med := finiteMedian(out.Pixels)
		if !isFinite32(med) || med <= 0 {
			return GMOSMaster{}, fmt.Errorf("GMOS master flat has no positive finite median")
		}
		for i, v := range out.Pixels {
			if isFinite32(v) && v > 0 {
				out.Pixels[i] = v / med
			} else {
				out.Pixels[i] = float32(math.NaN())
			}
		}
	}
	return GMOSMaster{Width: w, Height: h, Pixels: out.Pixels}, nil
}

func ApplyGMOSCalibration(data fitsio.ImageData, bias, flat GMOSMaster, bpm []bool) (fitsio.ImageData, error) {
	if data.Width != bias.Width || data.Height != bias.Height || data.Width != flat.Width || data.Height != flat.Height || len(data.Pixels) != data.Width*data.Height || len(bias.Pixels) != bias.Width*bias.Height || len(flat.Pixels) != flat.Width*flat.Height {
		return data, fmt.Errorf("GMOS calibration dimensions do not match science")
	}
	if len(bpm) > 0 && len(bpm) != len(data.Pixels) {
		return data, fmt.Errorf("GMOS BPM dimensions do not match science")
	}
	out := append([]float32(nil), data.Pixels...)
	for i, v := range out {
		if len(bpm) > 0 && bpm[i] {
			out[i] = float32(math.NaN())
			continue
		}
		f := flat.Pixels[i]
		if !isFinite32(v) || !isFinite32(bias.Pixels[i]) || !isFinite32(f) || f <= 0 {
			out[i] = float32(math.NaN())
			continue
		}
		out[i] = (v - bias.Pixels[i]) / f
	}
	return fitsio.ImageData{Width: data.Width, Height: data.Height, Pixels: out}, nil
}

// DiscoverGMOSCalibration scans one directory without decoding image arrays.
// It is intentionally header-driven: calibration files never enter science
// filter batches and unsupported instruments are ignored.
func DiscoverGMOSCalibration(dir string) (GMOSCalibrationManifest, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return GMOSCalibrationManifest{}, err
	}
	var m GMOSCalibrationManifest
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".fits" && ext != ".fit" && ext != ".fts" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		h, e1 := fitsio.LoadPrimaryHeader(path)
		if e1 != nil || !IsGeminiHeader(h) {
			continue
		}
		kind := gmosKind(h, e.Name())
		if kind == GMOSUnsupported {
			continue
		}
		exptime := loadExposureTime(h)
		f := GMOSCalibrationFrame{Path: path, Kind: kind, Filter: fitsio.FilterString(h), Date: dateHeader(h), Key: gmosFrameKey(path, h), ExposureTime: exptime, Anomalous: kind == GMOSBias && exptime > 5 && exptime < 5.4}
		switch kind {
		case GMOSScience:
			m.Science = append(m.Science, f)
		case GMOSBias:
			m.Bias = append(m.Bias, f)
		case GMOSTwilight:
			m.Twilight = append(m.Twilight, f)
		case GMOSBPM:
			m.BPM = append(m.BPM, f)
		}
	}
	sort.Slice(m.Science, func(i, j int) bool { return m.Science[i].Path < m.Science[j].Path })
	return m, nil
}

func gmosKind(h fitsio.Header, name string) GMOSFrameKind {
	for _, key := range []string{"OBSTYPE", "IMAGETYP"} {
		if strings.Contains(strings.ToUpper(strings.TrimSpace(fitsio.HeaderString(h, key))), "BPM") {
			return GMOSBPM
		}
	}
	upper := strings.ToUpper(name)
	if strings.Contains(upper, "BPM") || strings.Contains(upper, "BADPIX") {
		return GMOSBPM
	}
	for _, key := range []string{"OBSTYPE", "IMAGETYP", "OBJECT"} {
		v := strings.ToUpper(strings.TrimSpace(fitsio.HeaderString(h, key)))
		if strings.Contains(v, "BIAS") {
			return GMOSBias
		}
		if strings.Contains(v, "TWILIGHT") || strings.Contains(v, "FLAT") {
			return GMOSTwilight
		}
	}
	if !IsGeminiCalibrationHeader(h) {
		return GMOSScience
	}
	return GMOSUnsupported
}

func dateHeader(h fitsio.Header) string {
	v := fitsio.HeaderString(h, "DATE-OBS", "DATEOBS")
	if len(v) >= 10 {
		return v[:10]
	}
	return ""
}
func gmosCompatibilityKey(h fitsio.Header) string {
	return strings.Join([]string{fitsio.HeaderString(h, "DETECTOR"), fitsio.HeaderString(h, "CCDSUM"), fitsio.HeaderString(h, "GAIN"), fitsio.HeaderString(h, "RDNOISE"), fitsio.HeaderString(h, "DATASEC"), fitsio.HeaderString(h, "BIASSEC")}, "|")
}

func gmosFrameKey(path string, primary fitsio.Header) string {
	file, err := fitsio.LoadFileMetadata(path)
	if err != nil {
		return ""
	}
	chipHDUs := scienceHDUs(file, primary)
	return gmosCompositeCompatibilityKey(primary, chipHDUs)
}

func gmosCompositeCompatibilityKey(primary fitsio.Header, chips []fitsio.HDU) string {
	var keys []string
	for _, h := range chips {
		key := gmosCompatibilityKeyPair(primary, h.Header, h.Data.Width, h.Data.Height)
		if key == "" {
			return ""
		}
		keys = append(keys, key)
	}
	// GMOS-N raw imaging is a three-chip detector. Include every chip in
	// extension order so a frame cannot match when only its first chip agrees.
	if len(keys) != 3 {
		return ""
	}
	return strings.Join(keys, "||")
}

func gmosCompatibilityKeyPair(primary, chip fitsio.Header, width, height int) string {
	value := func(keys ...string) string {
		if v := fitsio.HeaderString(chip, keys...); v != "" {
			return v
		}
		return fitsio.HeaderString(primary, keys...)
	}
	// GMOS stores some amplifier/readout cards only on extensions. Optional
	// cards are included when present; absent cards must not make otherwise
	// compatible raw frames impossible to match.
	values := []string{value("DETECTOR", "CCDNAME"), value("CCDSUM", "BINNING"), value("DATASEC"), value("BIASSEC"), fmt.Sprintf("%dx%d", width, height)}
	for _, keys := range [][]string{{"GAIN"}, {"RDNOISE"}, {"AMPIN", "AMPINTEG"}} {
		if v := value(keys...); v != "" {
			values = append(values, v)
		}
	}
	for _, v := range values[:4] {
		if strings.TrimSpace(v) == "" {
			return ""
		}
	}
	return strings.Join(values, "|")
}

func SelectGMOSCalibrations(science GMOSCalibrationFrame, m GMOSCalibrationManifest) GMOSCalibrationSelection {
	return SelectGMOSCalibrationsWithOptions(science, m, false)
}

// SelectGMOSCalibrationsWithOptions permits an explicit override for unusual
// bias exposures that are normally excluded from the default ensemble.
func SelectGMOSCalibrationsWithOptions(science GMOSCalibrationFrame, m GMOSCalibrationManifest, allowAnomalousBias bool) GMOSCalibrationSelection {
	s := GMOSCalibrationSelection{}
	s.AllowAnomalousBias = allowAnomalousBias
	best := func(frames []GMOSCalibrationFrame, filter string) ([]GMOSCalibrationFrame, *GMOSCalibrationFrame) {
		var selected []GMOSCalibrationFrame
		var out *GMOSCalibrationFrame
		bestDist := time.Duration(1<<63 - 1)
		for i := range frames {
			if frames[i].Key == "" || science.Key == "" || frames[i].Key != science.Key {
				continue
			}
			if filter != "" && frames[i].Filter != filter {
				continue
			}
			d := dateDistance(science.Date, frames[i].Date)
			if frames[i].Anomalous && !s.AllowAnomalousBias {
				continue
			}
			selected = append(selected, frames[i])
			if out == nil || d < bestDist {
				x := frames[i]
				out = &x
				bestDist = d
			}
		}
		sort.Slice(selected, func(i, j int) bool { return selected[i].Path < selected[j].Path })
		return selected, out
	}
	s.BiasFrames, s.Bias = best(m.Bias, "")
	s.FlatFrames, s.Flat = best(m.Twilight, science.Filter)
	s.BPMFrames, s.BPM = best(m.BPM, "")
	if s.Bias == nil {
		s.Warning = "No compatible GMOS bias was found."
	}
	if s.Flat == nil {
		if s.Warning != "" {
			s.Warning += " "
		}
		s.Warning += "No compatible twilight flat was found for " + science.Filter + "; full calibration is unavailable."
	}
	return s
}

func dateDistance(a, b string) time.Duration {
	ta, e1 := time.Parse("2006-01-02", a)
	tb, e2 := time.Parse("2006-01-02", b)
	if e1 != nil || e2 != nil {
		return time.Duration(1<<63 - 1)
	}
	d := ta.Sub(tb)
	if d < 0 {
		d = -d
	}
	return d
}

// WriteGMOSCalibrationManifest publishes a small recipe atomically. Source
// frames remain untouched; callers can include its bytes in project metadata.
func WriteGMOSCalibrationManifest(path string, selection GMOSCalibrationSelection) error {
	selection.RecipeFingerprint = GMOSCalibrationFingerprint(selection)
	data, e := json.Marshal(selection)
	if e != nil {
		return e
	}
	tmp, e := os.CreateTemp(filepath.Dir(path), ".gmos-cal-*")
	if e != nil {
		return e
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, e = tmp.Write(data); e == nil {
		e = tmp.Sync()
	}
	if closeErr := tmp.Close(); e == nil {
		e = closeErr
	}
	if e != nil {
		return e
	}
	return os.Rename(name, path)
}

// GMOSCalibrationFingerprint changes whenever the ordered calibration inputs
// or override policy changes, making cached masters safe to invalidate.
func GMOSCalibrationFingerprint(s GMOSCalibrationSelection) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("anomalous=%t\n", s.AllowAnomalousBias))
	for _, group := range [][]GMOSCalibrationFrame{s.BiasFrames, s.FlatFrames, s.BPMFrames} {
		for _, f := range group {
			b.WriteString(fmt.Sprintf("%s|%s|%s|%s|%.9g|%t\n", f.Path, f.Kind, f.Filter, f.Key, f.ExposureTime, f.Anomalous))
		}
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(b.String())))
}

func GMOSCalibrationCacheValid(path, fingerprint string) bool {
	data, err := os.ReadFile(path + ".recipe")
	if err != nil {
		return false
	}
	var v struct {
		Fingerprint string `json:"fingerprint"`
	}
	return json.Unmarshal(data, &v) == nil && fingerprint != "" && v.Fingerprint == fingerprint
}
func WriteGMOSCalibrationCacheRecipe(path, fingerprint string) error {
	if fingerprint == "" {
		return fmt.Errorf("empty GMOS calibration fingerprint")
	}
	data, _ := json.Marshal(struct {
		Fingerprint string `json:"fingerprint"`
	}{fingerprint})
	tmp, err := os.CreateTemp(filepath.Dir(path), ".gmos-recipe-*")
	if err != nil {
		return err
	}
	n := tmp.Name()
	defer os.Remove(n)
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(n, path+".recipe")
}

func ValidateGMOSSelection(s GMOSCalibrationSelection) error {
	if s.Bias == nil {
		return fmt.Errorf("GMOS calibration requires a compatible bias")
	}
	if s.Flat == nil {
		return fmt.Errorf("GMOS calibration requires a compatible twilight flat")
	}
	return nil
}

func finiteMedian(values []float32) float32 {
	a := make([]float64, 0, len(values))
	for _, v := range values {
		if !math.IsNaN(float64(v)) && !math.IsInf(float64(v), 0) {
			a = append(a, float64(v))
		}
	}
	if len(a) == 0 {
		return float32(math.NaN())
	}
	sort.Float64s(a)
	mid := len(a) / 2
	if len(a)%2 == 0 {
		return float32((a[mid-1] + a[mid]) / 2)
	}
	return float32(a[mid])
}
