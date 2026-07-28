package gaia

// Native ESA TAP source discovery.  This client deliberately implements only
// the Gaia DR3 source-summary query; XP DataLink retrieval is a separate step.

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ErrESATAPHTTP   = errors.New("Gaia ESA TAP HTTP request failed")
	ErrESATAPQuery  = errors.New("Gaia ESA TAP query failed")
	ErrESATAPDecode = errors.New("Gaia ESA TAP response was incomplete or malformed")
)

type ESAConfig struct {
	Endpoint         string
	Release          string
	XPRepresentation string
	HTTPClient       *http.Client
	Mode             AccessMode
	UserAgent        string
	Timeout          time.Duration
	MaxResponseBytes int64
	MaxRows          int
	BatchSize        int
	Cache            *Cache
}

type ESAProvider struct {
	cfg        ESAConfig
	client     *http.Client
	sourceCell map[uint64]QueryCell
	mu         sync.RWMutex
}

func NewESAProvider(cfg ESAConfig) (*ESAProvider, error) {
	if cfg.Endpoint == "" || cfg.Release == "" || cfg.XPRepresentation == "" {
		return nil, errors.New("Gaia ESA endpoint, release, and XP representation are required")
	}
	if !strings.EqualFold(cfg.Release, "DR3") {
		return nil, fmt.Errorf("native ESA provider supports Gaia DR3 only, got %q", cfg.Release)
	}
	if cfg.Mode == "" {
		cfg.Mode = AccessOnline
	}
	if cfg.Mode != AccessOnline && cfg.Mode != AccessCacheOnly {
		return nil, fmt.Errorf("unsupported Gaia access mode %q", cfg.Mode)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.MaxResponseBytes <= 0 {
		cfg.MaxResponseBytes = 16 << 20
	}
	if cfg.MaxRows <= 0 {
		cfg.MaxRows = 100000
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 100
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	return &ESAProvider{cfg: cfg, client: client, sourceCell: make(map[uint64]QueryCell)}, nil
}

func (p *ESAProvider) Mode() AccessMode { return p.cfg.Mode }

func (p *ESAProvider) Provenance(ctx context.Context) (Provenance, error) {
	if err := ctx.Err(); err != nil {
		return Provenance{}, err
	}
	return Provenance{Release: p.cfg.Release, XPRepresentation: p.cfg.XPRepresentation, Provider: "gaia-esa-tap", ProviderVersion: "dr3-source-v1", EndpointSemantics: p.cfg.Endpoint}, nil
}

func (p *ESAProvider) DiscoverSources(ctx context.Context, q FieldQuery) ([]Source, error) {
	if err := q.Validate(); err != nil {
		return nil, err
	}
	if !strings.EqualFold(q.Release, p.cfg.Release) {
		return nil, fmt.Errorf("query release %q does not match provider release %q", q.Release, p.cfg.Release)
	}
	var all []Source
	for _, spec := range spatialCells(q.Footprint) {
		cell := p.cellFor(q, spec.id)
		var sources []Source
		complete := false
		if p.cfg.Cache != nil {
			var err error
			complete, err = p.cfg.Cache.IsCellComplete(ctx, cell)
			if err != nil {
				return nil, err
			}
			if complete {
				sources, err = p.cfg.Cache.Sources(ctx, cell)
				if err != nil {
					return nil, err
				}
			}
		}
		if !complete {
			if p.cfg.Mode == AccessCacheOnly {
				return nil, fmt.Errorf("Gaia cache miss for %s", cell.SpatialCell)
			}
			var err error
			sources, err = p.fetchSources(ctx, q, spec)
			if err != nil {
				return nil, err
			}
			if p.cfg.Cache != nil {
				if err := p.cfg.Cache.PutCell(ctx, CellBatch{Cell: cell, Sources: sources}); err != nil {
					return nil, err
				}
			}
		}
		p.mu.Lock()
		for _, source := range sources {
			p.sourceCell[source.SourceID] = cell
		}
		p.mu.Unlock()
		all = append(all, sources...)
	}
	byID := make(map[uint64]Source, len(all))
	for _, source := range all {
		byID[source.SourceID] = source
	}
	all = all[:0]
	for _, source := range byID {
		all = append(all, source)
	}
	all, err := NormalizeSources(all, q.Release)
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, ErrNoSuitableStars
	}
	return all, nil
}

func (p *ESAProvider) RetrieveXPSpectra(ctx context.Context, release, representation string, sourceIDs []uint64) ([]XPSpectrum, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if release != p.cfg.Release || representation != p.cfg.XPRepresentation {
		return nil, fmt.Errorf("XP request does not match provider configuration")
	}
	ids := uniqueIDs(sourceIDs)
	if len(ids) == 0 {
		return nil, nil
	}
	got := make([]XPSpectrum, 0, len(ids))
	missing := make([]uint64, 0, len(ids))
	for _, id := range ids {
		if p.cfg.Cache != nil {
			s, err := p.cfg.Cache.Spectrum(ctx, release, id, representation)
			if err == nil {
				got = append(got, s)
				continue
			}
		}
		missing = append(missing, id)
	}
	if len(missing) > 0 {
		if p.cfg.Mode == AccessCacheOnly {
			return nil, fmt.Errorf("Gaia XP cache miss for %d source(s)", len(missing))
		}
		for start := 0; start < len(missing); start += p.cfg.BatchSize {
			end := start + p.cfg.BatchSize
			if end > len(missing) {
				end = len(missing)
			}
			requested := missing[start:end]
			batch, err := p.fetchXPSpectra(ctx, release, representation, requested)
			if err != nil {
				return nil, err
			}
			if err := validateXPIDSet(batch, requested); err != nil {
				return nil, err
			}
			got = append(got, batch...)
			if p.cfg.Cache != nil {
				for _, s := range batch {
					p.mu.RLock()
					cell := p.sourceCell[s.SourceID]
					p.mu.RUnlock()
					if cell.Signature == "" {
						// The cache schema requires a discovered source row for cell
						// membership; defer caching until DiscoverSources maps this ID.
						continue
					}
					if err := p.cfg.Cache.PutCell(ctx, CellBatch{Cell: cell, Spectra: []XPSpectrum{s}}); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	return NormalizeXPSpectra(got, release, representation)
}

func (p *ESAProvider) fetchXPSpectra(ctx context.Context, release, representation string, ids []uint64) ([]XPSpectrum, error) {
	u, err := url.Parse(p.cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	u.Path = "/data-server/data"
	q := u.Query()
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatUint(id, 10)
	}
	q.Set("ID", strings.Join(parts, ","))
	q.Set("RETRIEVAL_TYPE", "XP_SAMPLED")
	q.Set("RELEASE", "Gaia DR3")
	q.Set("DATA_STRUCTURE", "INDIVIDUAL")
	q.Set("FORMAT", "CSV")
	u.RawQuery = q.Encode()
	reqCtx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/csv, application/zip, */*")
	if p.cfg.UserAgent != "" {
		req.Header.Set("User-Agent", p.cfg.UserAgent)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", ErrESATAPHTTP, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, p.cfg.MaxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrESATAPDecode, err)
	}
	if int64(len(data)) > p.cfg.MaxResponseBytes {
		return nil, fmt.Errorf("%w: response exceeds %d bytes", ErrRemoteLimit, p.cfg.MaxResponseBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: HTTP %d: %s", ErrESATAPHTTP, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "zip") || bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, fmt.Errorf("%w: invalid ZIP: %v", ErrESATAPDecode, err)
		}
		var all []XPSpectrum
		var total int64
		for _, f := range zr.File {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			r, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("%w: %v", ErrESATAPDecode, err)
			}
			b, err := io.ReadAll(io.LimitReader(r, p.cfg.MaxResponseBytes-total+1))
			_ = r.Close()
			total += int64(len(b))
			if total > p.cfg.MaxResponseBytes {
				return nil, fmt.Errorf("%w: ZIP uncompressed members exceed %d bytes", ErrRemoteLimit, p.cfg.MaxResponseBytes)
			}
			if err != nil {
				return nil, fmt.Errorf("%w: invalid ZIP entry", ErrESATAPDecode)
			}
			rows, err := decodeXPCSV(ctx, b, release, representation)
			if err != nil {
				return nil, err
			}
			all = append(all, rows...)
		}
		return NormalizeXPSpectra(all, release, representation)
	}
	return decodeXPCSV(ctx, data, release, representation)
}

func validateXPIDSet(got []XPSpectrum, requested []uint64) error {
	want := make(map[uint64]bool, len(requested))
	for _, id := range requested {
		want[id] = true
	}
	seen := make(map[uint64]bool, len(got))
	for _, s := range got {
		if !want[s.SourceID] || seen[s.SourceID] {
			return fmt.Errorf("%w: unexpected or duplicate source_id %d", ErrESATAPDecode, s.SourceID)
		}
		seen[s.SourceID] = true
	}
	for id := range want {
		if !seen[id] {
			return fmt.Errorf("%w: missing source_id %d", ErrESATAPDecode, id)
		}
	}
	return nil
}

func decodeXPCSV(ctx context.Context, data []byte, release, representation string) ([]XPSpectrum, error) {
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("%w: CSV header: %v", ErrESATAPDecode, err)
	}
	idx := make(map[string]int, len(header))
	for i, h := range header {
		idx[strings.ToLower(strings.TrimSpace(h))] = i
	}
	for _, name := range []string{"source_id", "wavelength", "flux", "flux_error"} {
		if _, ok := idx[name]; !ok {
			return nil, fmt.Errorf("%w: missing column %s", ErrESATAPDecode, name)
		}
	}
	byID := make(map[uint64]*XPSpectrum)
	for rowNo := 1; ; rowNo++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		row, e := r.Read()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, fmt.Errorf("%w: CSV row %d: %v", ErrESATAPDecode, rowNo, e)
		}
		if len(row) < len(header) {
			return nil, fmt.Errorf("%w: CSV row %d incomplete", ErrESATAPDecode, rowNo)
		}
		id, e := strconv.ParseUint(strings.TrimSpace(row[idx["source_id"]]), 10, 64)
		if e != nil {
			return nil, fmt.Errorf("%w: CSV row %d source_id", ErrESATAPDecode, rowNo)
		}
		parse := func(name string) (float64, error) { return strconv.ParseFloat(strings.TrimSpace(row[idx[name]]), 64) }
		w, e1 := parse("wavelength")
		f, e2 := parse("flux")
		fe, e3 := parse("flux_error")
		if e1 != nil || e2 != nil || e3 != nil || !finite(w) || !finite(f) || !finite(fe) {
			return nil, fmt.Errorf("%w: CSV row %d invalid sample", ErrESATAPDecode, rowNo)
		}
		s := byID[id]
		if s == nil {
			s = &XPSpectrum{Release: release, SourceID: id, RepresentationVersion: representation, CalibrationVersion: "Gaia DR3 XP_SAMPLED"}
			byID[id] = s
		}
		s.Wavelengths = append(s.Wavelengths, w)
		s.Flux = append(s.Flux, f)
		s.FluxErrors = append(s.FluxErrors, fe)
	}
	out := make([]XPSpectrum, 0, len(byID))
	for _, s := range byID {
		out = append(out, *s)
	}
	return NormalizeXPSpectra(out, release, representation)
}

const esaDR3SourceADQL = `SELECT source_id,ra,dec,ref_epoch,pmra,pmdec,ra_error,dec_error,pmra_error,pmdec_error,phot_g_mean_mag,phot_bp_mean_mag,phot_rp_mean_mag,phot_g_mean_flux_over_error,phot_bp_mean_flux_over_error,phot_rp_mean_flux_over_error FROM gaiadr3.gaia_source WHERE 1=CONTAINS(POINT('ICRS',ra,dec),CIRCLE('ICRS',%s,%s,%s)) AND phot_g_mean_mag <= %s AND has_xp_sampled = 'true' AND source_id IS NOT NULL AND ra IS NOT NULL AND dec IS NOT NULL AND ref_epoch IS NOT NULL AND pmra IS NOT NULL AND pmdec IS NOT NULL AND ra_error IS NOT NULL AND dec_error IS NOT NULL AND pmra_error IS NOT NULL AND pmdec_error IS NOT NULL AND phot_g_mean_mag IS NOT NULL AND phot_bp_mean_mag IS NOT NULL AND phot_rp_mean_mag IS NOT NULL AND phot_g_mean_flux_over_error > 0 AND phot_bp_mean_flux_over_error > 0 AND phot_rp_mean_flux_over_error > 0`

func (p *ESAProvider) fetchSources(ctx context.Context, q FieldQuery, spec cellSpec) ([]Source, error) {
	adql := fmt.Sprintf(esaDR3SourceADQL,
		formatADQLFloat(spec.center.RA), formatADQLFloat(spec.center.Dec),
		formatADQLFloat(spec.radius), formatADQLFloat(q.MagnitudeLimit))
	form := url.Values{"REQUEST": {"doQuery"}, "LANG": {"ADQL"}, "FORMAT": {"json"}, "QUERY": {adql}}
	endpoint := strings.TrimRight(p.cfg.Endpoint, "/") + "/sync"
	reqCtx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if p.cfg.UserAgent != "" {
		req.Header.Set("User-Agent", p.cfg.UserAgent)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", ErrESATAPHTTP, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, p.cfg.MaxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrESATAPDecode, err)
	}
	if int64(len(data)) > p.cfg.MaxResponseBytes {
		return nil, fmt.Errorf("%w: response exceeds %d bytes", ErrRemoteLimit, p.cfg.MaxResponseBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: HTTP %d: %s", ErrESATAPHTTP, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return decodeESASources(ctx, data, q.Release, q.MagnitudeLimit, p.cfg.MaxRows)
}

type esaJSONResponse struct {
	Metadata []struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Data  [][]json.RawMessage `json:"data"`
	Error json.RawMessage     `json:"error"`
}

func decodeESASources(ctx context.Context, data []byte, release string, magnitudeLimit float64, maxRows int) ([]Source, error) {
	var response esaJSONResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrESATAPDecode, err)
	}
	if len(response.Error) > 0 && string(response.Error) != "null" {
		return nil, fmt.Errorf("%w: %s", ErrESATAPQuery, strings.TrimSpace(string(response.Error)))
	}
	if len(response.Data) > maxRows {
		return nil, fmt.Errorf("%w: TAP returned more than %d rows", ErrRemoteLimit, maxRows)
	}
	index := make(map[string]int, len(response.Metadata))
	for i, col := range response.Metadata {
		index[strings.ToLower(strings.TrimSpace(col.Name))] = i
	}
	required := []string{"source_id", "ra", "dec", "ref_epoch", "pmra", "pmdec", "ra_error", "dec_error", "pmra_error", "pmdec_error", "phot_g_mean_mag", "phot_bp_mean_mag", "phot_rp_mean_mag", "phot_g_mean_flux_over_error", "phot_bp_mean_flux_over_error", "phot_rp_mean_flux_over_error"}
	for _, name := range required {
		if _, ok := index[name]; !ok {
			return nil, fmt.Errorf("%w: missing column %s", ErrESATAPDecode, name)
		}
	}
	out := make([]Source, 0, len(response.Data))
	for rowNo, row := range response.Data {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(row) < len(response.Metadata) {
			return nil, fmt.Errorf("%w: row %d has incomplete columns", ErrESATAPDecode, rowNo)
		}
		id, err := rawUint(row[index["source_id"]])
		if err != nil {
			return nil, fmt.Errorf("%w: row %d source_id: %v", ErrESATAPDecode, rowNo, err)
		}
		value := func(name string) (float64, error) { return rawFloat(row[index[name]]) }
		var s Source
		s.Release, s.SourceID = release, id
		if s.RA, err = value("ra"); err != nil {
			return nil, rowDecodeError(rowNo, "ra", err)
		}
		if s.Dec, err = value("dec"); err != nil {
			return nil, rowDecodeError(rowNo, "dec", err)
		}
		if s.ReferenceEpoch, err = value("ref_epoch"); err != nil {
			return nil, rowDecodeError(rowNo, "ref_epoch", err)
		}
		if s.ProperMotionRA, err = value("pmra"); err != nil {
			return nil, rowDecodeError(rowNo, "pmra", err)
		}
		if s.ProperMotionDec, err = value("pmdec"); err != nil {
			return nil, rowDecodeError(rowNo, "pmdec", err)
		}
		raErr, e1 := value("ra_error")
		decErr, e2 := value("dec_error")
		if e1 != nil || e2 != nil {
			return nil, rowDecodeError(rowNo, "position error", firstErr(e1, e2))
		}
		s.PositionError = math.Max(raErr, decErr)
		if s.ProperMotionErrorRA, err = value("pmra_error"); err != nil {
			return nil, rowDecodeError(rowNo, "pmra_error", err)
		}
		if s.ProperMotionErrorDec, err = value("pmdec_error"); err != nil {
			return nil, rowDecodeError(rowNo, "pmdec_error", err)
		}
		if s.G, err = value("phot_g_mean_mag"); err != nil {
			return nil, rowDecodeError(rowNo, "G", err)
		}
		if s.BP, err = value("phot_bp_mean_mag"); err != nil {
			return nil, rowDecodeError(rowNo, "BP", err)
		}
		if s.RP, err = value("phot_rp_mean_mag"); err != nil {
			return nil, rowDecodeError(rowNo, "RP", err)
		}
		for _, band := range []struct {
			name string
			dst  *float64
		}{{"G", &s.GError}, {"BP", &s.BPError}, {"RP", &s.RPError}} {
			snr, e := value("phot_" + strings.ToLower(band.name) + "_mean_flux_over_error")
			if e != nil || !finite(snr) || snr <= 0 {
				return nil, rowDecodeError(rowNo, band.name+" error", firstErr(e, errors.New("invalid SNR")))
			}
			*band.dst = 1.0857362047581296 / snr
		}
		if s.G > magnitudeLimit {
			continue
		}
		if err := s.Validate(release); err != nil {
			return nil, fmt.Errorf("%w: row %d: %v", ErrESATAPDecode, rowNo, err)
		}
		out = append(out, s)
	}
	return NormalizeSources(out, release)
}

func rowDecodeError(row int, field string, err error) error {
	return fmt.Errorf("%w: row %d %s: %v", ErrESATAPDecode, row, field, err)
}
func firstErr(a, b error) error {
	if a != nil {
		return a
	}
	return b
}
func rawFloat(raw json.RawMessage) (float64, error) {
	var n float64
	if string(raw) == "null" || json.Unmarshal(raw, &n) != nil || !finite(n) {
		return 0, errors.New("invalid number")
	}
	return n, nil
}
func rawUint(raw json.RawMessage) (uint64, error) {
	text := strings.Trim(string(raw), `" `)
	return strconv.ParseUint(text, 10, 64)
}
func formatADQLFloat(v float64) string { return strconv.FormatFloat(v, 'f', 10, 64) }

func (p *ESAProvider) cellFor(q FieldQuery, spatial string) QueryCell {
	raw := fmt.Sprintf("esa-tap-dr3-v1|%s|%s|%.8g", q.Release, q.QualitySelector, q.MagnitudeLimit)
	h := sha256.Sum256([]byte(raw))
	return QueryCell{Signature: hex.EncodeToString(h[:]), SpatialCell: spatial, Release: q.Release}
}
