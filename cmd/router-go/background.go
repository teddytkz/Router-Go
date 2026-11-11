package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// detach starts a copy of this program without a terminal (stdout/stderr go to <dataDir>/logs/background.log) and
// returns its pid; the caller then exits. The copy is given the same arguments minus --background.
// shortcut: background.log is appended to and never rotated; add rotation if it grows too large.
func detach(dataDir string) (pid int, logFile string, err error) {
	self, err := os.Executable()
	if err != nil {
		return 0, "", err
	}
	var args []string
	for _, a := range os.Args[1:] {
		if a != "-background" && a != "--background" && !strings.HasPrefix(a, "-background=") && !strings.HasPrefix(a, "--background=") {
			args = append(args, a)
		}
	}
	logFile = filepath.Join(dataDir, "logs", "background.log")
	if err := os.MkdirAll(filepath.Dir(logFile), 0o755); err != nil {
		return 0, "", err
	}
	out, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, "", err
	}
	defer out.Close()
	cmd := exec.Command(self, args...)
	cmd.Stdout, cmd.Stderr, cmd.SysProcAttr = out, out, detachAttr()
	if err := cmd.Start(); err != nil {
		return 0, logFile, err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case <-exited: // a copy that dies this fast failed to start (port taken, bad flag, ...)
		return 0, logFile, fmt.Errorf("the background copy exited at once")
	case <-time.After(1500 * time.Millisecond):
		return cmd.Process.Pid, logFile, nil
	}
}
