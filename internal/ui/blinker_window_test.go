package ui

import "testing"

func TestBlinkLoadIsCurrentRejectsStalePublication(t *testing.T) {
	if !blinkLoadIsCurrent(false, 3, 3, 7, 7, true) {
		t.Fatal("current checked load was rejected")
	}
	for _, tc := range []struct {
		name                                       string
		closed                                     bool
		window, currentWindow, frame, currentFrame uint64
		checked                                    bool
	}{
		{"closed", true, 3, 3, 7, 7, true},
		{"window generation", false, 2, 3, 7, 7, true},
		{"frame generation", false, 3, 3, 6, 7, true},
		{"deselected", false, 3, 3, 7, 7, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if blinkLoadIsCurrent(tc.closed, tc.window, tc.currentWindow, tc.frame, tc.currentFrame, tc.checked) {
				t.Fatal("stale load was accepted")
			}
		})
	}
}
