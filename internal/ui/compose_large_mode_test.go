package ui

import "testing"

func TestComposeLargeLoadGenerations(t *testing.T) {
	ws := &composeWorkspace{largeLoadGenerations: map[string]uint64{}}

	req, session, store := ws.nextLargeLoadGeneration("channel-0")
	if store != nil {
		t.Fatalf("store = %v, want nil", store)
	}
	if !ws.loadRequestStillCurrent("channel-0", req, session) {
		t.Fatal("fresh request should be current")
	}

	// A newer request for the same slot supersedes the old one.
	req2, _, _ := ws.nextLargeLoadGeneration("channel-0")
	if ws.loadRequestStillCurrent("channel-0", req, session) {
		t.Error("superseded request still reported current")
	}
	if !ws.loadRequestStillCurrent("channel-0", req2, session) {
		t.Error("latest request should be current")
	}

	// Invalidating a different slot must not affect channel-0.
	ws.invalidateLargeSlot(1)
	if !ws.loadRequestStillCurrent("channel-0", req2, session) {
		t.Error("invalidating channel-1 affected channel-0")
	}
	ws.invalidateLargeSlot(0)
	if ws.loadRequestStillCurrent("channel-0", req2, session) {
		t.Error("invalidated slot still reported current")
	}

	// Overlay slots use the overlay-N key.
	oreq, _, _ := ws.nextLargeLoadGeneration("overlay-3")
	ws.invalidateLargeSlot(3)
	if ws.loadRequestStillCurrent("overlay-3", oreq, session) {
		t.Error("invalidateLargeSlot(3) did not invalidate overlay-3")
	}

	// Leaving large mode starts a new session.
	req3, _, _ := ws.nextLargeLoadGeneration("channel-2")
	ws.cleanupLargeMode()
	if ws.loadRequestStillCurrent("channel-2", req3, session) {
		t.Error("request from previous session still reported current")
	}
}
