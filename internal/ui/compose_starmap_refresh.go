package ui

// activeComposeWorkspace is the single Compose tab's workspace, set once by
// newComposeWorkspace. The app builds exactly one window with one of each
// workspace tab, so at most one exists; this lets other workspaces (the Star
// Map review dialog, Pick Missed Stars) tell Compose to pick up a star map
// file they just saved, without Compose needing to poll for it.
var activeComposeWorkspace *composeWorkspace

// notifyStarMapSaved tells the active Compose workspace, if any, that a star
// map FITS file was just saved to disk, so it should re-check every treated
// source's star map and refresh its diagnostics overlay and gentler-stretch/
// White-Stars rendering. Safe to call even when Compose has never been shown
// or has nothing loaded. Must be called on the UI thread.
func notifyStarMapSaved() {
	ws := activeComposeWorkspace
	if ws == nil {
		return
	}
	ws.starTreatments.invalidateAll()
	ws.refresh()
}
