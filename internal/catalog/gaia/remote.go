package gaia

// This file contains the cache-through HTTP provider.  The wire format is a
// deliberately small JSON contract so deployments can put an HTTP adapter in
// front of the official Gaia archive (and tests can use httptest); no archive
// client types leak into the Provider interface.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type RemoteConfig struct {
	Endpoint         string
	Release          string
	XPRepresentation string
	HTTPClient       *http.Client
	Mode             AccessMode
	UserAgent        string
	Timeout          time.Duration
	Retries          int
	RetryBackoff     time.Duration
	BatchSize        int
	MaxResponseBytes int64
	MaxPages         int
	MaxConcurrent    int
	RateLimit        time.Duration
	SourcesPath      string
	SpectraPath      string
	Cache            *Cache
}

type RemoteProvider struct {
	cfg         RemoteConfig
	client      *http.Client
	mu          sync.Mutex
	cell        QueryCell
	sourceCell  map[uint64]QueryCell
	known       map[uint64]bool
	sem         chan struct{}
	rateMu      sync.Mutex
	lastRequest time.Time
}

// DefaultESATAPEndpoint is the public ESA Gaia TAP service. RemoteProvider's
// wire contract is a small JSON adapter, so it cannot issue /sources or
// /spectra requests directly to this TAP endpoint.
const DefaultESATAPEndpoint = "https://gea.esac.esa.int/tap-server/tap"

var ErrNoSuitableStars = errors.New("Gaia field contains no suitable stars")
var ErrRemoteNetwork = errors.New("Gaia remote request failed")
var ErrRemoteLimit = errors.New("Gaia remote response limit exceeded")
var ErrUnknownSource = errors.New("Gaia source was not discovered for this field")
var ErrUnsupportedESAEndpoint = errors.New("Gaia ESA TAP endpoint is not a JSON adapter")

type sourceEnvelope struct {
	Sources  []Source `json:"sources"`
	Results  []Source `json:"results"`
	Next     string   `json:"next"`
	NextPage int      `json:"next_page"`
}
type spectrumEnvelope struct {
	Spectra  []XPSpectrum `json:"spectra"`
	Results  []XPSpectrum `json:"results"`
	Next     string       `json:"next"`
	NextPage int          `json:"next_page"`
}

func NewRemoteProvider(cfg RemoteConfig) (*RemoteProvider, error) {
	if cfg.Endpoint == "" || cfg.Release == "" || cfg.XPRepresentation == "" {
		return nil, errors.New("Gaia remote endpoint, release, and XP representation are required")
	}
	if cfg.Mode == "" {
		cfg.Mode = AccessOnline
	}
	if cfg.Mode != AccessOnline && cfg.Mode != AccessCacheOnly {
		return nil, fmt.Errorf("unsupported Gaia access mode %q", cfg.Mode)
	}
	if cfg.Mode == AccessOnline && IsDefaultESATAPEndpoint(cfg.Endpoint) {
		return nil, fmt.Errorf("%w: configure a compatible JSON adapter endpoint or select Cache Only", ErrUnsupportedESAEndpoint)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.Retries < 0 {
		cfg.Retries = 0
	}
	if cfg.RetryBackoff <= 0 {
		cfg.RetryBackoff = 100 * time.Millisecond
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 100
	}
	if cfg.MaxResponseBytes <= 0 {
		cfg.MaxResponseBytes = 16 << 20
	}
	if cfg.MaxPages <= 0 {
		cfg.MaxPages = 100
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 1
	}
	if cfg.SourcesPath == "" {
		cfg.SourcesPath = "/sources"
	}
	if cfg.SpectraPath == "" {
		cfg.SpectraPath = "/spectra"
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	return &RemoteProvider{cfg: cfg, client: client, sourceCell: make(map[uint64]QueryCell), known: make(map[uint64]bool), sem: make(chan struct{}, cfg.MaxConcurrent)}, nil
}

// IsDefaultESATAPEndpoint reports whether endpoint identifies the official ESA
// Gaia TAP service, ignoring harmless whitespace, trailing slashes, and query
// parameters.
func IsDefaultESATAPEndpoint(endpoint string) bool {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Scheme, "https") &&
		strings.EqualFold(u.Hostname(), "gea.esac.esa.int") &&
		strings.TrimRight(u.EscapedPath(), "/") == "/tap-server/tap"
}

func (p *RemoteProvider) Mode() AccessMode { return p.cfg.Mode }

func (p *RemoteProvider) Provenance(ctx context.Context) (Provenance, error) {
	if err := ctx.Err(); err != nil {
		return Provenance{}, err
	}
	return Provenance{Release: p.cfg.Release, XPRepresentation: p.cfg.XPRepresentation, Provider: "gaia-remote", ProviderVersion: "v1", EndpointSemantics: p.cfg.Endpoint}, nil
}

func (p *RemoteProvider) DiscoverSources(ctx context.Context, q FieldQuery) ([]Source, error) {
	if err := q.Validate(); err != nil {
		return nil, err
	}
	if q.Release != p.cfg.Release {
		return nil, fmt.Errorf("query release %q does not match provider release %q", q.Release, p.cfg.Release)
	}
	p.mu.Lock()
	p.known = make(map[uint64]bool)
	p.sourceCell = make(map[uint64]QueryCell)
	p.mu.Unlock()
	cells := spatialCells(q.Footprint)
	var all []Source
	for _, spec := range cells {
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
		p.remember(sources, cell)
		all = append(all, sources...)
	}
	// Adjacent cells may overlap at boundaries; deduplicate by Gaia ID before
	// normalizing the combined result.
	byID := make(map[uint64]Source, len(all))
	for _, s := range all {
		byID[s.SourceID] = s
	}
	all = all[:0]
	for _, s := range byID {
		all = append(all, s)
	}
	all, err := NormalizeSources(all, q.Release)
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, ErrNoSuitableStars
	}
	p.mu.Lock()
	p.cell = p.cellFor(q, cells[0].id)
	p.mu.Unlock()
	return all, nil
}

func (p *RemoteProvider) RetrieveXPSpectra(ctx context.Context, release, representation string, ids []uint64) ([]XPSpectrum, error) {
	if release != p.cfg.Release || representation != p.cfg.XPRepresentation {
		return nil, fmt.Errorf("XP request does not match provider configuration")
	}
	ids = uniqueIDs(ids)
	p.mu.Lock()
	known := ids[:0]
	for _, id := range ids {
		if p.known[id] {
			known = append(known, id)
		}
	}
	p.mu.Unlock()
	ids = known
	if len(ids) == 0 {
		return nil, ErrUnknownSource
	}
	if len(ids) == 0 {
		return nil, nil
	}
	p.mu.Lock()
	cell := p.cell
	p.mu.Unlock()
	if cell.Signature == "" {
		return nil, errors.New("DiscoverSources must precede RetrieveXPSpectra")
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
			batch, err := p.fetchSpectra(ctx, release, representation, missing[start:end])
			if err != nil {
				return nil, err
			}
			got = append(got, batch...)
			if p.cfg.Cache != nil {
				if err := p.cfg.Cache.PutCell(ctx, CellBatch{Cell: cell, Spectra: batch}); err != nil {
					return nil, err
				}
			}
		}
	}
	return NormalizeXPSpectra(got, release, representation)
}

func (p *RemoteProvider) fetchSources(ctx context.Context, q FieldQuery, spec cellSpec) ([]Source, error) {
	var out []Source
	page := 1
	attempts := 0
	next := ""
	for {
		v := url.Values{}
		v.Set("release", q.Release)
		v.Set("ra", strconv.FormatFloat(spec.center.RA, 'g', -1, 64))
		v.Set("dec", strconv.FormatFloat(spec.center.Dec, 'g', -1, 64))
		v.Set("radius", strconv.FormatFloat(spec.radius, 'g', -1, 64))
		v.Set("magnitude_limit", strconv.FormatFloat(q.MagnitudeLimit, 'g', -1, 64))
		v.Set("page", strconv.Itoa(page))
		if next != "" {
			v.Set("cursor", next)
		}
		attempts++
		if attempts > p.cfg.MaxPages {
			return nil, ErrRemoteLimit
		}
		var e sourceEnvelope
		if err := p.requestJSON(ctx, p.cfg.SourcesPath, v, &e); err != nil {
			return nil, err
		}
		rows := e.Sources
		if len(rows) == 0 {
			rows = e.Results
		}
		out = append(out, rows...)
		next = e.Next
		if next == "" && e.NextPage > 0 {
			page = e.NextPage
			continue
		}
		if next == "" || len(rows) == 0 {
			break
		}
	}
	// Apply the client-side magnitude guard as well.  Archives may interpret
	// server-side constraints differently; this keeps the cache deterministic
	// and prevents XP requests for unsuitable stars.
	filtered := out[:0]
	seen := make(map[uint64]bool, len(out))
	for _, s := range out {
		if s.G <= q.MagnitudeLimit && !seen[s.SourceID] {
			seen[s.SourceID] = true
			filtered = append(filtered, s)
		}
	}
	n, err := NormalizeSources(filtered, q.Release)
	if err != nil {
		return nil, err
	}
	return n, nil
}

func (p *RemoteProvider) fetchSpectra(ctx context.Context, release, repr string, ids []uint64) ([]XPSpectrum, error) {
	v := url.Values{}
	v.Set("release", release)
	v.Set("representation", repr)
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatUint(id, 10)
	}
	v.Set("source_ids", strings.Join(parts, ","))
	var e spectrumEnvelope
	if err := p.requestJSON(ctx, p.cfg.SpectraPath, v, &e); err != nil {
		return nil, err
	}
	rows := e.Spectra
	if len(rows) == 0 {
		rows = e.Results
	}
	return NormalizeXPSpectra(rows, release, repr)
}

func (p *RemoteProvider) requestJSON(ctx context.Context, path string, q url.Values, out any) error {
	select {
	case p.sem <- struct{}{}:
		defer func() { <-p.sem }()
	case <-ctx.Done():
		return ctx.Err()
	}
	base, err := url.Parse(p.cfg.Endpoint)
	if err != nil {
		return err
	}
	base.Path = strings.TrimRight(base.Path, "/") + path
	base.RawQuery = q.Encode()
	var last error
	for attempt := 0; attempt <= p.cfg.Retries; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := p.waitRate(ctx); err != nil {
			return err
		}
		reqCtx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, base.String(), nil)
		if err == nil {
			req.Header.Set("Accept", "application/json")
			if p.cfg.UserAgent != "" {
				req.Header.Set("User-Agent", p.cfg.UserAgent)
			}
			resp, e := p.client.Do(req)
			if e == nil {
				if resp.StatusCode >= 200 && resp.StatusCode < 300 {
					r := io.LimitReader(resp.Body, p.cfg.MaxResponseBytes+1)
					data, e := io.ReadAll(r)
					resp.Body.Close()
					cancel()
					if e == nil && int64(len(data)) <= p.cfg.MaxResponseBytes {
						if e = json.Unmarshal(data, out); e == nil {
							return nil
						}
						last = e
					} else if e == nil {
						last = fmt.Errorf("Gaia response exceeds %d bytes", p.cfg.MaxResponseBytes)
					} else {
						last = e
					}
				} else {
					io.CopyN(io.Discard, resp.Body, 1024)
					resp.Body.Close()
					last = fmt.Errorf("HTTP %d", resp.StatusCode)
					if resp.StatusCode < 500 && resp.StatusCode != 429 {
						cancel()
						break
					}
				}
			} else {
				last = e
			}
		}
		cancel()
		if attempt < p.cfg.Retries {
			d := p.cfg.RetryBackoff * time.Duration(1<<min(attempt, 6))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(d):
			}
		}
	}
	return fmt.Errorf("%w: %v", ErrRemoteNetwork, last)
}

func (p *RemoteProvider) waitRate(ctx context.Context) error {
	if p.cfg.RateLimit <= 0 {
		return nil
	}
	p.rateMu.Lock()
	wait := p.cfg.RateLimit - time.Since(p.lastRequest)
	if wait < 0 {
		wait = 0
	}
	p.lastRequest = time.Now().Add(wait)
	p.rateMu.Unlock()
	if wait == 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (p *RemoteProvider) cellFor(q FieldQuery, spatial string) QueryCell {
	raw, _ := json.Marshal(struct {
		Release, Quality string
		Magnitude        float64
		Endpoint, Schema string
	}{q.Release, q.QualitySelector, q.MagnitudeLimit, p.cfg.Endpoint + p.cfg.SourcesPath + p.cfg.SpectraPath, "gaia-grid-v2"})
	h := sha256.Sum256(raw)
	sig := hex.EncodeToString(h[:])
	return QueryCell{Signature: sig, SpatialCell: spatial, Release: q.Release}
}
func (p *RemoteProvider) remember(s []Source, c QueryCell) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, x := range s {
		p.sourceCell[x.SourceID] = c
		p.known[x.SourceID] = true
	}
}

type cellSpec struct {
	id     string
	center Coordinate
	radius float64
}

// The one-degree grid is deterministic and deliberately conservative: a
// query cell's cone encloses the grid square, so overlapping fields reuse it.
func spatialCells(f Footprint) []cellSpec {
	minRA, maxRA := f.Center.RA, f.Center.RA
	minDec, maxDec := f.Center.Dec, f.Center.Dec
	if f.RadiusDeg > 0 {
		minRA = f.Center.RA - f.RadiusDeg
		maxRA = f.Center.RA + f.RadiusDeg
		minDec = f.Center.Dec - f.RadiusDeg
		maxDec = f.Center.Dec + f.RadiusDeg
	}
	if len(f.Polygon) > 0 {
		minRA, maxRA = f.Polygon[0].RA, f.Polygon[0].RA
		minDec, maxDec = f.Polygon[0].Dec, f.Polygon[0].Dec
		for _, p := range f.Polygon {
			ra := p.RA
			// Unwrap longitudes around the first vertex so a polygon crossing
			// 0/360 remains a narrow footprint rather than spanning the sky.
			for ra-minRA > 180 {
				ra -= 360
			}
			for ra-minRA < -180 {
				ra += 360
			}
			if ra < minRA {
				minRA = ra
			}
			if ra > maxRA {
				maxRA = ra
			}
			if p.Dec < minDec {
				minDec = p.Dec
			}
			if p.Dec > maxDec {
				maxDec = p.Dec
			}
		}
	}
	startRA := int(math.Floor(minRA))
	endRA := int(math.Floor(maxRA))
	startDec := int(math.Floor(minDec))
	endDec := int(math.Floor(maxDec))
	out := make([]cellSpec, 0)
	for ra := startRA; ra <= endRA; ra++ {
		for dec := startDec; dec <= endDec; dec++ {
			raNorm := math.Mod(float64(ra)+.5+360, 360)
			c := Coordinate{RA: raNorm, Dec: float64(dec) + .5}
			r := .71
			if c.RA < 0 || c.RA >= 360 || c.Dec < -90 || c.Dec > 90 {
				continue
			}
			raID := int(math.Mod(float64(ra)+360, 360))
			out = append(out, cellSpec{id: fmt.Sprintf("ra%d_dec%d", raID, dec), center: c, radius: r})
		}
	}
	if len(out) == 0 {
		out = []cellSpec{{id: "ra0_dec0", center: f.Center, radius: f.RadiusDeg}}
	}
	return out
}
func uniqueIDs(ids []uint64) []uint64 {
	m := map[uint64]bool{}
	out := make([]uint64, 0, len(ids))
	for _, id := range ids {
		if id > 0 && !m[id] {
			m[id] = true
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
