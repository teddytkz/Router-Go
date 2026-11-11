//go:build windows

package tray

import (
	"syscall"
	"time"

	"fyne.io/systray"
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	user32           = syscall.NewLazyDLL("user32.dll")
	getConsoleWindow = kernel32.NewProc("GetConsoleWindow")
	getAncestor      = user32.NewProc("GetAncestor")
	showWindow       = user32.NewProc("ShowWindow")
)

const (
	swHide, swShow = 0, 5
	gaRootOwner    = 3
)

// console is the window that shows this process's log. Under Windows Terminal the console window is an invisible
// pseudo-console, so its root owner (the terminal window) is the one to hide.
func console() uintptr {
	h, _, _ := getConsoleWindow.Call()
	if h == 0 {
		return 0
	}
	if root, _, _ := getAncestor.Call(h, gaRootOwner); root != 0 {
		return root
	}
	return h
}

// Start shows the tray icon and returns a func that removes it. onExit runs when the user picks Exit.
func Start(icon []byte, onExit func()) (stop func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		systray.Run(func() {
			systray.SetIcon(ico(icon))
			systray.SetTooltip("Router-Go")
			var toggleCh <-chan struct{}
			var toggle *systray.MenuItem
			if console() != 0 { // started with --background there is no console window to hide
				toggle = systray.AddMenuItem("Hide to tray", "Hide the console window")
				toggleCh = toggle.ClickedCh
			}
			exit := systray.AddMenuItem("Exit", "Stop Router-Go")
			hidden := false
			for {
				select {
				case <-toggleCh:
					w := console()
					if hidden = !hidden; hidden {
						showWindow.Call(w, swHide)
						toggle.SetTitle("Show console")
					} else {
						showWindow.Call(w, swShow)
						toggle.SetTitle("Hide to tray")
					}
				case <-exit.ClickedCh:
					onExit()
					return
				}
			}
		}, nil)
	}()
	return func() {
		systray.Quit() // removes the icon, otherwise a ghost stays in the tray until the mouse passes over it
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
}
