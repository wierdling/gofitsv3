package ui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
	"github.com/wierdling/gofiledialog"
)

type drizzleQueueWindow struct {
	ws       *mosaicWorkspace
	win      fyne.Window
	jobs     []drizzleQueueJob
	selected int
	running  bool
	runner   *drizzleQueueRunner

	rows        *fyne.Container
	stage       *widget.Label
	overall     *widget.Label
	progress    *widget.ProgressBar
	addBtn      *widget.Button
	generateBtn *widget.Button
	removeBtn   *widget.Button
	upBtn       *widget.Button
	downBtn     *widget.Button
	startBtn    *widget.Button
	cancelBtn   *widget.Button
	stopBtn     *widget.Button
}

func (ws *mosaicWorkspace) openDrizzleQueue() {
	if ws.queueWindow != nil {
		ws.queueWindow.Show()
		return
	}
	q := &drizzleQueueWindow{ws: ws, selected: -1}
	q.win = ws.app.NewWindow("Drizzle Queue")
	ws.queueWindow = q.win
	q.rows = container.NewVBox()
	q.stage = widget.NewLabel("Queue is empty.")
	q.overall = widget.NewLabel("")
	q.progress = widget.NewProgressBar()
	q.addBtn = widget.NewButton("Add Projects...", q.addProject)
	q.generateBtn = widget.NewButton("Create Projects from Directory...", q.openProjectGenerator)
	q.removeBtn = widget.NewButton("Remove", q.removeSelected)
	q.upBtn = widget.NewButton("Move Up", func() { q.moveSelected(-1) })
	q.downBtn = widget.NewButton("Move Down", func() { q.moveSelected(1) })
	q.startBtn = widget.NewButton("Start Queue", q.start)
	q.cancelBtn = widget.NewButton("Cancel Current", func() {
		if q.runner != nil {
			q.runner.CancelCurrent()
		}
	})
	q.stopBtn = widget.NewButton("Stop After Current", func() {
		if q.runner != nil {
			q.runner.StopAfterCurrent()
		}
	})
	q.cancelBtn.Disable()
	q.stopBtn.Disable()

	content := container.NewBorder(
		container.NewVBox(q.overall, q.stage, q.progress),
		container.NewVBox(container.NewGridWithColumns(4, q.addBtn, q.generateBtn, q.removeBtn, q.startBtn), container.NewGridWithColumns(2, q.upBtn, q.downBtn), container.NewGridWithColumns(2, q.cancelBtn, q.stopBtn)),
		nil, nil, container.NewVScroll(q.rows),
	)
	q.win.SetContent(content)
	q.win.Resize(fyne.NewSize(820, 520))
	q.win.SetCloseIntercept(func() { q.win.Hide() })
	q.win.SetOnClosed(func() { ws.queueWindow = nil })
	q.refresh()
	q.win.Show()
}

func (q *drizzleQueueWindow) addProject() {
	opts := []gofiledialog.Option{
		gofiledialog.WithTitle("Add Mosaic Projects"),
		gofiledialog.WithFilters(gofiledialog.Filter{Name: "Mosaic projects", Extensions: []string{".json"}}),
		gofiledialog.WithMultiSelect(true),
	}
	if lastDir := q.ws.app.Preferences().String("lastDir"); lastDir != "" {
		opts = append(opts, gofiledialog.WithStartDir(lastDir))
	}
	if err := gofiledialog.ShowOpen(func(paths []string, err error) {
		if err != nil {
			dialog.ShowError(err, q.win)
			return
		}
		if len(paths) == 0 {
			return
		}
		q.ws.app.Preferences().SetString("lastDir", filepath.Dir(paths[0]))
		added, duplicates, addErr := q.addProjectPaths(paths)
		if added > 0 {
			q.refresh()
		}
		if addErr != nil {
			if duplicates > 0 {
				addErr = fmt.Errorf("%w\n%d selected project(s) were already queued", addErr, duplicates)
			}
			dialog.ShowError(addErr, q.win)
		} else if duplicates > 0 {
			dialog.ShowInformation("Already Queued", fmt.Sprintf("%d selected project(s) were already in the queue.", duplicates), q.win)
		}
	}, q.win, opts...); err != nil {
		dialog.ShowError(err, q.win)
	}
}

func (q *drizzleQueueWindow) addProjectPaths(paths []string) (added, duplicates int, addErr error) {
	var failures []error
	for _, path := range paths {
		project, absPath, err := readMosaicProject(path)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", filepath.Base(path), err))
			continue
		}
		if !q.addGeneratedProject(absPath, project, false) {
			duplicates++
			continue
		}
		added++
	}
	return added, duplicates, errors.Join(failures...)
}

func (q *drizzleQueueWindow) removeSelected() {
	if q.running || q.selected < 0 || q.selected >= len(q.jobs) {
		return
	}
	q.jobs = append(q.jobs[:q.selected], q.jobs[q.selected+1:]...)
	if q.selected >= len(q.jobs) {
		q.selected = len(q.jobs) - 1
	}
	q.refresh()
}

func (q *drizzleQueueWindow) moveSelected(delta int) {
	if q.running || q.selected < 0 || q.selected >= len(q.jobs) {
		return
	}
	target := q.selected + delta
	if target < 0 || target >= len(q.jobs) {
		return
	}
	q.jobs[q.selected], q.jobs[target] = q.jobs[target], q.jobs[q.selected]
	q.selected = target
	q.refresh()
}

func (q *drizzleQueueWindow) start() {
	if q.running || !q.hasPending() {
		return
	}
	q.running = true
	q.ws.queueRunning = true
	q.ws.updateActionButtons()
	q.runner = &drizzleQueueRunner{
		executor: defaultDrizzleQueueExecutor{},
		onEvent: func(event drizzleQueueEvent) {
			fyne.Do(func() {
				if event.Index >= 0 && event.Index < len(q.jobs) {
					q.jobs[event.Index].Status = event.Status
					if event.OutputPath != "" {
						q.jobs[event.Index].OutputPath = event.OutputPath
					}
					if event.Err != "" {
						q.jobs[event.Index].Err = event.Err
					}
					q.overall.SetText(fmt.Sprintf("Job %d of %d: %s", event.Index+1, len(q.jobs), filepath.Base(q.jobs[event.Index].ProjectPath)))
				}
				q.stage.SetText(event.Stage)
				if event.Total > 0 {
					q.progress.Max = float64(event.Total)
					q.progress.SetValue(float64(event.Done))
				}
				q.refresh()
			})
		},
		onDone: func() {
			fyne.Do(func() {
				q.running = false
				q.ws.queueRunning = false
				q.ws.updateActionButtons()
				q.refresh()
				q.showSummary()
			})
		},
	}
	q.refresh()
	jobs := append([]drizzleQueueJob(nil), q.jobs...)
	go q.runner.Run(context.Background(), jobs)
}

func (q *drizzleQueueWindow) refresh() {
	if q.rows == nil {
		return
	}
	q.rows.Objects = nil
	for i := range q.jobs {
		index := i
		job := &q.jobs[i]
		align := widget.NewCheck("Align", func(value bool) { q.jobs[index].RunAlign = value })
		align.SetChecked(job.RunAlign)
		if q.running {
			align.Disable()
		}
		selectBtn := widget.NewButton(fmt.Sprintf("%d", i+1), func() { q.selected = index; q.refresh() })
		if i == q.selected {
			selectBtn.Importance = widget.HighImportance
		}
		name := widget.NewLabel(filepath.Base(job.ProjectPath))
		filter := job.Filter
		if filter == "" {
			filter = "(auto)"
		}
		text := fmt.Sprintf("%s  |  %s  |  %s", filter, job.Status.String(), filepath.Base(job.OutputPath))
		if job.Err != "" {
			text += "  |  " + summarizeQueueError(job.Err)
		}
		q.rows.Add(container.NewBorder(nil, nil, container.NewHBox(selectBtn, align, name), nil, widget.NewLabel(text)))
	}
	if len(q.jobs) == 0 {
		q.rows.Add(widget.NewLabel("No projects queued."))
	}
	q.rows.Refresh()
	setButtonEnabled(q.removeBtn, !q.running && q.selected >= 0)
	setButtonEnabled(q.upBtn, !q.running && q.selected > 0)
	setButtonEnabled(q.downBtn, !q.running && q.selected >= 0 && q.selected < len(q.jobs)-1)
	setButtonEnabled(q.addBtn, !q.running)
	setButtonEnabled(q.generateBtn, !q.running)
	setButtonEnabled(q.startBtn, !q.running && q.hasPending())
	setButtonEnabled(q.cancelBtn, q.running)
	setButtonEnabled(q.stopBtn, q.running)
}

func setButtonEnabled(button *widget.Button, enabled bool) {
	if enabled {
		button.Enable()
	} else {
		button.Disable()
	}
}

func (q *drizzleQueueWindow) hasPending() bool {
	for _, job := range q.jobs {
		if job.Status == queuePending {
			return true
		}
	}
	return false
}

func (q *drizzleQueueWindow) showSummary() {
	succeeded, failed, cancelled, pending := 0, 0, 0, 0
	var failures []string
	for _, job := range q.jobs {
		switch job.Status {
		case queueSucceeded:
			succeeded++
		case queueFailed:
			failed++
			failures = append(failures, fmt.Sprintf("%s: %s", filepath.Base(job.ProjectPath), summarizeQueueError(job.Err)))
		case queueCancelled:
			cancelled++
		default:
			pending++
		}
	}
	message := fmt.Sprintf("Completed %d of %d drizzle jobs.", succeeded, len(q.jobs))
	title := "Drizzle Queue Complete"
	if failed > 0 {
		title = "Drizzle Queue Completed with Failures"
		message += fmt.Sprintf(" %d failed.", failed)
		message += "\n\n" + strings.Join(failures, "\n")
	}
	if cancelled > 0 || pending > 0 {
		message += fmt.Sprintf("\nCancelled: %d  Unrun: %d", cancelled, pending)
	}
	q.ws.app.SendNotification(fyne.NewNotification(title, fmt.Sprintf("Succeeded: %d, failed: %d, cancelled: %d, unrun: %d", succeeded, failed, cancelled, pending)))
	dialog.ShowInformation(title, message, q.win)
}

const queueErrorDisplayLimit = 200

// summarizeQueueError keeps failure dialogs and queue rows compact while the
// complete error remains available on the queue job for diagnostics.
func summarizeQueueError(message string) string {
	runes := []rune(strings.TrimSpace(message))
	if len(runes) <= queueErrorDisplayLimit {
		return string(runes)
	}
	return string(runes[:queueErrorDisplayLimit-3]) + "..."
}
