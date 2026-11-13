//go:build !windows

package main

import (
	"os"
	"syscall"
)

// restartSelf replaces this process image with the binary now on disk. Exec keeps the pid, and the listening socket
// is close-on-exec, so there is no moment with two processes on the port; shutdown is not needed.
// shortcut: no graceful drain, so an open stream (SSE) is dropped; the client retries against the new binary.
func restartSelf(_ func()) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(exe, os.Args, os.Environ())
}
