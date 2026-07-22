package ui

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	fynetest "fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
	"gofitsv3/internal/mosaic"
)

func TestMosaicControlsAccordionStartsOpenAndAllowsIndependentToggling(t *testing.T) {
	app := fynetest.NewApp()
	t.Cleanup(app.Quit)

	win := fynetest.NewWindow(nil)
	root, _, _, _ := newMosaicWorkspace(app, win)
	accordion := mosaicControlsAccordion(t, root)

	if !accordion.MultiOpen {
		t.Fatal("Mosaic controls accordion does not allow multiple open sections")
	}
	wantTitles := []string{"Preview Settings", "Drizzle Commands", "Input Frames"}
	if len(accordion.Items) != len(wantTitles) {
		t.Fatalf("accordion item count = %d, want %d", len(accordion.Items), len(wantTitles))
	}
	for i, want := range wantTitles {
		if got := accordion.Items[i].Title; got != want {
			t.Errorf("accordion item %d title = %q, want %q", i, got, want)
		}
		if !accordion.Items[i].Open {
			t.Errorf("accordion item %q is closed initially", want)
		}
	}

	accordion.Close(0)
	if accordion.Items[0].Open {
		t.Error("closing Preview Settings left it open")
	}
	if !accordion.Items[1].Open || !accordion.Items[2].Open {
		t.Error("closing Preview Settings also closed another section")
	}

	accordion.Open(0)
	if !accordion.Items[0].Open || !accordion.Items[1].Open || !accordion.Items[2].Open {
		t.Error("opening Preview Settings did not preserve the other open sections")
	}
}

func TestMosaicInputFramesTabsUseAvailableAccordionHeight(t *testing.T) {
	app := fynetest.NewApp()
	t.Cleanup(app.Quit)

	win := fynetest.NewWindow(nil)
	root, _, _, _ := newMosaicWorkspace(app, win)
	root.Resize(fyne.NewSize(1000, 1200))
	accordion := mosaicControlsAccordion(t, root)
	accordion.Close(0)
	accordion.Close(1)
	accordion.Resize(fyne.NewSize(320, 1000))

	tabs := findAppTabs(accordion.Items[2].Detail)
	if tabs == nil {
		t.Fatal("Input Frames detail does not contain tabs")
	}
	if tabs.Size().Height <= tabs.MinSize().Height {
		t.Errorf("Input Frames tabs height = %v, want more than their minimum height %v", tabs.Size().Height, tabs.MinSize().Height)
	}

	accordion.Open(0)
	if !accordion.Items[0].Open || accordion.Items[1].Open || !accordion.Items[2].Open {
		t.Error("reopening Preview Settings changed the independent Input Frames section state")
	}
}

func mosaicControlsAccordion(t *testing.T, root fyne.CanvasObject) *widget.Accordion {
	t.Helper()
	if split, ok := root.(*container.Split); ok {
		root = split.Leading
	}
	if accordion := findAccordion(root); accordion != nil {
		return accordion
	}
	t.Fatalf("Mosaic controls do not contain an accordion")
	return nil
}

func findAccordion(object fyne.CanvasObject) *widget.Accordion {
	if accordion, ok := object.(*widget.Accordion); ok {
		return accordion
	}
	switch object := object.(type) {
	case *fyne.Container:
		for _, child := range object.Objects {
			if accordion := findAccordion(child); accordion != nil {
				return accordion
			}
		}
	case *container.Scroll:
		return findAccordion(object.Content)
	}
	return nil
}

func findAppTabs(object fyne.CanvasObject) *container.AppTabs {
	if tabs, ok := object.(*container.AppTabs); ok {
		return tabs
	}
	if container, ok := object.(*fyne.Container); ok {
		for _, child := range container.Objects {
			if tabs := findAppTabs(child); tabs != nil {
				return tabs
			}
		}
	}
	return nil
}

func TestMosaicInputFramesScrollConstrainsWideTable(t *testing.T) {
	app := fynetest.NewApp()
	t.Cleanup(app.Quit)

	ws := &mosaicWorkspace{
		app:            app,
		state:          &mosaicState{inputs: []mosaic.Input{{Path: "a-very-long-input-frame-name.fits"}}},
		offsetControls: container.NewVBox(),
		offsetHeader:   container.NewVBox(),
	}
	ws.offsetScroll = container.NewVScroll(ws.offsetControls)
	ws.rebuildOffsetControls()

	inputFramesScroll := newMosaicInputFramesScroll(ws.offsetHeader, ws.offsetScroll)
	if inputFramesScroll.Direction != fyne.ScrollHorizontalOnly {
		t.Fatalf("Input Frames wrapper direction = %v, want horizontal only", inputFramesScroll.Direction)
	}
	if ws.offsetScroll.Direction != fyne.ScrollVerticalOnly {
		t.Fatalf("Input Frames rows direction = %v, want vertical only", ws.offsetScroll.Direction)
	}
	if got := inputFramesScroll.MinSize().Width; got != 260 {
		t.Errorf("Input Frames wrapper minimum width = %v, want 260", got)
	}
	if contentWidth := inputFramesScroll.Content.MinSize().Width; contentWidth <= inputFramesScroll.MinSize().Width {
		t.Errorf("Input Frames content width = %v, want wider than constrained wrapper width %v", contentWidth, inputFramesScroll.MinSize().Width)
	}
}

func TestMosaicInputFramesRowsAlignWithHeaderForLongReferenceName(t *testing.T) {
	app := fynetest.NewApp()
	t.Cleanup(app.Quit)

	ws := &mosaicWorkspace{
		app: app,
		state: &mosaicState{inputs: []mosaic.Input{{
			Path: "a-very-long-reference-frame-name-that-must-not-widen-the-input-frame-table.fits",
		}}},
		offsetControls: container.NewVBox(),
		offsetHeader:   container.NewVBox(),
	}
	ws.rebuildOffsetControls()

	header := ws.offsetHeader.Objects[0].(*fyne.Container)
	row := ws.offsetControls.Objects[0].(*fyne.Container)
	header.Resize(header.MinSize())
	row.Resize(row.MinSize())

	if got := header.Objects[2].Size().Width; got != 160 {
		t.Errorf("header Name width = %v, want 160", got)
	}
	if got := row.Objects[2].Size().Width; got != 160 {
		t.Errorf("row Name width = %v, want 160", got)
	}
	for _, column := range []int{3, 4, 5} {
		headerCell := header.Objects[column]
		rowCell := row.Objects[column]
		if headerCell.Position().X != rowCell.Position().X {
			t.Errorf("column %d x position: header = %v, row = %v", column, headerCell.Position().X, rowCell.Position().X)
		}
		if headerCell.Size().Width != rowCell.Size().Width {
			t.Errorf("column %d width: header = %v, row = %v", column, headerCell.Size().Width, rowCell.Size().Width)
		}
	}
}

func TestInputFramesPopupRowsAlignWithHeaderForLongValues(t *testing.T) {
	app := fynetest.NewApp()
	t.Cleanup(app.Quit)

	ws := &mosaicWorkspace{
		app: app,
		win: fynetest.NewWindow(nil),
		state: &mosaicState{inputs: []mosaic.Input{{
			Path:         "a-very-long-reference-frame-name-that-must-not-widen-the-popup-table.fits",
			DateObs:      "2026-07-21T12:34:56.7890123456789",
			ExposureTime: 123456789.1234,
		}}},
		offsetControls: container.NewVBox(),
		offsetHeader:   container.NewVBox(),
	}
	ws.openInputFramesPopup()
	t.Cleanup(ws.inputFramesWindow.Close)

	contentWindow, ok := ws.inputFramesWindow.(interface{ Content() fyne.CanvasObject })
	if !ok {
		t.Fatalf("Input Frames window %T does not expose its content", ws.inputFramesWindow)
	}
	var header, row *fyne.Container
	for _, candidate := range tableRows(contentWindow.Content()) {
		if _, ok := candidate.Objects[0].(*canvas.Rectangle); ok {
			header = candidate
		} else if _, ok := candidate.Objects[0].(*widget.Button); ok {
			row = candidate
		}
	}
	if header == nil || row == nil {
		t.Fatalf("popup table header = %v, row = %v, want both", header != nil, row != nil)
	}
	header.Resize(header.MinSize())
	row.Resize(row.MinSize())

	for _, column := range []int{2, 3, 4, 5, 6, 7, 8, 9} {
		if header.Objects[column].Position().X != row.Objects[column].Position().X {
			t.Errorf("column %d x position: header = %v, row = %v", column, header.Objects[column].Position().X, row.Objects[column].Position().X)
		}
		if header.Objects[column].Size().Width != row.Objects[column].Size().Width {
			t.Errorf("column %d width: header = %v, row = %v", column, header.Objects[column].Size().Width, row.Objects[column].Size().Width)
		}
	}
	if got := row.Objects[2].Size().Width; got != 160 {
		t.Errorf("popup row Name width = %v, want 160", got)
	}
}

func tableRows(object fyne.CanvasObject) []*fyne.Container {
	if scroll, ok := object.(*container.Scroll); ok {
		return tableRows(scroll.Content)
	}
	containerObject, ok := object.(*fyne.Container)
	if !ok {
		return nil
	}
	rows := make([]*fyne.Container, 0, 2)
	if len(containerObject.Objects) == 12 {
		rows = append(rows, containerObject)
	}
	for _, child := range containerObject.Objects {
		rows = append(rows, tableRows(child)...)
	}
	return rows
}
