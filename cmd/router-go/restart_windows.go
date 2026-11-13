//go:build windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

// restartSelf ends this process (after shutdown drains requests) and starts the new binary with the same arguments.
// The running file was renamed aside by the updater, because Windows locks an executable while it runs.
func restartSelf(shutdown func()) error {
	shutdown()
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000008 | 0x00000200, HideWindow: true}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil // unreachable
}
