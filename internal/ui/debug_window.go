package ui

import (
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/debuglog"
)

var (
	debugWin   fyne.Window
	debugWinMu sync.Mutex
	debugLines []string
	debugDirty bool
	debugList  *widget.List
)

func showDebugWindow(a fyne.App) {
	debugWinMu.Lock()
	if debugWin != nil {
		debugWin.Show()
		debugWin.RequestFocus()
		debugWinMu.Unlock()
		return
	}
	debugWinMu.Unlock()

	list := widget.NewList(
		func() int {
			debugWinMu.Lock()
			defer debugWinMu.Unlock()
			return len(debugLines)
		},
		func() fyne.CanvasObject {
			l := widget.NewLabel("")
			l.TextStyle = fyne.TextStyle{Monospace: true}
			l.Truncation = fyne.TextTruncateEllipsis
			return l
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			debugWinMu.Lock()
			line := ""
			if id < len(debugLines) {
				line = debugLines[id]
			}
			debugWinMu.Unlock()
			obj.(*widget.Label).SetText(line)
		},
	)
	debugList = list

	debuglog.SetHandler(func(msg string) {
		debugWinMu.Lock()
		debugLines = append(debugLines, msg)
		debugDirty = true
		debugWinMu.Unlock()
	})

	// Batch UI updates: refresh the list at most every 100 ms.
	ticker := time.NewTicker(100 * time.Millisecond)
	go func() {
		for range ticker.C {
			debugWinMu.Lock()
			dirty := debugDirty
			n := len(debugLines)
			debugDirty = false
			debugWinMu.Unlock()
			if dirty && n > 0 {
				fyne.Do(func() {
					list.Refresh()
					list.ScrollToBottom()
				})
			}
		}
	}()

	clearBtn := widget.NewButton("Clear", func() {
		debugWinMu.Lock()
		debugLines = nil
		debugDirty = false
		debugWinMu.Unlock()
		list.Refresh()
	})

	win := a.NewWindow("Debug Log")
	win.SetContent(container.NewBorder(nil, clearBtn, nil, nil, list))
	win.Resize(fyne.NewSize(700, 400))
	win.SetOnClosed(func() {
		ticker.Stop()
		debugWinMu.Lock()
		debugWin = nil
		debugList = nil
		debugWinMu.Unlock()
	})

	debugWinMu.Lock()
	debugWin = win
	debugWinMu.Unlock()
	win.Show()
}
