package mosaic

import (
	"context"
	"fmt"
	"sync"
	"testing"
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
