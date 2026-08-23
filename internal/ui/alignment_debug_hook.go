package ui

import "fyne.io/fyne/v2"

// installAlignmentDebugHook installs a temporary no-op debug hook and returns a
// cleanup function. The UI references this during debug-alignment flows, but
// the hook is optional and should not affect normal processing behavior.
func installAlignmentDebugHook(_ fyne.Window) func() {
	// Debug visualization is optional. Do not replace the process-wide hook
	// while an alignment may be running in another goroutine.
	return func() {}
}
