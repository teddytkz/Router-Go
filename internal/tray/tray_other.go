//go:build !windows

package tray

// Start does nothing here: only Windows has a console window to hide. It returns a no-op stop.
func Start(icon []byte, onExit func()) (stop func()) { return func() {} }
