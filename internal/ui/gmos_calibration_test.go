package ui

import (
	"testing"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/mosaic"
	"gofitsv3/internal/processing"
)

func TestPreserveGMOSInputState(t *testing.T) {
	src := mosaic.Input{OffsetX: 2.5, OffsetY: -1.25, HasManualTransform: true,
		ManualTransform: processing.AffineTransform{A: 1, F: 3}, OffsetLocked: true,
		Excluded: true, NormalizeExposure: true, ExposureScale: 4.5}
	dst := mosaic.Input{}
	preserveGMOSInputState(&dst, src)
	if dst.OffsetX != src.OffsetX || dst.OffsetY != src.OffsetY || dst.ManualTransform != src.ManualTransform ||
		dst.HasManualTransform != src.HasManualTransform || dst.OffsetLocked != src.OffsetLocked || dst.Excluded != src.Excluded ||
		dst.NormalizeExposure != src.NormalizeExposure || dst.ExposureScale != src.ExposureScale {
		t.Fatalf("state was not preserved: got %#v want %#v", dst, src)
	}
}

func TestGMOSReferencePathAndStaleGuardHelpers(t *testing.T) {
	if got := gmosReferencePath(nil); got != "" {
		t.Fatalf("nil reference path = %q", got)
	}
	in := mosaic.Input{Path: "working.fits", SourcePath: "source.fits"}
	if got := gmosReferencePath(&in); got != "source.fits" {
		t.Fatalf("source path = %q", got)
	}
	if !sameStringSlice([]string{"a", "b"}, []string{"a", "b"}) || sameStringSlice([]string{"a"}, []string{"b"}) {
		t.Fatal("stale guard helper comparison is incorrect")
	}
}

func TestGMOSReferenceIdentityIncludesSCIExt(t *testing.T) {
	a := &mosaic.Input{SourcePath: "reference.fits", SCIExt: 1}
	b := &mosaic.Input{SourcePath: "reference.fits", SCIExt: 2}
	c := &mosaic.Input{SourcePath: "reference.fits", SCIExt: 1}
	if gmosReferenceIdentity(a) == gmosReferenceIdentity(b) {
		t.Fatal("different SCI extensions have the same reference identity")
	}
	if gmosReferenceIdentity(a) != gmosReferenceIdentity(c) {
		t.Fatal("identical reference chips have different identities")
	}
}

func TestReloadGMOSInputsRejectsIncompleteSelection(t *testing.T) {
	_, _, err := reloadGMOSInputs(nil, []mosaic.Input{{Path: "source.fits"}}, mosaic.GMOSCalibrationSelection{}, nil)
	if err == nil {
		t.Fatal("incomplete GMOS selection was accepted")
	}
}

func TestGMOSCalibrationEligibilityIgnoresReference(t *testing.T) {
	science := []mosaic.Input{{PrimaryHeader: mosaicTestGMOSHeader()}}
	if !gmosCalibrationEligible(science, nil, false) {
		t.Fatal("GMOS science input was not eligible")
	}
	ref := &mosaic.Input{Path: "arbitrary-reference.fits", PrimaryHeader: fitsio.Header{Cards: map[string]string{"INSTRUME": "'HST'"}}}
	if !gmosCalibrationEligible(science, ref, false) { // arbitrary reference is intentionally not consulted
		t.Fatal("GMOS science input with a reference was not eligible")
	}
}

func TestGMOSCalibrationEligibilityRejectsQueueAndNonGMOS(t *testing.T) {
	if gmosCalibrationEligible(nil, nil, false) || gmosCalibrationEligible([]mosaic.Input{{PrimaryHeader: mosaicTestGMOSHeader()}}, nil, true) {
		t.Fatal("empty or queued workspace was eligible")
	}
	nonGMOS := mosaic.Input{PrimaryHeader: fitsio.Header{Cards: map[string]string{"INSTRUME": "'HST'"}}}
	if gmosCalibrationEligible([]mosaic.Input{nonGMOS}, nil, false) ||
		gmosCalibrationEligible([]mosaic.Input{{PrimaryHeader: mosaicTestGMOSHeader()}, nonGMOS}, nil, false) {
		t.Fatal("non-GMOS or mixed workspace was eligible")
	}
}

func TestValidateGMOSWorkspaceInputsRejectsMixedFilters(t *testing.T) {
	a := mosaic.Input{Path: "g.fits", PrimaryHeader: mosaicTestGMOSHeaderWithFilter("g_G0301")}
	b := mosaic.Input{Path: "r.fits", PrimaryHeader: mosaicTestGMOSHeaderWithFilter("r_G0303")}
	if err := validateGMOSWorkspaceInputs([]mosaic.Input{a, b}, a); err == nil {
		t.Fatal("mixed GMOS filters were accepted")
	}
}

func TestValidateGMOSRecipeForScienceRejectsMismatch(t *testing.T) {
	sel := mosaic.GMOSCalibrationSelection{Flat: &mosaic.GMOSCalibrationFrame{Filter: "r_G0303", Key: "detector-b"}}
	if err := validateGMOSRecipeForScience("g_G0301", "detector-a", sel); err == nil {
		t.Fatal("mismatched persisted recipe was accepted")
	}
}

func TestValidateGMOSRecipeForScienceAcceptsMatchingRecipe(t *testing.T) {
	sel := mosaic.GMOSCalibrationSelection{Flat: &mosaic.GMOSCalibrationFrame{Filter: "r_G0303", Key: "detector-a"}}
	if err := validateGMOSRecipeForScience("r_G0303", "detector-a", sel); err != nil {
		t.Fatalf("matching persisted recipe rejected: %v", err)
	}
}

func mosaicTestGMOSHeader() fitsio.Header { return mosaicTestGMOSHeaderWithFilter("r_G0303") }

func mosaicTestGMOSHeaderWithFilter(filter string) fitsio.Header {
	return fitsio.Header{Cards: map[string]string{"INSTRUME": "'GMOS-N'", "DETECTOR": "'EEV'", "CCDSUM": "'2 2'", "FILTER": "'" + filter + "'"}}
}
