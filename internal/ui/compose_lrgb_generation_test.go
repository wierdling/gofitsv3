package ui

import "testing"

func TestComposeLRGBPublishRejectsStaleProjectCompletion(t *testing.T) {
	if composeLRGBPublishAllowed(2, 1, "project-b.fits", "project-a.fits") {
		t.Fatal("project A completion was accepted after project B advanced generation")
	}
	if !composeLRGBPublishAllowed(2, 2, "project-b.fits", "project-b.fits") {
		t.Fatal("current project completion was rejected")
	}
}
