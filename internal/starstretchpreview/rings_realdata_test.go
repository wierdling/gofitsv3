package starstretchpreview

import (
	"context"
	"path/filepath"
	"testing"

	"gofitsv3/internal/models"
)

// This opt-in regression exercises the actual reported source, including
// footprint preparation and rendering, rather than just profile acceptance.
func TestRealDataTrifid1270DiffractionRings(t *testing.T) {
	root := realDataRoot(t)
	set := realDataSet{"trifid-f673n", filepath.Join(root, "Trifid", "F673N_drizzle.fits"), filepath.Join(root, "Trifid", "working", "F673N_starmap.fits")}
	hdu, product := loadRealData(t, set)
	src := realDataSource(t, product, 1270)
	for _, tc := range []struct {
		name string
		meta models.LoadedImage
	}{{"mtf", realDataTrifidMTF}, {"asinh", realDataAsinh}} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := previewSource(context.Background(), hdu.Data, product.Map, src, tc.meta, .75)
			if err != nil {
				t.Fatal(err)
			}
			f := assertTreatedPreview(t, p, "Trifid 1270 "+tc.name)
			if !f.RingValidated || f.WingModel != "EmpiricalRing" {
				t.Fatalf("missing diffraction-ring validation: %+v", f)
			}
			if f.Saturated != src.Saturated {
				t.Fatalf("ring fitting changed catalog saturation status: source=%v fit=%v", src.Saturated, f.Saturated)
			}
			t.Logf("model=%s residual=%.3f halo=%.1f spikes=%d", f.WingModel, f.Residual, f.HaloRadius, len(f.Spikes))
		})
	}
}
