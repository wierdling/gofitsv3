package ui

import (
	"testing"

	"gofitsv3/internal/models"
)

func TestSkysubOptionsFromSettingsMapsDisconnectedBackgroundEqualization(t *testing.T) {
	options := skysubOptionsFromSettings(models.SkysubSettings{
		EqualizeDisconnectedBackgrounds: true,
	})
	if !options.EqualizeDisconnectedBackgrounds {
		t.Fatal("EqualizeDisconnectedBackgrounds = false, want mapped true")
	}
}
