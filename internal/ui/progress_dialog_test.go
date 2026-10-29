package ui

import (
	"testing"
	"time"

	fynetest "fyne.io/fyne/v2/test"
)

// finishResult runs job.finish and reports which callback ran.
func finishResult(t *testing.T, job *uiJob) string {
	t.Helper()
	got := make(chan string, 1)
	job.finish(func() { got <- "applied" }, func() { got <- "dropped" })
	select {
	case r := <-got:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("finish callback did not run")
		return ""
	}
}

func TestUIJobAppliesWhenCurrent(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()
	var gens jobGeneration
	job := startUIJob(&gens, "Working", "Please wait", fynetest.NewWindow(nil))
	if r := finishResult(t, job); r != "applied" {
		t.Fatalf("current job: %s, want applied", r)
	}
	if job.ctx.Err() == nil {
		t.Error("finish did not release the job context")
	}
}

func TestUIJobCancelDropsResult(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()
	job := startUIJob(nil, "Working", "Please wait", fynetest.NewWindow(nil))
	job.pt.cancel() // what the dialog's Cancel button does
	if r := finishResult(t, job); r != "dropped" {
		t.Fatalf("cancelled job: %s, want dropped", r)
	}
}

func TestUIJobSupersededDropsResult(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()
	win := fynetest.NewWindow(nil)
	var gens jobGeneration
	first := startUIJob(&gens, "Working", "first", win)
	second := startUIJob(&gens, "Working", "second", win)
	if first.ctx.Err() == nil {
		t.Error("a newer job did not cancel the older job's context")
	}
	if r := finishResult(t, first); r != "dropped" {
		t.Fatalf("superseded job: %s, want dropped", r)
	}
	if r := finishResult(t, second); r != "applied" {
		t.Fatalf("latest job: %s, want applied", r)
	}
	third := startUIJob(&gens, "Working", "third", win)
	gens.invalidate() // e.g. workspace reset
	if r := finishResult(t, third); r != "dropped" {
		t.Fatalf("invalidated job: %s, want dropped", r)
	}
}

func TestEditBeginJobCancelsPrevious(t *testing.T) {
	es := &editWorkspaceState{}
	ctx1, _, gen1 := es.beginJob()
	ctx2, cancel2, gen2 := es.beginJob()
	if ctx1.Err() == nil || es.jobCurrent(ctx1, gen1) {
		t.Fatal("beginJob left the previous job live")
	}
	if !es.jobCurrent(ctx2, gen2) {
		t.Fatal("latest job is not current")
	}
	cancel2()
	if es.jobCurrent(ctx2, gen2) {
		t.Fatal("cancelled job still current")
	}
	ctx3, _, gen3 := es.beginJob()
	es.invalidateJobs()
	if ctx3.Err() == nil || es.jobCurrent(ctx3, gen3) {
		t.Fatal("invalidateJobs left the job live")
	}
}
