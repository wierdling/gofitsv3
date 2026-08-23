package ui

import (
	"encoding/json"
	"testing"

	fynetest "fyne.io/fyne/v2/test"
	"gofitsv3/internal/stretch"
)

func newTestMosaicWorkspaceForLevels(t *testing.T) *mosaicWorkspace {
	t.Helper()
	app := fynetest.NewApp()
	t.Cleanup(app.Quit)

	ws := &mosaicWorkspace{
		app:         app,
		stretchMode: stretch.Asinh,
		mtfMidtone:  stretch.DefaultMTFMidtone,
	}
	ws.blackEntry = NewNumberEntry(0.001, 4)
	ws.whiteEntry = NewNumberEntry(0.001, 4)
	ws.bgEntry = NewNumberEntry(0.001, 4)
	ws.peakEntry = NewNumberEntry(1, 1)
	ws.scaledPeakEntry = NewNumberEntry(1, 1)
	ws.mtfMidtoneEntry = NewNumberEntry(0.01, 3)
	ws.mtfMidtoneEntry.SetValue(stretch.DefaultMTFMidtone)
	ws.modeSelect = NewSafeSelect([]string{"Linear", "Log", "Asinh", "Sqrt", "HistEq", "MTF"}, func(s string) {
		ws.stretchMode = labelToMode(s)
	})
	ws.modeSelect.SetSelected("Asinh")
	return ws
}

func TestLoadLevelPrefsAndModeRestoresMTFMidtone(t *testing.T) {
	ws := newTestMosaicWorkspaceForLevels(t)
	ws.app.Preferences().SetString(ws.prefKey("F606W"), `{"black":"0.1000","white":"0.9000","background":"0.2000","peak":"0.8000","scaledPeak":"1000.0","mode":"MTF","mtfMidtone":"0.1234"}`)

	if !ws.loadLevelPrefsAndMode("F606W") {
		t.Fatal("loadLevelPrefsAndMode returned false")
	}
	if ws.mtfMidtone != 0.1234 {
		t.Fatalf("mtfMidtone = %v, want 0.1234", ws.mtfMidtone)
	}
	if got := ws.mtfMidtoneEntry.Value(); got != 0.123 {
		t.Fatalf("mtfMidtoneEntry = %v, want rounded display value 0.123", got)
	}
	if ws.stretchMode != stretch.MTF {
		t.Fatalf("stretchMode = %v, want MTF", ws.stretchMode)
	}
}

func TestSaveLevelPrefsStoresMTFMidtone(t *testing.T) {
	ws := newTestMosaicWorkspaceForLevels(t)
	ws.activeFilter = "F606W"
	ws.stretchMode = stretch.MTF
	ws.mtfMidtone = 0.3456

	ws.saveLevelPrefs()

	raw := ws.app.Preferences().String(ws.prefKey("F606W"))
	var saved mosaicSavedLevels
	if err := json.Unmarshal([]byte(raw), &saved); err != nil {
		t.Fatalf("unmarshal saved prefs: %v", err)
	}
	if saved.MTFMidtone != "0.3456" {
		t.Fatalf("saved MTFMidtone = %q, want 0.3456", saved.MTFMidtone)
	}
	if saved.Mode != "MTF" {
		t.Fatalf("saved Mode = %q, want MTF", saved.Mode)
	}
}

func TestLevelPrefsRoundTripMTFMidtone(t *testing.T) {
	ws := newTestMosaicWorkspaceForLevels(t)
	ws.activeFilter = "F606W"
	ws.stretchMode = stretch.MTF
	ws.mtfMidtone = 0.3456

	ws.saveLevelPrefs()
	ws.stretchMode = stretch.Asinh
	ws.mtfMidtone = 0.1111
	ws.mtfMidtoneEntry.SetValue(0.1111)

	if !ws.loadLevelPrefsAndMode("F606W") {
		t.Fatal("loadLevelPrefsAndMode returned false")
	}
	if ws.stretchMode != stretch.MTF {
		t.Fatalf("stretchMode = %v, want MTF", ws.stretchMode)
	}
	if ws.mtfMidtone != 0.3456 {
		t.Fatalf("mtfMidtone = %v, want 0.3456", ws.mtfMidtone)
	}
	if got := ws.mtfMidtoneEntry.Value(); got != 0.346 {
		t.Fatalf("mtfMidtoneEntry = %v, want rounded display value 0.346", got)
	}
}

func TestLoadLevelPrefsAndModePreservesMissingMTFMidtone(t *testing.T) {
	ws := newTestMosaicWorkspaceForLevels(t)
	ws.mtfMidtone = 0.4444
	ws.mtfMidtoneEntry.SetValue(0.4444)
	ws.app.Preferences().SetString(ws.prefKey("F814W"), `{"black":"0.1000","white":"0.9000","background":"0.2000","peak":"0.8000","scaledPeak":"1000.0","mode":"Asinh"}`)

	if !ws.loadLevelPrefsAndMode("F814W") {
		t.Fatal("loadLevelPrefsAndMode returned false")
	}
	if ws.mtfMidtone != 0.4444 {
		t.Fatalf("mtfMidtone = %v, want preserved 0.4444", ws.mtfMidtone)
	}
	if got := ws.mtfMidtoneEntry.Value(); got != 0.444 {
		t.Fatalf("mtfMidtoneEntry = %v, want preserved 0.444", got)
	}
}

func TestLoadLevelPrefsAndModePreservesInvalidMTFMidtone(t *testing.T) {
	ws := newTestMosaicWorkspaceForLevels(t)
	ws.mtfMidtone = 0.5555
	ws.mtfMidtoneEntry.SetValue(0.5555)
	ws.app.Preferences().SetString(ws.prefKey("F555W"), `{"black":"0.1000","white":"0.9000","background":"0.2000","peak":"0.8000","scaledPeak":"1000.0","mode":"MTF","mtfMidtone":"1.5"}`)

	if !ws.loadLevelPrefsAndMode("F555W") {
		t.Fatal("loadLevelPrefsAndMode returned false")
	}
	if ws.mtfMidtone != 0.5555 {
		t.Fatalf("mtfMidtone = %v, want preserved 0.5555", ws.mtfMidtone)
	}
	if got := ws.mtfMidtoneEntry.Value(); got != 0.556 {
		t.Fatalf("mtfMidtoneEntry = %v, want preserved 0.556", got)
	}
	if ws.stretchMode != stretch.MTF {
		t.Fatalf("stretchMode = %v, want MTF", ws.stretchMode)
	}
}

func TestLoadLevelPrefsAndModeMalformedPreservesAllControls(t *testing.T) {
	ws := newTestMosaicWorkspaceForLevels(t)
	ws.blackEntry.SetValue(0.11)
	ws.whiteEntry.SetValue(0.88)
	ws.bgEntry.SetValue(0.22)
	ws.peakEntry.SetValue(0.77)
	ws.scaledPeakEntry.SetValue(1234)
	ws.mtfMidtone = 0.42
	ws.mtfMidtoneEntry.SetValue(0.42)
	ws.app.Preferences().SetString(ws.prefKey("BAD"), `{"black":"not-a-number","white":"0.9","background":"0.2","peak":"0.8","scaledPeak":"1000","mode":"MTF","mtfMidtone":"0.1"}`)
	if ws.loadLevelPrefsAndMode("BAD") {
		t.Fatal("malformed prefs accepted")
	}
	if ws.blackEntry.Value() != 0.11 || ws.whiteEntry.Value() != 0.88 || ws.bgEntry.Value() != 0.22 || ws.peakEntry.Value() != 0.8 || ws.scaledPeakEntry.Value() != 1234 || ws.mtfMidtone != 0.42 {
		t.Fatalf("malformed prefs partially mutated controls: %.4f %.4f %.4f %.4f %.4f mtf %.4f", ws.blackEntry.Value(), ws.whiteEntry.Value(), ws.bgEntry.Value(), ws.peakEntry.Value(), ws.scaledPeakEntry.Value(), ws.mtfMidtone)
	}
}
