package processing

import (
	"context"
	"math"
	"testing"
)

func TestFootprintStructuredResidualGuardRejectsOffsetKnot(t *testing.T) {
	const w, h = 81, 81
	pixels := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x-40), float64(y-40)
			knot := .18 * math.Exp(-.5*((dx-4)*(dx-4)+dy*dy)/(3.8*3.8))
			star := 4 * math.Exp(-.5*(dx*dx+dy*dy)/(1.5*1.5))
			pixels[y*w+x] = float32(.25 + knot + star)
		}
	}
	f := StarTreatmentFit{X: 40, Y: 40, Background: .25, Signal: 4, Sigma: 1.56, Noise: .0005}
	if !footprintHasStructuredResidual(context.Background(), pixels, w, h, f, 8, 12.8) {
		t.Fatal("offset nebular residual was not identified")
	}
}

func TestFootprintStructuredResidualGuardKeepsSymmetricHalo(t *testing.T) {
	const w, h = 81, 81
	pixels := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x-40), float64(y-40)
			pixels[y*w+x] = float32(.2 + 5*math.Exp(-.5*(dx*dx+dy*dy)/(3.1*3.1)))
		}
	}
	f := StarTreatmentFit{X: 40, Y: 40, Background: .2, Signal: 5, Sigma: 3.1, Noise: .0005}
	if footprintHasStructuredResidual(context.Background(), pixels, w, h, f, 8, 12.8) {
		t.Fatal("symmetric stellar halo was incorrectly identified as structured")
	}
}
