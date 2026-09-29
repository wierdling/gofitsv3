package ui

import (
	"fmt"
)

func (ws *composeWorkspace) isLargeModeActive() bool { return ws.largeMode }

func (ws *composeWorkspace) nextLargeLoadGeneration(slot string) (uint64, uint64, *composeLargeStore) {
	ws.largeMu.Lock()
	defer ws.largeMu.Unlock()
	ws.largeLoadGenerations[slot]++
	return ws.largeLoadGenerations[slot], ws.largeSessionGeneration, ws.largeStore
}

func (ws *composeWorkspace) loadRequestStillCurrent(slot string, request, session uint64) bool {
	ws.largeMu.RLock()
	defer ws.largeMu.RUnlock()
	return composeLoadRequestCurrent(ws.largeSessionGeneration, session, ws.largeLoadGenerations[slot], request)
}

func (ws *composeWorkspace) largeLoadStillCurrent(slot string, request, session uint64, d composeArtifactDescriptor) bool {
	ws.largeMu.RLock()
	defer ws.largeMu.RUnlock()
	store := ws.largeStore
	if store == nil || ws.largeSessionGeneration != session || ws.largeLoadGenerations[slot] != request || d.Slot != slot {
		return false
	}
	current, ok := store.Descriptor(slot)
	return ok && current.Generation == d.Generation && current.Path == d.Path
}

func (ws *composeWorkspace) cleanupLargeArtifactIfCurrent(d composeArtifactDescriptor) {
	ws.largeMu.RLock()
	store := ws.largeStore
	ws.largeMu.RUnlock()
	if d.Slot == "" || store == nil {
		return
	}
	_, _ = store.RemoveSlotIfCurrent(d)
}

func (ws *composeWorkspace) invalidateLargeSlot(idx int) {
	ws.largeMu.Lock()
	defer ws.largeMu.Unlock()
	slot := fmt.Sprintf("channel-%d", idx)
	if idx >= 3 {
		slot = fmt.Sprintf("overlay-%d", idx)
	}
	ws.largeLoadGenerations[slot]++
}

func (ws *composeWorkspace) cleanupLargeMode() {
	ws.largeMu.Lock()
	ws.largeSessionGeneration++
	store := ws.largeStore
	ws.largeStore = nil
	ws.largeMu.Unlock()
	if store != nil {
		ws.editSnapshotMu.Lock()
		if ws.editSnapshotCancel != nil {
			ws.editSnapshotCancel()
		}
		ws.editSnapshotMu.Unlock()
		_ = store.Close()
	}
}
