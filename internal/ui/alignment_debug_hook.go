package ui

import (
	"fyne.io/fyne/v2"

	"gofitsv3/internal/processing"
)

// installAlignmentDebugHook installs a temporary no-op debug hook and returns a
// cleanup function. The UI references this during debug-alignment flows, but
// the hook is optional and should not affect normal processing behavior.
func installAlignmentDebugHook(_ fyne.Window) func() {
	prev := processing.AlignmentDebugHook
	processing.AlignmentDebugHook = func(processing.AlignmentDiag) {}
	return func() {
		processing.AlignmentDebugHook = prev
	}
}
