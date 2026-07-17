package ui

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"gofitsv3/internal/models"
	"gofitsv3/internal/mosaic"
)

func TestDrizzleQueueOutputPath(t *testing.T) {
	when := time.Date(2026, 7, 16, 21, 45, 30, 0, time.Local)
	got, err := drizzleQueueOutputPath(filepath.Join("C:", "projects", "mosaic.json"), " F606W ", when)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("C:", "projects", "F606W_"+when.Format("20060102_150405")+"_drizzle.fits")
	if got != want {
		t.Fatalf("output path = %q, want %q", got, want)
	}
}

func TestDrizzleQueueOutputPathSanitizesFilter(t *testing.T) {
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.Local)
	got, err := drizzleQueueOutputPath(filepath.Join("C:", "projects", "mosaic.json"), `F/606:W*?`, when)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("C:", "projects", "F_606_W___"+when.Format("20060102_150405")+"_drizzle.fits")
	if got != want {
		t.Fatalf("sanitized output path = %q, want %q", got, want)
	}
}

func TestDrizzleQueueOutputPathRejectsEmptyFilter(t *testing.T) {
	if _, err := drizzleQueueOutputPath("mosaic.json", "***", time.Now()); err == nil {
		t.Fatal("expected invalid filter error")
	}
}

type queueFakeExecutor struct {
	started   chan int
	release   chan struct{}
	failIndex int
	order     []int
}

func (f *queueFakeExecutor) Execute(ctx context.Context, job drizzleQueueJob, progress func(string, int, int)) (string, error) {
	index := len(f.order)
	f.order = append(f.order, index)
	if f.started != nil {
		f.started <- index
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return "", mosaic.ErrCancelled
		}
	}
	if index == f.failIndex {
		return "", errors.New("fake failure")
	}
	return "output.fits", nil
}

func TestDrizzleQueueRunnerContinuesAfterFailure(t *testing.T) {
	fake := &queueFakeExecutor{failIndex: 0}
	jobs := []drizzleQueueJob{{ProjectPath: "one.json"}, {ProjectPath: "two.json"}}
	done := make(chan struct{})
	runner := &drizzleQueueRunner{executor: fake, onDone: func() { close(done) }}
	runner.Run(context.Background(), jobs)
	<-done
	if jobs[0].Status != queueFailed || jobs[1].Status != queueSucceeded {
		t.Fatalf("statuses = %v, %v; want failed, succeeded", jobs[0].Status, jobs[1].Status)
	}
	if len(fake.order) != 2 {
		t.Fatalf("executed %d jobs, want 2", len(fake.order))
	}
}

func TestDrizzleQueueRunnerCancelCurrentContinues(t *testing.T) {
	fake := &queueFakeExecutor{started: make(chan int, 2), release: make(chan struct{})}
	jobs := []drizzleQueueJob{{ProjectPath: "one.json"}, {ProjectPath: "two.json"}}
	done := make(chan struct{})
	runner := &drizzleQueueRunner{executor: fake, onDone: func() { close(done) }}
	go runner.Run(context.Background(), jobs)
	if got := <-fake.started; got != 0 {
		t.Fatalf("first started index = %d", got)
	}
	runner.CancelCurrent()
	if got := <-fake.started; got != 1 {
		t.Fatalf("second started index = %d, want 1", got)
	}
	close(fake.release)
	<-done
	if jobs[0].Status != queueCancelled || jobs[1].Status != queueSucceeded {
		t.Fatalf("statuses = %v, %v; want cancelled, succeeded", jobs[0].Status, jobs[1].Status)
	}
}

func TestApplyInputStateRestoresQueueRelevantFields(t *testing.T) {
	var input mosaic.Input
	state := models.MosaicInputState{
		OffsetX: 3.5, OffsetY: -2.25, HasTransform: true, Locked: true,
		Excluded: true, NormalizeExposure: true, ExposureScale: 0.125,
		TransformA: 1, TransformD: 1, TransformF: 4,
	}
	applyInputState(&input, state)
	if !input.Excluded || !input.NormalizeExposure || input.ExposureScale != 0.125 || !input.OffsetLocked {
		t.Fatalf("input state not restored: %+v", input)
	}
	if input.OffsetX != 3.5 || input.OffsetY != -2.25 || !input.HasManualTransform || input.ManualTransform.F != 4 {
		t.Fatalf("transform state not restored: %+v", input)
	}
}
