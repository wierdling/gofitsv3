package ui

import (
	"context"
	"testing"
)

func TestComposeWithSuspendedRefreshSkipsGeneration(t *testing.T) {
	ws := &composeWorkspace{}
	calls := 0
	ws.withSuspendedRefresh(func() {
		if !ws.suspendRefresh {
			t.Fatal("suspendRefresh not set inside callback")
		}
		// Nested suspension must restore the outer state, not clear it.
		ws.withSuspendedRefresh(func() {})
		if !ws.suspendRefresh {
			t.Fatal("nested call cleared suspendRefresh")
		}
		// While suspended, a refresh only runs onDone and returns.
		ws.refreshAsync(func() { calls++ })
	})
	if ws.suspendRefresh {
		t.Fatal("suspendRefresh not restored")
	}
	if calls != 1 {
		t.Fatalf("onDone called %d times, want 1", calls)
	}
}

func TestComposeRGBLargeModeRequiresChannels(t *testing.T) {
	ws := &composeWorkspace{largeMode: true}
	if _, _, _, _, err := ws.composeRGB(context.Background()); err == nil {
		t.Fatal("expected an error without a large store and channels")
	}
}
