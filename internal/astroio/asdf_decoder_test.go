package astroio

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCollectJWSTCardsRejectsUnsupportedGWCSGraph(t *testing.T) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(`meta:
  instrument: {name: MIRI, detector: MIRIMAGE}
  wcs:
    steps:
      - frame: {name: detector}
        transform: !transform/compose-1.4.0 {forward: []}
      - frame: {name: v2v3}
        transform: !transform/compose-1.4.0 {forward: []}
      - frame: {name: v2v3vacorr}
        transform: !transform/compose-1.4.0 {forward: []}
      - frame: {name: world}
        transform: null
`), &root); err != nil {
		t.Fatal(err)
	}
	cards := map[string]string{}
	collectJWSTCards(&root, cards)
	if _, ok := cards["GWCSMODEL"]; ok {
		t.Fatal("unsupported/incomplete GWCS graph was marked native")
	}
}

func TestCollectJWSTCardsAcceptsRealMIRIModelGraph(t *testing.T) {
	path := filepath.Join("..", "..", "TestImages", "asdf", "jw09548001001_02101_00001_mirimage_cal.asdf")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("real ASDF fixture unavailable: %v", err)
	}
	s, err := (ASDFDecoder{}).Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Metadata(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(m.Cards["GWCSMODEL"], "MIRI_NATIVE_GWCS") {
		t.Fatalf("real MIRI graph marker = %q", m.Cards["GWCSMODEL"])
	}
}

func TestCollectJWSTCardsAcceptsRealNIRCamModuleBModelGraph(t *testing.T) {
	path := filepath.Join("..", "..", "TestImages", "asdf", "nircam", "jw09548002001_02101_00001_nrcb1_cal.asdf")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("real ASDF fixture unavailable: %v", err)
	}
	s, err := (ASDFDecoder{}).Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Metadata(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m.NativeGWCS == nil || m.Cards["GWCSMODEL"] != "NIRCAM_NATIVE_GWCS" {
		t.Fatalf("native=%v model=%q", m.NativeGWCS != nil, m.Cards["GWCSMODEL"])
	}
	if m.Cards["INSTRUME"] != "NIRCAM" || m.Cards["DETECTOR"] != "NRCB1" || m.Cards["FILTER"] != "F187N" {
		t.Fatalf("nested metadata not extracted: %+v", m.Cards)
	}
}

func asdfTestFile(t *testing.T, name string) string {
	t.Helper()
	h := []byte("#ASDF 1.0.0\n#ASDF_STANDARD 1.6.0\ndata: !core/ndarray-1.0.0 {source: 0, datatype: float32, byteorder: little, shape: [2, 2]}\n...\n")
	b := make([]byte, 54+24)
	binary.BigEndian.PutUint32(b, 0xd3424c4b)
	binary.BigEndian.PutUint16(b[4:], 48)
	binary.BigEndian.PutUint64(b[14:], 16)
	binary.BigEndian.PutUint64(b[22:], 16)
	binary.BigEndian.PutUint64(b[30:], 16)
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, append(h, b...), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCollectScalarCardsFindsJWSTWCSInfo(t *testing.T) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte("meta:\n  wcsinfo:\n    crpix1: 12.5\n    crpix2: 13.5\n    crval1: 10.25\n    crval2: -4.5\n    cdelt1: -0.00001\n    cdelt2: 0.00001\n"), &root); err != nil {
		t.Fatal(err)
	}
	cards := map[string]string{}
	collectScalarCards(&root, cards)
	for key, want := range map[string]string{"CRPIX1": "12.5", "CRVAL2": "-4.5", "CDELT1": "-0.00001"} {
		if cards[key] != want {
			t.Fatalf("card %s=%q, want %q", key, cards[key], want)
		}
	}
}

func TestASDFReadPlaneHonorsCanceledContext(t *testing.T) {
	s, err := (ASDFDecoder{}).Open(context.Background(), asdfTestFile(t, "sample.bin"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.ReadPlane(ctx, PlaneID("asdf:data"), ReadOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestASDFMagicProbeIgnoresExtension(t *testing.T) {
	p := asdfTestFile(t, "sample.data")
	matched, err := (ASDFDecoder{}).Probe(p)
	if err != nil || !matched {
		t.Fatalf("matched=%v err=%v", matched, err)
	}
	if _, err := Open(context.Background(), p); err != nil {
		t.Fatal(err)
	}
}
