package ui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gofitsv3/internal/models"
	"gofitsv3/internal/mosaic"
	"gofitsv3/internal/processing"
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

func TestSummarizeQueueErrorLimitsDisplayLength(t *testing.T) {
	message := strings.Repeat("alignment failure; ", 30)
	got := summarizeQueueError(message)
	if len([]rune(got)) != queueErrorDisplayLimit {
		t.Fatalf("summary rune length = %d, want %d", len([]rune(got)), queueErrorDisplayLimit)
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("summary should end with ellipsis: %q", got)
	}
}

type queueFakeExecutor struct {
	started   chan int
	release   chan struct{}
	failIndex int
	order     []int
	onStart   func(int)
}

func (f *queueFakeExecutor) Execute(ctx context.Context, job drizzleQueueJob, progress func(string, int, int)) (string, error) {
	index := len(f.order)
	f.order = append(f.order, index)
	if f.onStart != nil {
		f.onStart(index)
	}
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

func TestDrizzleQueueRunnerStopAfterCurrentLeavesLaterJobsPending(t *testing.T) {
	var runner *drizzleQueueRunner
	fake := &queueFakeExecutor{failIndex: -1, onStart: func(i int) {
		if i == 0 {
			runner.StopAfterCurrent()
		}
	}}
	j := []drizzleQueueJob{{ProjectPath: "one.json"}, {ProjectPath: "two.json"}}
	runner = &drizzleQueueRunner{executor: fake}
	runner.Run(context.Background(), j)
	if len(fake.order) != 1 || j[0].Status != queueSucceeded || j[1].Status != queuePending {
		t.Fatalf("executed=%v jobs=%+v", fake.order, j)
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

func TestAddProjectPathsAddsAllValidSelectionsAndContinuesAfterFailure(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.json")
	bad := filepath.Join(dir, "bad.json")
	second := filepath.Join(dir, "second.json")
	if err := os.WriteFile(first, []byte(`{"activeFilter":"F606W"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte(`{`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte(`{"activeFilter":"F814W"}`), 0644); err != nil {
		t.Fatal(err)
	}
	q := &drizzleQueueWindow{selected: -1}

	added, duplicates, err := q.addProjectPaths([]string{first, bad, second})

	if added != 2 || duplicates != 0 || err == nil || !strings.Contains(err.Error(), "bad.json") {
		t.Fatalf("addProjectPaths = (%d, %d, %v), want (2, 0, bad.json error)", added, duplicates, err)
	}
	if len(q.jobs) != 2 || q.jobs[0].ProjectPath != first || q.jobs[1].ProjectPath != second {
		t.Fatalf("queued projects = %+v, want first and second in selection order", q.jobs)
	}
	if q.jobs[0].Filter != "F606W" || q.jobs[1].Filter != "F814W" || q.jobs[0].RunAlign || q.jobs[1].RunAlign {
		t.Fatalf("queued project settings = %+v", q.jobs)
	}

	added, duplicates, err = q.addProjectPaths([]string{first})
	if added != 0 || duplicates != 1 || err != nil || len(q.jobs) != 2 {
		t.Fatalf("duplicate add = (%d, %d, %v), jobs=%d; want (0, 1, nil), jobs=2", added, duplicates, err, len(q.jobs))
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

func TestApplyLoadedProjectAlignmentResultsAcceptsPartialSuccess(t *testing.T) {
	inputs := []mosaic.Input{
		{Path: "reference.fits"},
		{Path: "excluded.fits", Excluded: true},
		{Path: "aligned.fits", OffsetX: 1},
		{Path: "unaligned.fits", OffsetX: 7, OffsetY: -3},
	}
	transform := processing.AffineTransform{A: 1, C: 4, E: 1, F: -2}
	results := []mosaic.StarAlignmentResult{
		{Applied: true},
		{Applied: true, OffsetX: 4, OffsetY: -2, ManualTransform: transform, HasManualTransform: true},
		{Error: "not enough matching stars"},
	}

	applyLoadedProjectAlignmentResults(inputs, []int{0, 2, 3}, results, 0)

	if got := inputs[2]; got.OffsetX != 4 || got.OffsetY != -2 || !got.HasManualTransform || got.ManualTransform != transform {
		t.Fatalf("successful alignment was not applied: %+v", got)
	}
	if got := inputs[3]; got.OffsetX != 7 || got.OffsetY != -3 || got.HasManualTransform {
		t.Fatalf("failed alignment should preserve the existing placement: %+v", got)
	}
}

func TestApplyLoadedProjectAlignmentResultsMapsExternalReferenceAndPreservesLockedInput(t *testing.T) {
	inputs := []mosaic.Input{
		{Path: "first.fits", OffsetX: 1},
		{Path: "excluded.fits", Excluded: true},
		{Path: "locked.fits", OffsetX: 2, OffsetLocked: true},
		{Path: "last.fits", OffsetX: 3},
	}
	results := []mosaic.StarAlignmentResult{
		{Applied: true, OffsetX: 100},
		{Applied: true, OffsetX: 10},
		{Applied: true, OffsetX: 20},
		{Applied: true, OffsetX: 30},
	}

	applyLoadedProjectAlignmentResults(inputs, []int{0, 2, 3}, results, 1)

	if got := inputs[0].OffsetX; got != 10 {
		t.Fatalf("first input offset = %v, want 10", got)
	}
	if got := inputs[2].OffsetX; got != 2 {
		t.Fatalf("locked input offset = %v, want 2", got)
	}
	if got := inputs[3].OffsetX; got != 30 {
		t.Fatalf("last input offset = %v, want 30", got)
	}
}
