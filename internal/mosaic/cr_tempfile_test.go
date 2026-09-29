package mosaic

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"gofitsv3/internal/fitsio"
)

type testCRCustomError struct{ message string }

func (e testCRCustomError) Error() string { return e.message }

func TestFirstErrorConcurrentHeterogeneousErrors(t *testing.T) {
	var first firstError
	// These have distinct dynamic types. An atomic.Value storing the first
	// error would panic when a concurrent worker stores a different type.
	errs := []error{
		context.Canceled,
		fmt.Errorf("wrapped deadline: %w", context.DeadlineExceeded),
		testCRCustomError{message: "custom failure"},
		fmt.Errorf("formatted failure: %d", 4),
	}
	var wg sync.WaitGroup
	for _, err := range errs {
		wg.Add(1)
		go func(err error) { defer wg.Done(); first.Store(err) }(err)
	}
	wg.Wait()
	got := first.Load()
	if got == nil {
		t.Fatal("Load returned nil")
	}
	for _, err := range errs {
		if got == err {
			return
		}
	}
	t.Fatalf("Load returned unexpected error %v", got)
}

func TestCRPreparationProgressStageUsesGeminiWordingForGMOSOnly(t *testing.T) {
	planned := []plannedInput{{input: Input{PrimaryHeader: fitsio.Header{Cards: map[string]string{"INSTRUME": "GMOS-N"}}}}}
	if got := crPreparationProgressStage(planned, []int{0}); got != "Preparing Gemini frames" {
		t.Fatalf("stage = %q", got)
	}
}

func TestCRPreparationProgressStageKeepsCosmicRayWordingForOtherInputs(t *testing.T) {
	planned := []plannedInput{{input: Input{PrimaryHeader: fitsio.Header{Cards: map[string]string{"INSTRUME": "WFC3"}}}}}
	if got := crPreparationProgressStage(planned, []int{0}); got != "Cleaning cosmic rays" {
		t.Fatalf("stage = %q", got)
	}
}
