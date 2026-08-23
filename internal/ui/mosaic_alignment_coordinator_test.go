package ui

import "testing"

func TestMosaicAlignmentCoordinatorRejectsOverlapAndStaleCompletion(t *testing.T) {
	ws := &mosaicWorkspace{}
	_, generation, ok := ws.beginMosaicAlignment()
	if !ok {
		t.Fatal("first alignment did not start")
	}
	if _, _, ok := ws.beginMosaicAlignment(); ok {
		t.Fatal("overlapping alignment was accepted")
	}
	if ws.finishMosaicAlignment(generation + 1) {
		t.Fatal("stale completion was accepted")
	}
	if !ws.finishMosaicAlignment(generation) {
		t.Fatal("current completion was rejected")
	}
}

func TestMosaicBuildCoordinatorRejectsStaleCompletion(t *testing.T) {
	ws := &mosaicWorkspace{}
	_, generation, ok := ws.beginMosaicBuild()
	if !ok || ws.finishMosaicBuild(generation+1) {
		t.Fatal("stale build completion was accepted")
	}
	if !ws.finishMosaicBuild(generation) {
		t.Fatal("current build completion was rejected")
	}
}
