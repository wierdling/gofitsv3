package mosaic

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/processing"
)

func TestPlanSkysubCancellationStopsBeforeLoading(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := 0
	planned := []plannedInput{{input: Input{Path: "a.fits", HDU: fitsio.HDU{Data: fitsio.ImageData{Width: 2, Height: 2}}}, sourceToRef: processing.IdentityTransform()}}
	_, _, _, _, err := planSkysub(planned, Options{Ctx: ctx, Skysub: SkysubOptions{Enabled: true}, FrameLoader: func(Input) ([]float32, []float32, error) {
		called++
		return []float32{1, 1, 1, 1}, nil, nil
	}})
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("planSkysub error = %v, want ErrCancelled", err)
	}
	if called != 0 {
		t.Fatalf("loader called %d times after cancellation", called)
	}
}

func TestFrameLoaderCtxDefaultOptionsGetsBackgroundContext(t *testing.T) {
	called := false
	p := plannedInput{input: Input{Path: "frame.fits"}}
	_, _, _, owned, err := resolveFramePixels(p.input, Options{
		FrameLoaderCtx: func(ctx context.Context, _ Input) ([]float32, []float32, error) {
			called = ctx != nil
			return []float32{1}, nil, nil
		},
	})
	if err != nil {
		t.Fatalf("resolveFramePixels error = %v", err)
	}
	if !called || !owned {
		t.Fatalf("FrameLoaderCtx called=%v owned=%v, want called with non-nil context and owned=true", called, owned)
	}
}

func TestPlanSkysubCancellationUnblocksStartedLoader(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	planned := make([]plannedInput, 1)
	for i := range planned {
		planned[i] = plannedInput{input: Input{Path: "frame.fits", HDU: fitsio.HDU{Data: fitsio.ImageData{Width: 2, Height: 2}}}, sourceToRef: processing.IdentityTransform()}
	}
	started := make(chan struct{}, 1)
	finished := make(chan struct{}, 1)
	loader := func(loadCtx context.Context, _ Input) ([]float32, []float32, error) {
		started <- struct{}{}
		<-loadCtx.Done()
		finished <- struct{}{}
		return nil, nil, loadCtx.Err()
	}
	done := make(chan error, 1)
	go func() {
		_, _, _, _, err := planSkysub(planned, Options{Ctx: ctx, Skysub: SkysubOptions{Enabled: true}, FrameLoaderCtx: loader})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("loader did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrCancelled) {
			t.Fatalf("planSkysub error = %v, want ErrCancelled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("planSkysub did not return after loader cancellation")
	}
	select {
	case <-finished:
	default:
		t.Fatal("planSkysub returned before started loader finished")
	}
}

func TestBuildOverlapSampleMapSkipsNonFiniteMappedCoordinates(t *testing.T) {
	p := plannedInput{
		input:       Input{HDU: fitsio.HDU{Data: fitsio.ImageData{Width: 4, Height: 4}}},
		sourceToRef: processing.AffineTransform{A: 1, E: 1, C: 0, F: 0},
	}
	p.sourceToRef.C = 0
	// A NaN translation makes every mapped coordinate invalid.
	p.sourceToRef.C = math.NaN()
	m := buildOverlapSampleMap(p, []float32{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1}, SkysubOptions{})
	if len(m) != 0 {
		t.Fatalf("map contains %d cells for non-finite coordinates, want none", len(m))
	}
}
