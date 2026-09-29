package ui

import (
	"testing"

	"gofitsv3/internal/export"
)

func TestComposeDetectExportFormat(t *testing.T) {
	ws := &composeWorkspace{}
	cases := map[string]export.Format{
		"out.png":   export.PNG,
		"out.webp":  export.WEBP,
		"out.tif":   export.TIFF,
		"out.tiff":  export.TIFF,
		"out.jpg":   export.JPEG,
		"out.jpeg":  export.JPEG,
		"out":       export.PNG,
		"out.bmp":   export.PNG,
		"a.png.jpg": export.JPEG,
	}
	for path, want := range cases {
		if got := ws.detectExportFormat(path); got != want {
			t.Errorf("detectExportFormat(%q) = %q, want %q", path, got, want)
		}
	}
}
