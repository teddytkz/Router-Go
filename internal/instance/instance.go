// Package instance keeps one router-go per data directory: starting a second one stops the first.
package instance

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/process"
)

// Replace stops the router-go recorded in <dataDir>/router-go.pid (if it is still running) and records this process
// instead. The returned func removes the pid file on a clean exit.
// shortcut: two copies started at the very same instant can both pass the check; add a file lock if that ever matters.
func Replace(dataDir string) (release func(), err error) {
	file := filepath.Join(dataDir, "router-go.pid")
	if b, e := os.ReadFile(file); e == nil {
		if pid, e := strconv.Atoi(strings.TrimSpace(string(b))); e == nil && pid != os.Getpid() {
			stop(pid)
		}
	}
	me := strconv.Itoa(os.Getpid())
	if err := os.WriteFile(file, []byte(me), 0o600); err != nil {
		return func() {}, err
	}
	return func() {
		if b, e := os.ReadFile(file); e == nil && strings.TrimSpace(string(b)) == me {
			_ = os.Remove(file)
		}
	}, nil
}

// stop ends pid, but only when it is a router-go: a stale pid file may name a process that has since reused the number.
func stop(pid int) {
	p, err := process.NewProcess(int32(pid))
	if err != nil {
		return // not running
	}
	name, err := p.Name()
	self, _ := os.Executable()
	if err != nil || !sameProgram(name, filepath.Base(self)) {
		return
	}
	_ = p.Terminate() // SIGTERM lets the old one drain; on Windows this ends it at once
	if !waitGone(p, 8*time.Second) {
		_ = p.Kill()
		waitGone(p, 2*time.Second)
	}
}

// sameProgram compares process and executable names; Linux truncates a process name to 15 characters.
func sameProgram(name, exe string) bool {
	name, exe = strings.ToLower(name), strings.ToLower(exe)
	return name == exe || (len(name) == 15 && strings.HasPrefix(exe, name))
}

func waitGone(p *process.Process, d time.Duration) bool {
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		if ok, err := p.IsRunning(); err != nil || !ok {
			return true
		}
	}
	return false
}
