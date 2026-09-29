package processing

import (
	"context"
	"math"
	"testing"
)

func TestDeriveStarTreatmentFitsBorrowsGeometryAndMeasuresBackground(t *testing.T) {
	const w, h, cx, cy = 90, 80, 45, 40
	imgs, reference := neutralizeScene(t, w, h, cx, cy) // red channel fits; blue has a different background and amplitude
	sources := []StarMapSource{{ID: 1, X: cx, Y: cy, FWHM: 3.8, Radius: 7, Status: "accepted"}}
	blue := imgs[2].HDU.Data.Pixels
	derived, err := DeriveStarTreatmentFits(context.Background(), reference, sources, blue, w, h, StarTreatmentOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(derived) != 1 || !derived[0].Usable {
		t.Fatalf("derived fit not usable: %+v", derived)
	}
	ref, d := reference[0], derived[0]
	if d.X != ref.X || d.Y != ref.Y || d.InnerRadius != ref.InnerRadius || d.OuterRadius != ref.OuterRadius || d.CoreRadius != ref.CoreRadius || d.HaloValidated != ref.HaloValidated {
		t.Fatalf("geometry changed: ref=%+v derived=%+v", ref, d)
	}
	// Blue background is .10 + .0005*x at the star; red was .30 + .0005*x.
	if math.Abs(d.Background-(.10+.0005*cx)) > .01 {
		t.Fatalf("blue background not re-measured: %g", d.Background)
	}
	if math.Abs(ref.Background-(.30+.0005*cx)) > .01 {
		t.Fatalf("reference background unexpected: %g", ref.Background)
	}
	if d.Companions != nil {
		t.Fatal("reference-unit companions must not be carried over")
	}
	model, err := NewStarTreatmentModel(derived, w, h, *imgs[2], .75)
	if err != nil {
		t.Fatal(err)
	}
	center := int(cy)*w + int(cx)
	plain := stretchDiskValue(blue[center], *imgs[2])
	if got := model.TreatedStretch(blue[center], cx, cy); got >= plain {
		t.Fatalf("derived model did not compress the blue core: %v vs %v", got, plain)
	}
	// An unusable reference fit stays unusable with its own reason.
	out, err := DeriveStarTreatmentFits(context.Background(), []StarTreatmentFit{{Usable: false, Reason: "not accepted"}}, sources, blue, w, h, StarTreatmentOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Usable || out[0].Reason != "not accepted" {
		t.Fatalf("unusable handling wrong: %+v", out)
	}
}
