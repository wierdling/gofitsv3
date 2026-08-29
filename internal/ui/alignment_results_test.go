package ui

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"gofitsv3/internal/mosaic"
	"gofitsv3/internal/processing"
)

func TestBuildAlignmentResultRowsFollowsInputOrder(t *testing.T) {
	inputs := []mosaic.Input{
		{Path: "first.fits"},
		{Path: "excluded.fits", Excluded: true},
		{Path: "locked.fits", OffsetLocked: true},
		{Path: "last.fits"},
	}
	results := []mosaic.StarAlignmentResult{
		{Applied: true},
		{Applied: false, Error: "locked result"},
		{Applied: true},
	}

	rows := buildAlignmentResultRows(inputs, results, false)
	if len(rows) != 2 || rows[0].stateIdx != 0 || rows[1].stateIdx != 3 {
		t.Fatalf("rows = %+v, want state indexes [0, 3]", rows)
	}
	if !rows[1].result.Applied {
		t.Fatal("last input result was not mapped in input order")
	}
}

func TestWriteAlignmentCSVIncludesNameStatusAndMessage(t *testing.T) {
	inputs := []mosaic.Input{{Path: "ok.fits"}, {Path: "bad.fits"}}
	rows := []alignmentResultRow{
		{stateIdx: 0, result: mosaic.StarAlignmentResult{Applied: true}},
		{stateIdx: 1, result: mosaic.StarAlignmentResult{Error: "not enough stars, retry"}},
	}
	var out bytes.Buffer
	if err := writeAlignmentCSV(&out, rows, inputs); err != nil {
		t.Fatal(err)
	}
	got := strings.ReplaceAll(out.String(), "\r\n", "\n")
	if !strings.Contains(got, "detected_source,detected_reference,matched,accepted,rejected") || !strings.Contains(got, "ok.fits,true,") || !strings.Contains(got, "bad.fits,false,\"not enough stars, retry\"") {
		t.Fatalf("CSV = %q, missing enriched alignment fields", got)
	}
}

func TestWriteAlignmentResidualCSV(t *testing.T) {
	var out bytes.Buffer
	encoded, _ := json.Marshal([]processing.Residual{{X: 2, Y: 3, DX: .25, DY: -.5, Radial: .559}})
	row := alignmentResultRow{result: mosaic.StarAlignmentResult{Residuals: string(encoded)}}
	if err := writeAlignmentResidualCSV(&out, row, "frame.fits"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ReplaceAll(out.String(), "\r\n", "\n"), "frame.fits,2,3,0,0,0.25,-0.5") {
		t.Fatalf("unexpected residual CSV: %q", out.String())
	}
}
