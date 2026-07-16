package ui

import (
	"testing"

	"fyne.io/fyne/v2/theme"
)

func TestAppThemeUsesGreenForActionButtons(t *testing.T) {
	got := appTheme{Theme: theme.DarkTheme()}.Color(theme.ColorNamePrimary, theme.VariantDark)
	if got != actionButtonColor {
		t.Fatalf("primary color = %v, want action button green %v", got, actionButtonColor)
	}
}
