package asdfio

import (
	"math"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCompileNativeGWCSRejectsSimplifiedRegisteredGraphs(t *testing.T) {
	for _, tc := range []struct {
		name, instrument, detector, module string
		profile                            GWCSProfile
	}{
		{"MIRI", "MIRI", "MIRIMAGE", "", ProfileMIRIMIRIMAGE},
		{"NIRCam", "NIRCAM", "NRCB1", "B", ProfileNIRCamModuleB},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var root yaml.Node
			source := "meta:\n  instrument: {name: " + tc.instrument + ", detector: " + tc.detector
			if tc.module != "" {
				source += ", module: " + tc.module
			}
			source += `}
  wcs:
    steps:
      - frame: {name: detector}
        transform: !transform/compose-1.4.0 {forward: [!transform/identity-1.0.0 {}]}
      - frame: {name: v2v3}
        transform: !transform/compose-1.4.0 {forward: [!transform/identity-1.0.0 {}]}
      - frame: {name: v2v3vacorr}
        transform: !transform/compose-1.4.0 {forward: [!transform/identity-1.0.0 {}]}
      - frame: {name: world}
        transform: null
`
			if err := yaml.Unmarshal([]byte(source), &root); err != nil {
				t.Fatal(err)
			}
			_, err := (&Document{Root: &root}).CompileNativeGWCS(tc.profile)
			if err == nil {
				t.Fatal("simplified graph was admitted as native")
			}
		})
	}
}

func TestCompileMIRIGWCSRealFixture(t *testing.T) {
	path := filepath.Join("..", "..", "TestImages", "asdf", "jw09548001001_02101_00001_mirimage_cal.asdf")
	d, err := Open(path)
	if err != nil {
		t.Skipf("real ASDF fixture unavailable: %v", err)
	}
	m, err := d.CompileMIRIGWCS()
	if err != nil {
		t.Fatalf("CompileMIRIGWCS: %v", err)
	}
	for _, p := range [][2]float64{{0, 0}, {512, 512}, {1031, 1023}} {
		ra, dec, err := m.PixelToICRS(p[0], p[1])
		if err != nil || math.IsNaN(ra) || math.IsNaN(dec) || math.IsInf(ra, 0) || math.IsInf(dec, 0) {
			t.Fatalf("PixelToICRS(%v): ra=%v dec=%v err=%v", p, ra, dec, err)
		}
		if ra < 0 || ra >= 360 || dec < -90 || dec > 90 {
			t.Fatalf("invalid ICRS coordinates ra=%v dec=%v", ra, dec)
		}
	}
}

func TestCompileNIRCamModuleBGWCSRealFixture(t *testing.T) {
	path := filepath.Join("..", "..", "TestImages", "asdf", "nircam", "jw09548002001_02101_00001_nrcb1_cal.asdf")
	d, err := Open(path)
	if err != nil {
		t.Skipf("real ASDF fixture unavailable: %v", err)
	}
	m, err := d.CompileNativeGWCS(ProfileNIRCamModuleB)
	if err != nil {
		t.Fatalf("CompileNativeGWCS: %v", err)
	}
	for _, p := range [][2]float64{{0, 0}, {512, 512}, {2047, 2047}} {
		ra, dec, err := m.PixelToICRS(p[0], p[1])
		if err != nil || math.IsNaN(ra) || math.IsNaN(dec) || math.IsInf(ra, 0) || math.IsInf(dec, 0) {
			t.Fatalf("PixelToICRS(%v): ra=%v dec=%v err=%v", p, ra, dec, err)
		}
		if ra < 0 || ra >= 360 || dec < -90 || dec > 90 {
			t.Fatalf("invalid ICRS coordinates ra=%v dec=%v", ra, dec)
		}
	}
}

// TestNativeGWCSRealFixtureGoldens compares the Go evaluator with coordinates
// produced independently by the serialized GWCS through the official Python
// stack (asdf 5.3.1, gwcs 1.0.3):
//
//	$env:UV_CACHE_DIR='...'; uv run --with asdf --with gwcs python <evaluator>
//
// The evaluator opened each ASDF file with asdf.open, called meta.wcs(x, y),
// and recorded the returned ICRS degree values. Goldens are intentionally
// immutable and are not generated from the Go implementation. The 1e-7
// degree tolerance is approximately 0.36 milliarcseconds.
func TestNativeGWCSRealFixtureGoldens(t *testing.T) {
	type golden struct {
		name    string
		profile GWCSProfile
		points  [][4]float64 // x, y, expected RA, expected Dec (degrees)
	}
	data := []golden{
		{
			name:    filepath.Join("..", "..", "TestImages", "asdf", "jw09548001001_02101_00001_mirimage_cal.asdf"),
			profile: ProfileMIRIMIRIMAGE,
			points: [][4]float64{
				{0, 0, 112.319065607367065, 20.930357377483059},
				{515.5, 511.5, 112.298825554452250, 20.918761328308108},
				{1031, 1023, 112.278556736668918, 20.907274157188045},
				{257.75, 767.25, 112.292645917413836, 20.928298698983465},
				{773.25, 255.75, 112.305019274675459, 20.909151839228965},
				{500.125, 900.875, 112.286473418154685, 20.922099331351756},
			},
		},
		{
			name:    filepath.Join("..", "..", "TestImages", "asdf", "nircam", "jw09548002001_02101_00001_nrcb1_cal.asdf"),
			profile: ProfileNIRCamModuleB,
			points: [][4]float64{
				{0, 0, 112.291552582445306, 20.909399946170495},
				{1023.5, 1023.5, 112.280733513853434, 20.902196946909605},
				{2047, 2047, 112.269945090674469, 20.895062052548642},
				{256.25, 1536.75, 112.277244025168940, 20.909355889390607},
				{1536.75, 256.25, 112.286884108486674, 20.896772712574851},
				{1000.125, 1800.875, 112.273759254782277, 20.903480730033081},
			},
		},
		{
			name:    filepath.Join("..", "..", "TestImages", "asdf", "nircam", "jw09548002001_02101_00001_nrcb2_cal.asdf"),
			profile: ProfileNIRCamModuleB,
			points: [][4]float64{
				{0, 0, 112.311665050944185, 20.906352343076652},
				{1023.5, 1023.5, 112.300599834067086, 20.899137495109265},
				{2047, 2047, 112.289626930136677, 20.892006615468500},
				{256.25, 1536.75, 112.297112079515799, 20.906418814760784},
				{1536.75, 256.25, 112.306823032110401, 20.893577446372827},
				{1000.125, 1800.875, 112.293538133730010, 20.900497914996464},
			},
		},
		{
			name:    filepath.Join("..", "..", "TestImages", "asdf", "nircam", "jw09548002001_02101_00001_nrcb3_cal.asdf"),
			profile: ProfileNIRCamModuleB,
			points: [][4]float64{
				{0, 0, 112.294704857557349, 20.928195169697322},
				{1023.5, 1023.5, 112.283937439026772, 20.920795743220513},
				{2047, 2047, 112.273235413789038, 20.913513265869508},
				{256.25, 1536.75, 112.280319323018617, 20.927952333260073},
				{1536.75, 256.25, 112.290193819219198, 20.915437967552684},
				{1000.125, 1800.875, 112.276915965925141, 20.921997559706959},
			},
		},
		{
			name:    filepath.Join("..", "..", "TestImages", "asdf", "nircam", "jw09548002001_02101_00001_nrcb4_cal.asdf"),
			profile: ProfileNIRCamModuleB,
			points: [][4]float64{
				{0, 0, 112.314911224611109, 20.925334027818671},
				{1023.5, 1023.5, 112.303891284471746, 20.917857024117364},
				{2047, 2047, 112.293000890874254, 20.910518170695205},
				{256.25, 1536.75, 112.300263085831517, 20.925107708846646},
				{1536.75, 256.25, 112.310233168929898, 20.912408370131740},
				{1000.125, 1800.875, 112.296772401045885, 20.919089508298267},
			},
		},
		{
			name:    filepath.Join("..", "..", "TestImages", "asdf", "nircam", "jw09548002001_02101_00001_nrcblong_cal.asdf"),
			profile: ProfileNIRCamModuleB,
			points: [][4]float64{
				{0, 0, 112.314458079094521, 20.924800888943643},
				{1023.5, 1023.5, 112.292174572810751, 20.909767250593866},
				{2047, 2047, 112.270193596012390, 20.895134927893984},
				{256.25, 1536.75, 112.284893857734417, 20.924383842385168},
				{1536.75, 256.25, 112.304885079488258, 20.898668197782218},
				{1000.125, 1800.875, 112.277890380673639, 20.912313135479270},
			},
		},
	}
	const tolerance = 1e-7
	for _, tc := range data {
		t.Run(filepath.Base(tc.name), func(t *testing.T) {
			d, err := Open(tc.name)
			if err != nil {
				t.Skipf("real ASDF fixture unavailable: %v", err)
			}
			m, err := d.CompileNativeGWCS(tc.profile)
			if err != nil {
				t.Fatalf("CompileNativeGWCS(%s): %v", tc.profile, err)
			}
			for _, p := range tc.points {
				ra, dec, err := m.PixelToICRS(p[0], p[1])
				if err != nil {
					t.Fatalf("PixelToICRS(%v,%v): %v", p[0], p[1], err)
				}
				if math.Abs(ra-p[2]) > tolerance || math.Abs(dec-p[3]) > tolerance {
					t.Errorf("PixelToICRS(%v,%v) = (%0.15f,%0.15f), want (%0.15f,%0.15f) within %g degrees", p[0], p[1], ra, dec, p[2], p[3], tolerance)
				}
			}
		})
	}
}
