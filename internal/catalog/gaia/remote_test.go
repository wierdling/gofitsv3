package gaia

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func remoteSource(id uint64) Source {
	return Source{Release: "DR3", SourceID: id, RA: 10, Dec: 2, ReferenceEpoch: 2016, PositionError: .1, ProperMotionErrorRA: .1, ProperMotionErrorDec: .1, G: 12, BP: 12.2, RP: 11.8, GError: .01, BPError: .01, RPError: .01}
}
func remoteSpectrum(id uint64) XPSpectrum {
	return XPSpectrum{Release: "DR3", SourceID: id, RepresentationVersion: "xp-v1", CalibrationVersion: "c1", Wavelengths: []float64{400, 500}, Flux: []float64{1, 2}, FluxErrors: []float64{.1, .1}}
}

func TestRemoteProviderRejectsMixedKnownAndUnknownSpectraIDs(t *testing.T) {
	p, err := NewRemoteProvider(RemoteConfig{Endpoint: "http://example.test", Release: "DR3", XPRepresentation: "xp-v1"})
	if err != nil {
		t.Fatal(err)
	}
	p.known[1] = true
	p.cell = QueryCell{Signature: "sig", SpatialCell: "cell", Release: "DR3"}
	if _, err := p.RetrieveXPSpectra(context.Background(), "DR3", "xp-v1", []uint64{1, 2}); !errors.Is(err, ErrUnknownSource) {
		t.Fatalf("mixed IDs err=%v", err)
	}
}

func TestRemoteProviderFetchSpectraRequiresExactRequestedIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(spectrumEnvelope{Spectra: []XPSpectrum{remoteSpectrum(1)}})
	}))
	defer srv.Close()
	p, err := NewRemoteProvider(RemoteConfig{Endpoint: srv.URL, Release: "DR3", XPRepresentation: "xp-v1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.fetchSpectra(context.Background(), "DR3", "xp-v1", []uint64{1, 2}); err == nil {
		t.Fatal("incomplete spectra response accepted")
	}
}

func TestRemoteProviderOversizedJSONReturnsLimitWithoutRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"spectra":"` + strings.Repeat("x", 128) + `"}`))
	}))
	defer srv.Close()
	p, err := NewRemoteProvider(RemoteConfig{Endpoint: srv.URL, Release: "DR3", XPRepresentation: "xp-v1", MaxResponseBytes: 16, Retries: 3})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.fetchSpectra(context.Background(), "DR3", "xp-v1", []uint64{1}); !errors.Is(err, ErrRemoteLimit) {
		t.Fatalf("oversized response err=%v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("oversized response retried %d times", calls.Load())
	}
}

func TestRemoteProviderCacheThroughAndBatching(t *testing.T) {
	var sourceCalls, spectrumCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/sources":
			sourceCalls.Add(1)
			_ = json.NewEncoder(w).Encode(sourceEnvelope{Sources: []Source{remoteSource(2), remoteSource(1)}})
		case "/spectra":
			spectrumCalls.Add(1)
			var rows []XPSpectrum
			for _, raw := range strings.Split(r.URL.Query().Get("source_ids"), ",") {
				if raw == "1" {
					rows = append(rows, remoteSpectrum(1))
				}
				if raw == "2" {
					rows = append(rows, remoteSpectrum(2))
				}
			}
			_ = json.NewEncoder(w).Encode(spectrumEnvelope{Spectra: rows})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, err := OpenCache(context.Background(), filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	p, err := NewRemoteProvider(RemoteConfig{Endpoint: srv.URL, Release: "DR3", XPRepresentation: "xp-v1", Cache: c, BatchSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	q := FieldQuery{Footprint: Footprint{Center: Coordinate{RA: 10.5, Dec: 2.5}, RadiusDeg: .1}, ObservationEpoch: 2024, Release: "DR3", MagnitudeLimit: 18}
	s, err := p.DiscoverSources(context.Background(), q)
	if err != nil || len(s) != 2 {
		t.Fatalf("sources=%v err=%v", s, err)
	}
	x, err := p.RetrieveXPSpectra(context.Background(), "DR3", "xp-v1", []uint64{2, 1, 2})
	if err != nil || len(x) != 2 {
		t.Fatalf("spectra=%v err=%v", x, err)
	}
	if sourceCalls.Load() != 1 || spectrumCalls.Load() != 2 {
		t.Fatalf("calls source=%d spectra=%d", sourceCalls.Load(), spectrumCalls.Load())
	}
	// A fresh provider with the same query reads both source and XP rows locally.
	p2, _ := NewRemoteProvider(RemoteConfig{Endpoint: srv.URL, Release: "DR3", XPRepresentation: "xp-v1", Cache: c, BatchSize: 1})
	if _, err = p2.DiscoverSources(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	if _, err = p2.RetrieveXPSpectra(context.Background(), "DR3", "xp-v1", []uint64{1, 2}); err != nil {
		t.Fatal(err)
	}
	if sourceCalls.Load() != 1 || spectrumCalls.Load() != 2 {
		t.Fatalf("cache miss caused network source=%d spectra=%d", sourceCalls.Load(), spectrumCalls.Load())
	}
}

func TestRemoteProviderRejectsDefaultESATAPEndpoint(t *testing.T) {
	_, err := NewRemoteProvider(RemoteConfig{Endpoint: DefaultESATAPEndpoint, Release: "DR3", XPRepresentation: "xp-v1"})
	if !errors.Is(err, ErrUnsupportedESAEndpoint) {
		t.Fatalf("expected ESA TAP endpoint error, got %v", err)
	}
	if !strings.Contains(err.Error(), "JSON adapter") || !strings.Contains(err.Error(), "Cache Only") {
		t.Fatalf("expected actionable endpoint guidance, got %v", err)
	}
}

func TestRemoteProviderAllowsCustomJSONAdapterEndpoint(t *testing.T) {
	p, err := NewRemoteProvider(RemoteConfig{Endpoint: "https://gea.esac.esa.int/gaia-json", Release: "DR3", XPRepresentation: "xp-v1"})
	if err != nil {
		t.Fatalf("custom adapter endpoint rejected: %v", err)
	}
	if p == nil {
		t.Fatal("expected provider")
	}
}

func TestRemoteProviderOnlineThenCacheOnlyEndToEnd(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/sources" {
			_ = json.NewEncoder(w).Encode(sourceEnvelope{Sources: []Source{remoteSource(7)}})
			return
		}
		_ = json.NewEncoder(w).Encode(spectrumEnvelope{Spectra: []XPSpectrum{remoteSpectrum(7)}})
	}))
	cachePath := filepath.Join(t.TempDir(), "e2e-cache.sqlite")
	c, err := OpenCache(context.Background(), cachePath)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	q := FieldQuery{Footprint: Footprint{Center: Coordinate{RA: 10.5, Dec: 2.5}, RadiusDeg: .1}, ObservationEpoch: 2024, Release: "DR3", MagnitudeLimit: 18}
	online, err := NewRemoteProvider(RemoteConfig{Endpoint: srv.URL, Release: "DR3", XPRepresentation: "xp-v1", Cache: c})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = online.DiscoverSources(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	if _, err = online.RetrieveXPSpectra(context.Background(), "DR3", "xp-v1", []uint64{7}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("online fetch calls=%d, want 2", calls.Load())
	}
	// A cache-only provider can complete the same field after the network is gone.
	srv.Close()
	offline, err := NewRemoteProvider(RemoteConfig{Endpoint: srv.URL, Release: "DR3", XPRepresentation: "xp-v1", Mode: AccessCacheOnly, Cache: c})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := offline.DiscoverSources(context.Background(), q); err != nil || len(got) != 1 {
		t.Fatalf("cache-only sources=%v err=%v", got, err)
	}
	if got, err := offline.RetrieveXPSpectra(context.Background(), "DR3", "xp-v1", []uint64{7}); err != nil || len(got) != 1 {
		t.Fatalf("cache-only spectra=%v err=%v", got, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("cache-only path contacted network: calls=%d", calls.Load())
	}
}

func TestRemoteProviderCacheOnlyMissNeverContactsNetwork(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/sources" {
			_ = json.NewEncoder(w).Encode(sourceEnvelope{Sources: []Source{remoteSource(1)}})
			return
		}
		http.Error(w, "unexpected spectrum network", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c, err := OpenCache(context.Background(), filepath.Join(t.TempDir(), "miss.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	p, err := NewRemoteProvider(RemoteConfig{Endpoint: srv.URL, Release: "DR3", XPRepresentation: "xp-v1", Mode: AccessCacheOnly, Cache: c})
	if err != nil {
		t.Fatal(err)
	}
	q := FieldQuery{Footprint: Footprint{Center: Coordinate{RA: 10.5, Dec: 2.5}, RadiusDeg: .1}, ObservationEpoch: 2024, Release: "DR3", MagnitudeLimit: 18}
	if _, err := p.DiscoverSources(context.Background(), q); err == nil || !strings.Contains(err.Error(), "cache miss") {
		t.Fatalf("cache-only miss err=%v", err)
	}
	if _, err := p.RetrieveXPSpectra(context.Background(), "DR3", "xp-v1", []uint64{7}); !errors.Is(err, ErrUnknownSource) {
		t.Fatalf("spectrum miss err=%v", err)
	}
	// Prime only the source cell; an existing source with no XP row must take
	// the cache-only spectrum-miss path without attempting HTTP.
	online, err := NewRemoteProvider(RemoteConfig{Endpoint: srv.URL, Release: "DR3", XPRepresentation: "xp-v1", Cache: c})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := online.DiscoverSources(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	primedCalls := calls.Load()
	offline, err := NewRemoteProvider(RemoteConfig{Endpoint: srv.URL, Release: "DR3", XPRepresentation: "xp-v1", Mode: AccessCacheOnly, Cache: c})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := offline.DiscoverSources(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	if _, err := offline.RetrieveXPSpectra(context.Background(), "DR3", "xp-v1", []uint64{1}); err == nil || !strings.Contains(err.Error(), "XP cache miss") {
		t.Fatalf("missing XP row err=%v", err)
	}
	if calls.Load() != primedCalls {
		t.Fatalf("cache-only miss contacted network: before=%d after=%d", primedCalls, calls.Load())
	}
}

func TestRemoteProviderRetriesAndCancellation(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if calls.Load() == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_ = json.NewEncoder(w).Encode(sourceEnvelope{Sources: []Source{remoteSource(1)}})
	}))
	defer srv.Close()
	p, _ := NewRemoteProvider(RemoteConfig{Endpoint: srv.URL, Release: "DR3", XPRepresentation: "xp-v1", Retries: 1, RetryBackoff: 1})
	q := FieldQuery{Footprint: Footprint{Center: Coordinate{RA: 10.5, Dec: 2.5}, RadiusDeg: .1}, ObservationEpoch: 2024, Release: "DR3", MagnitudeLimit: 18}
	if _, err := p.DiscoverSources(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("retry calls=%d", calls.Load())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.DiscoverSources(ctx, q); err == nil {
		t.Fatal("cancelled request succeeded")
	}
}

func TestRemoteProviderPageLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(sourceEnvelope{Sources: []Source{remoteSource(1)}, NextPage: 2})
	}))
	defer srv.Close()
	p, _ := NewRemoteProvider(RemoteConfig{Endpoint: srv.URL, Release: "DR3", XPRepresentation: "xp-v1", MaxPages: 1})
	q := FieldQuery{Footprint: Footprint{Center: Coordinate{RA: 10.5, Dec: 2.5}, RadiusDeg: .1}, ObservationEpoch: 2024, Release: "DR3", MagnitudeLimit: 18}
	if _, err := p.DiscoverSources(context.Background(), q); !errors.Is(err, ErrRemoteLimit) {
		t.Fatalf("err=%v", err)
	}
}

func TestRemoteProviderRateLimitIsShared(t *testing.T) {
	var calls atomic.Int32
	var mu sync.Mutex
	var requestTimes []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		mu.Lock()
		requestTimes = append(requestTimes, time.Now())
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(sourceEnvelope{Sources: []Source{remoteSource(1)}})
	}))
	defer srv.Close()
	p, _ := NewRemoteProvider(RemoteConfig{Endpoint: srv.URL, Release: "DR3", XPRepresentation: "xp-v1", RateLimit: 20 * time.Millisecond})
	q := FieldQuery{Footprint: Footprint{Center: Coordinate{RA: 10.5, Dec: 2.5}, RadiusDeg: .1}, ObservationEpoch: 2024, Release: "DR3", MagnitudeLimit: 18}
	_, err := p.DiscoverSources(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.DiscoverSources(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls.Load() != 2 || len(requestTimes) != 2 || requestTimes[1].Sub(requestTimes[0]) < 15*time.Millisecond {
		t.Fatalf("request spacing=%v calls=%d", requestTimes[1].Sub(requestTimes[0]), calls.Load())
	}
}

func TestRemoteProviderUnknownIDsAndEmptyCache(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(sourceEnvelope{})
	}))
	defer srv.Close()
	c, _ := OpenCache(context.Background(), filepath.Join(t.TempDir(), "empty.db"))
	defer c.Close()
	p, _ := NewRemoteProvider(RemoteConfig{Endpoint: srv.URL, Release: "DR3", XPRepresentation: "xp-v1", Cache: c})
	q := FieldQuery{Footprint: Footprint{Center: Coordinate{RA: 10.5, Dec: 2.5}, RadiusDeg: .1}, ObservationEpoch: 2024, Release: "DR3", MagnitudeLimit: 18}
	if _, err := p.DiscoverSources(context.Background(), q); !errors.Is(err, ErrNoSuitableStars) {
		t.Fatalf("err=%v", err)
	}
	if _, err := p.DiscoverSources(context.Background(), q); !errors.Is(err, ErrNoSuitableStars) {
		t.Fatalf("cached err=%v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("empty cell refetched: %d", calls.Load())
	}
	if _, err := p.RetrieveXPSpectra(context.Background(), "DR3", "xp-v1", []uint64{99}); !errors.Is(err, ErrUnknownSource) {
		t.Fatalf("unknown err=%v", err)
	}
}

func TestRemoteProviderEndpointSemanticsSeparateCache(t *testing.T) {
	var a, b atomic.Int32
	mk := func(c *atomic.Int32) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c.Add(1)
			_ = json.NewEncoder(w).Encode(sourceEnvelope{Sources: []Source{remoteSource(1)}})
		}))
	}
	sa, sb := mk(&a), mk(&b)
	defer sa.Close()
	defer sb.Close()
	c, _ := OpenCache(context.Background(), filepath.Join(t.TempDir(), "sig.db"))
	defer c.Close()
	q := FieldQuery{Footprint: Footprint{Center: Coordinate{RA: 10.5, Dec: 2.5}, RadiusDeg: .1}, ObservationEpoch: 2024, Release: "DR3", MagnitudeLimit: 18}
	pa, _ := NewRemoteProvider(RemoteConfig{Endpoint: sa.URL, Release: "DR3", XPRepresentation: "xp-v1", Cache: c})
	pb, _ := NewRemoteProvider(RemoteConfig{Endpoint: sb.URL, Release: "DR3", XPRepresentation: "xp-v1", Cache: c})
	if _, e := pa.DiscoverSources(context.Background(), q); e != nil {
		t.Fatal(e)
	}
	if _, e := pb.DiscoverSources(context.Background(), q); e != nil {
		t.Fatal(e)
	}
	if a.Load() != 1 || b.Load() != 1 {
		t.Fatalf("calls %d %d", a.Load(), b.Load())
	}
}

func TestRemoteProviderResetsKnownIDsBetweenFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := uint64(1)
		if r.URL.Query().Get("ra") == "20.5" {
			id = 2
		}
		_ = json.NewEncoder(w).Encode(sourceEnvelope{Sources: []Source{remoteSource(id)}})
	}))
	defer srv.Close()
	p, _ := NewRemoteProvider(RemoteConfig{Endpoint: srv.URL, Release: "DR3", XPRepresentation: "xp-v1"})
	q := FieldQuery{Footprint: Footprint{Center: Coordinate{RA: 10.5, Dec: 2.5}, RadiusDeg: .1}, ObservationEpoch: 2024, Release: "DR3", MagnitudeLimit: 18}
	if _, e := p.DiscoverSources(context.Background(), q); e != nil {
		t.Fatal(e)
	}
	q.Footprint.Center.RA = 20.5
	if _, e := p.DiscoverSources(context.Background(), q); e != nil {
		t.Fatal(e)
	}
	if _, e := p.RetrieveXPSpectra(context.Background(), "DR3", "xp-v1", []uint64{1}); !errors.Is(e, ErrUnknownSource) {
		t.Fatalf("old field id accepted: %v", e)
	}
}

func TestSpatialCellsWrapRA0(t *testing.T) {
	cells := spatialCells(Footprint{Center: Coordinate{RA: 359.9, Dec: 0}, RadiusDeg: .3})
	if len(cells) == 0 {
		t.Fatal("no cells")
	}
	for _, c := range cells {
		if c.center.RA < 0 || c.center.RA >= 360 {
			t.Fatalf("bad RA %v", c.center.RA)
		}
	}
	poly := spatialCells(Footprint{Polygon: []Coordinate{{RA: 359.8, Dec: -.1}, {RA: .2, Dec: -.1}, {RA: .2, Dec: .1}}})
	if len(poly) > 4 {
		t.Fatalf("meridian polygon exploded to %d cells", len(poly))
	}
}

func TestRemoteProviderSharedCacheAcrossRA0Cells(t *testing.T) {
	var mu sync.Mutex
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ra := r.URL.Query().Get("ra")
		mu.Lock()
		requests = append(requests, ra)
		mu.Unlock()
		id := uint64(359)
		if ra == "0.5" {
			id = 0
		}
		_ = json.NewEncoder(w).Encode(sourceEnvelope{Sources: []Source{remoteSource(id + 1)}})
	}))
	defer srv.Close()
	c, err := OpenCache(context.Background(), filepath.Join(t.TempDir(), "ra0.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	p, _ := NewRemoteProvider(RemoteConfig{Endpoint: srv.URL, Release: "DR3", XPRepresentation: "xp-v1", Cache: c})
	q1 := FieldQuery{Footprint: Footprint{Center: Coordinate{RA: 359.8, Dec: 2.2}, RadiusDeg: .1}, ObservationEpoch: 2024, Release: "DR3", MagnitudeLimit: 18}
	q2 := FieldQuery{Footprint: Footprint{Polygon: []Coordinate{{RA: 359.8, Dec: 2.1}, {RA: .2, Dec: 2.1}, {RA: .2, Dec: 2.3}}}, ObservationEpoch: 2024, Release: "DR3", MagnitudeLimit: 18}
	if _, err = p.DiscoverSources(context.Background(), q1); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	first := append([]string(nil), requests...)
	mu.Unlock()
	if len(first) != 1 {
		t.Fatalf("first query fetched %d cells: %v", len(first), first)
	}
	if _, err = p.DiscoverSources(context.Background(), q2); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("overlapping query fetched %d cells: %v", len(requests), requests)
	}
	if requests[0] == requests[1] {
		t.Fatalf("second query refetched canonical cell %q", requests[1])
	}
}

func TestRemoteProviderConfiguresBoundedSemaphore(t *testing.T) {
	p, err := NewRemoteProvider(RemoteConfig{Endpoint: "http://example.invalid", Release: "DR3", XPRepresentation: "xp-v1", MaxConcurrent: 2})
	if err != nil {
		t.Fatal(err)
	}
	if cap(p.sem) != 2 {
		t.Fatalf("semaphore capacity=%d", cap(p.sem))
	}
}

func TestRemoteProviderAggregatesNextPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			_ = json.NewEncoder(w).Encode(sourceEnvelope{Sources: []Source{remoteSource(2)}})
			return
		}
		_ = json.NewEncoder(w).Encode(sourceEnvelope{Sources: []Source{remoteSource(1)}, NextPage: 2})
	}))
	defer srv.Close()
	p, _ := NewRemoteProvider(RemoteConfig{Endpoint: srv.URL, Release: "DR3", XPRepresentation: "xp-v1"})
	q := FieldQuery{Footprint: Footprint{Center: Coordinate{RA: 10.5, Dec: 2.5}, RadiusDeg: .1}, ObservationEpoch: 2024, Release: "DR3", MagnitudeLimit: 18}
	s, e := p.DiscoverSources(context.Background(), q)
	if e != nil || len(s) != 2 {
		t.Fatalf("sources=%d err=%v", len(s), e)
	}
}

func TestRemoteProviderAggregatesCursorPages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cursor") == "cursor-2" {
			_ = json.NewEncoder(w).Encode(sourceEnvelope{Sources: []Source{remoteSource(2)}})
			return
		}
		_ = json.NewEncoder(w).Encode(sourceEnvelope{Sources: []Source{remoteSource(1)}, Next: "cursor-2"})
	}))
	defer srv.Close()
	p, _ := NewRemoteProvider(RemoteConfig{Endpoint: srv.URL, Release: "DR3", XPRepresentation: "xp-v1"})
	q := FieldQuery{Footprint: Footprint{Center: Coordinate{RA: 10.5, Dec: 2.5}, RadiusDeg: .1}, ObservationEpoch: 2024, Release: "DR3", MagnitudeLimit: 18}
	s, err := p.DiscoverSources(context.Background(), q)
	if err != nil || len(s) != 2 {
		t.Fatalf("sources=%d err=%v", len(s), err)
	}
}

func TestRemoteProviderConcurrentDiscoverLimitsAndSpacesRequests(t *testing.T) {
	var active, maxActive atomic.Int32
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		for {
			old := maxActive.Load()
			if n <= old || maxActive.CompareAndSwap(old, n) {
				break
			}
		}
		started <- struct{}{}
		<-release
		active.Add(-1)
		_ = json.NewEncoder(w).Encode(sourceEnvelope{Sources: []Source{remoteSource(1)}})
	}))
	defer srv.Close()
	p, _ := NewRemoteProvider(RemoteConfig{Endpoint: srv.URL, Release: "DR3", XPRepresentation: "xp-v1", MaxConcurrent: 1})
	q := FieldQuery{Footprint: Footprint{Center: Coordinate{RA: 10.5, Dec: 2.5}, RadiusDeg: .1}, ObservationEpoch: 2024, Release: "DR3", MagnitudeLimit: 18}
	done := make(chan error, 2)
	go func() { _, err := p.DiscoverSources(context.Background(), q); done <- err }()
	go func() { _, err := p.DiscoverSources(context.Background(), q); done <- err }()
	<-started
	select {
	case <-started:
		t.Fatal("second request entered while semaphore was occupied")
	case <-time.After(10 * time.Millisecond):
	}
	release <- struct{}{}
	<-started
	release <- struct{}{}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if maxActive.Load() > 1 {
		t.Fatalf("max in-flight requests=%d", maxActive.Load())
	}
}

func TestRemoteProviderPersistentNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer srv.Close()
	p, _ := NewRemoteProvider(RemoteConfig{Endpoint: srv.URL, Release: "DR3", XPRepresentation: "xp-v1", Retries: 1, RetryBackoff: time.Millisecond})
	q := FieldQuery{Footprint: Footprint{Center: Coordinate{RA: 10.5, Dec: 2.5}, RadiusDeg: .1}, ObservationEpoch: 2024, Release: "DR3", MagnitudeLimit: 18}
	if _, e := p.DiscoverSources(context.Background(), q); !errors.Is(e, ErrRemoteNetwork) {
		t.Fatalf("err=%v", e)
	}
}
