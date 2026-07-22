package ui

import (
	"bytes"
	"strings"
	"testing"

	"gofitsv3/internal/mosaic"
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
	want := "image_name,aligned,message\nok.fits,true,\nbad.fits,false,\"not enough stars, retry\"\n"
	if strings.ReplaceAll(out.String(), "\r\n", "\n") != want {
		t.Fatalf("CSV = %q, want %q", out.String(), want)
	}
}
