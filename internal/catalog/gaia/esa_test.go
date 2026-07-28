package gaia

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

const esaFixture = `{"metadata":[{"name":"source_id"},{"name":"ra"},{"name":"dec"},{"name":"ref_epoch"},{"name":"pmra"},{"name":"pmdec"},{"name":"ra_error"},{"name":"dec_error"},{"name":"pmra_error"},{"name":"pmdec_error"},{"name":"phot_g_mean_mag"},{"name":"phot_bp_mean_mag"},{"name":"phot_rp_mean_mag"},{"name":"phot_g_mean_flux_over_error"},{"name":"phot_bp_mean_flux_over_error"},{"name":"phot_rp_mean_flux_over_error"}],"data":[[123456789012345678,12.5,-4.25,2016,1.2,-2.3,0.1,0.2,0.3,0.4,12,12.5,11.8,100,80,90]]}`

func testESAQuery() FieldQuery {
	return FieldQuery{Footprint: Footprint{Center: Coordinate{RA: 12.5, Dec: -4.25}, RadiusDeg: .1}, ObservationEpoch: 2024, Release: "DR3", MagnitudeLimit: 18}
}

func TestESAProviderPostsSafeDR3QueryAndCachesCompleteRows(t *testing.T) {
	var gotForm url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tap/sync" || r.Method != http.MethodPost {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		_ = r.ParseForm()
		gotForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(esaFixture))
	}))
	defer server.Close()
	cache, err := OpenCache(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	p, err := NewESAProvider(ESAConfig{Endpoint: server.URL + "/tap", Release: "DR3", XPRepresentation: "xp-v1", Cache: cache})
	if err != nil {
		t.Fatal(err)
	}
	cell := QueryCell{Signature: "sig", SpatialCell: "cell", Release: "DR3"}
	if err := cache.PutCell(context.Background(), CellBatch{Cell: cell, Sources: []Source{testSource(7)}}); err != nil {
		t.Fatal(err)
	}
	p.sourceCell[7] = cell
	sources, err := p.DiscoverSources(context.Background(), testESAQuery())
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].SourceID != 123456789012345678 {
		t.Fatalf("sources = %+v", sources)
	}
	if !strings.Contains(gotForm.Get("QUERY"), "gaiadr3.gaia_source") || strings.Contains(gotForm.Get("QUERY"), ";") {
		t.Fatalf("unsafe/unexpected ADQL: %q", gotForm.Get("QUERY"))
	}
	if gotForm.Get("FORMAT") != "json" || gotForm.Get("LANG") != "ADQL" {
		t.Fatalf("form = %v", gotForm)
	}
	complete, err := cache.IsCellComplete(context.Background(), p.cellFor(testESAQuery(), spatialCells(testESAQuery().Footprint)[0].id))
	if err != nil || !complete {
		t.Fatalf("cache complete=%v err=%v", complete, err)
	}
}

func TestESAProviderDistinguishesTAPErrorsAndDoesNotCache(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"ADQL syntax error"}`))
	}))
	defer server.Close()
	cache, err := OpenCache(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	p, err := NewESAProvider(ESAConfig{Endpoint: server.URL, Release: "DR3", XPRepresentation: "xp-v1", Cache: cache})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DiscoverSources(context.Background(), testESAQuery()); !errors.Is(err, ErrESATAPHTTP) {
		t.Fatalf("err=%v", err)
	}
	complete, err := cache.IsCellComplete(context.Background(), p.cellFor(testESAQuery(), spatialCells(testESAQuery().Footprint)[0].id))
	if err != nil {
		t.Fatal(err)
	}
	if complete {
		t.Fatal("TAP failure marked cache cell complete")
	}
}

func TestESAProviderReportsTAPErrorPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":"query rejected"}`))
	}))
	defer server.Close()
	p, err := NewESAProvider(ESAConfig{Endpoint: server.URL, Release: "DR3", XPRepresentation: "xp-v1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DiscoverSources(context.Background(), testESAQuery()); !errors.Is(err, ErrESATAPQuery) {
		t.Fatalf("err=%v", err)
	}
}

func TestESAProviderRejectsIncompleteRowsAndXP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"metadata":[{"name":"source_id"}],"data":[[1]]}`))
	}))
	defer server.Close()
	p, err := NewESAProvider(ESAConfig{Endpoint: server.URL, Release: "DR3", XPRepresentation: "xp-v1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DiscoverSources(context.Background(), testESAQuery()); !errors.Is(err, ErrESATAPDecode) {
		t.Fatalf("err=%v", err)
	}
}

func TestESAProviderRetrievesSampledCSV(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/data-server/data" || r.URL.Query().Get("RETRIEVAL_TYPE") != "XP_SAMPLED" || r.URL.Query().Get("ID") != "7" {
			t.Fatalf("request = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "text/csv")
		_, _ = w.Write([]byte("source_id,wavelength,flux,flux_error\n7,400,1,.1\n7,500,2,.2\n"))
	}))
	defer server.Close()
	p, err := NewESAProvider(ESAConfig{Endpoint: server.URL + "/tap", Release: "DR3", XPRepresentation: "xp-v1"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.RetrieveXPSpectra(context.Background(), "DR3", "xp-v1", []uint64{7})
	if err != nil || len(got) != 1 || len(got[0].Flux) != 2 {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if _, err = p.RetrieveXPSpectra(context.Background(), "DR3", "xp-v1", []uint64{7}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("network calls=%d", calls)
	}
}

func TestESAProviderRejectsOversizedXPResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("source_id,wavelength,flux,flux_error\n1,1,1,1\n"))
	}))
	defer server.Close()
	p, err := NewESAProvider(ESAConfig{Endpoint: server.URL, Release: "DR3", XPRepresentation: "xp-v1", MaxResponseBytes: 8})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.RetrieveXPSpectra(context.Background(), "DR3", "xp-v1", []uint64{1}); !errors.Is(err, ErrRemoteLimit) {
		t.Fatalf("err=%v", err)
	}
}

func TestESAProviderRejectsMissingBatchID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("source_id,wavelength,flux,flux_error\n1,400,1,.1\n1,500,2,.2\n"))
	}))
	defer server.Close()
	p, _ := NewESAProvider(ESAConfig{Endpoint: server.URL, Release: "DR3", XPRepresentation: "xp-v1"})
	if _, err := p.RetrieveXPSpectra(context.Background(), "DR3", "xp-v1", []uint64{1, 2}); !errors.Is(err, ErrESATAPDecode) {
		t.Fatalf("err=%v", err)
	}
}

func TestESAProviderOnlineThenCacheOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gaia.db")
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/tap/sync" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(esaFixture))
			return
		}
		w.Header().Set("Content-Type", "text/csv")
		_, _ = w.Write([]byte("source_id,wavelength,flux,flux_error\n123456789012345678,400,1,.1\n123456789012345678,500,2,.2\n"))
	}))
	defer srv.Close()
	c, _ := OpenCache(context.Background(), path)
	defer c.Close()
	p, _ := NewESAProvider(ESAConfig{Endpoint: srv.URL + "/tap", Release: "DR3", XPRepresentation: "xp-v1", Cache: c})
	q := testESAQuery()
	if _, err := p.DiscoverSources(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	if _, err := p.RetrieveXPSpectra(context.Background(), "DR3", "xp-v1", []uint64{123456789012345678}); err != nil {
		t.Fatal(err)
	}
	before := calls
	c2, _ := OpenCache(context.Background(), path)
	defer c2.Close()
	off, _ := NewESAProvider(ESAConfig{Endpoint: "http://127.0.0.1:1", Release: "DR3", XPRepresentation: "xp-v1", Mode: AccessCacheOnly, Cache: c2})
	if _, err := off.DiscoverSources(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	if _, err := off.RetrieveXPSpectra(context.Background(), "DR3", "xp-v1", []uint64{123456789012345678}); err != nil {
		t.Fatal(err)
	}
	if calls != before {
		t.Fatalf("cache-only contacted network: %d -> %d", before, calls)
	}
}

func TestESAProviderRetrievesZIPForMultipleIDs(t *testing.T) {
	var body bytes.Buffer
	zw := zip.NewWriter(&body)
	for _, id := range []string{"1", "2"} {
		f, _ := zw.Create(id + ".csv")
		_, _ = f.Write([]byte("source_id,wavelength,flux,flux_error\n" + id + ",400,1,.1\n" + id + ",500,2,.2\n"))
	}
	_ = zw.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(body.Bytes())
	}))
	defer server.Close()
	p, err := NewESAProvider(ESAConfig{Endpoint: server.URL, Release: "DR3", XPRepresentation: "xp-v1"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.RetrieveXPSpectra(context.Background(), "DR3", "xp-v1", []uint64{1, 2})
	if err != nil || len(got) != 2 {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestESAProviderRejectsZIPAggregateBudget(t *testing.T) {
	var body bytes.Buffer
	zw := zip.NewWriter(&body)
	for _, id := range []string{"1", "2"} {
		f, _ := zw.Create(id + ".csv")
		for i := 0; i < 20; i++ {
			_, _ = f.Write([]byte("source_id,wavelength,flux,flux_error\n" + id + ",400,1,.1\n"))
		}
	}
	_ = zw.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(body.Bytes())
	}))
	defer srv.Close()
	p, _ := NewESAProvider(ESAConfig{Endpoint: srv.URL, Release: "DR3", XPRepresentation: "xp-v1", MaxResponseBytes: 500})
	if _, err := p.RetrieveXPSpectra(context.Background(), "DR3", "xp-v1", []uint64{1, 2}); !errors.Is(err, ErrRemoteLimit) {
		t.Fatalf("err=%v", err)
	}
}

func TestESAProviderCancellation(t *testing.T) {
	p, err := NewESAProvider(ESAConfig{Endpoint: "http://127.0.0.1:1", Release: "DR3", XPRepresentation: "xp-v1"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.DiscoverSources(ctx, testESAQuery()); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}
