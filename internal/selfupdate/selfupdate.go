// Package selfupdate keeps the running binary in step with one published at a URL.
//
// There is no build version to compare: identity is the file's SHA-256, so a published binary whose hash differs from
// the running one is installed and the process restarts into it.
package selfupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"router-go/internal/consolelog"
)

const (
	Interval = 6 * time.Hour
	maxSize  = 200 << 20
)

// wake asks a running Run to check now, instead of waiting out the interval.
var wake = make(chan struct{}, 1)

// Wake makes the background check run at once. It does nothing when Run is not running.
func Wake() {
	select {
	case wake <- struct{}{}:
	default: // a wake-up is already queued
	}
}

// Run checks url every Interval until ctx is done, and also whenever Wake is called. After installing a different
// binary it calls restart and returns; restart only fails when the process could not be replaced, in which case the
// new binary is already on disk and takes effect on the next start. An empty url disables the check.
func Run(ctx context.Context, url func() string, restart func()) {
	exe, err := os.Executable()
	if err != nil {
		consolelog.Logf("WARN", "UPDATE", "cannot locate this binary: %v", err)
		return
	}
	first := true
	for {
		if !first {
			select {
			case <-ctx.Done():
				return
			case <-time.After(Interval):
			case <-wake:
			}
		}
		first = false
		raw := url()
		if raw == "" {
			continue
		}
		ok, err := check(raw, exe)
		switch {
		case err != nil:
			consolelog.Logf("WARN", "UPDATE", "%v", err)
		case ok:
			consolelog.Logf("INFO", "UPDATE", "a new binary was installed, restarting")
			restart()
			return
		}
	}
}

// CheckMessage tells the dashboard whether rawURL holds another binary, without installing it.
func CheckMessage(rawURL string) (diff bool, msg string, err error) {
	if rawURL == "" {
		return false, "", fmt.Errorf("no update URL is set")
	}
	if diff, err = Check(rawURL); err != nil {
		return false, "", err
	}
	if diff {
		return true, "A different binary is published, so it is being installed and the server restarts in a few seconds.", nil
	}
	return false, "This binary is the one published at the update URL.", nil
}

// Check downloads rawURL and returns true when it is a different binary from the one on disk; nothing is installed.
func Check(rawURL string) (bool, error) {
	exe, err := os.Executable()
	if err != nil {
		return false, err
	}
	return different(rawURL, exe)
}

// check downloads rawURL and, when it is a different binary that starts, replaces exe with it.
func check(rawURL, exe string) (bool, error) {
	diff, err := different(rawURL, exe)
	if err != nil || !diff {
		return false, err
	}
	b, err := download(rawURL)
	if err != nil {
		return false, err
	}
	if err := verify(b); err != nil {
		return false, err
	}
	return true, install(exe, b)
}

// different reports whether rawURL holds another binary than exe, without installing anything.
func different(rawURL, exe string) (bool, error) {
	if err := CheckURL(rawURL); err != nil {
		return false, err
	}
	before, err := fileSHA(exe)
	if err != nil {
		return false, err
	}
	b, err := download(rawURL)
	if err != nil {
		return false, err
	}
	return hex.EncodeToString(sum(b)) != before, nil
}

// CheckURL accepts http and https. Plain http means anyone who can intercept the connection can serve a binary, and
// that binary runs with the gateway's keys; https is the safe choice whenever the host offers it.
func CheckURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("update URL: %w", err)
	}
	if (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
		return nil
	}
	return fmt.Errorf("update URL %q must start with http:// or https://", rawURL)
}

func download(rawURL string) ([]byte, error) {
	c := &http.Client{Timeout: 10 * time.Minute}
	resp, err := c.Get(rawURL)
	if err != nil {
		return nil, fmt.Errorf("update: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("update: %s answered %s", rawURL, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
	if err != nil {
		return nil, fmt.Errorf("update: %w", err)
	}
	if len(b) > maxSize {
		return nil, fmt.Errorf("update: %s is larger than %d MB", rawURL, maxSize>>20)
	}
	if n := resp.ContentLength; n > 0 && int64(len(b)) != n {
		return nil, fmt.Errorf("update: download is cut short (%d of %d bytes)", len(b), n)
	}
	return b, nil
}

// verify refuses anything that is not this platform's executable format and then proves it starts, so a CDN error
// page or a truncated download cannot replace a working binary.
func verify(b []byte) error {
	var magic [][]byte
	switch runtime.GOOS {
	case "windows":
		magic = [][]byte{{'M', 'Z'}}
	case "darwin":
		magic = [][]byte{{0xcf, 0xfa, 0xed, 0xfe}, {0xce, 0xfa, 0xed, 0xfe}}
	default:
		magic = [][]byte{{0x7f, 'E', 'L', 'F'}}
	}
	ok := false
	for _, m := range magic {
		ok = ok || bytes.HasPrefix(b, m)
	}
	if !ok {
		return fmt.Errorf("update: the download is not a %s executable", runtime.GOOS)
	}
	name := "router-go-*"
	if runtime.GOOS == "windows" {
		name = "router-go-*.exe"
	}
	f, err := os.CreateTemp("", name)
	if err != nil {
		return fmt.Errorf("update: %w", err)
	}
	defer os.Remove(f.Name())
	_, werr := f.Write(b)
	cerr := f.Close()
	if werr != nil {
		return fmt.Errorf("update: %w", werr)
	}
	if cerr != nil {
		return fmt.Errorf("update: %w", cerr)
	}
	if err := os.Chmod(f.Name(), 0o755); err != nil {
		return fmt.Errorf("update: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, f.Name(), "-version").CombinedOutput(); err != nil {
		return fmt.Errorf("update: the new binary does not start (%v): %.200s", err, out)
	}
	return nil
}

// install writes b beside exe and renames it over the old file. On Unix a running binary is replaced in place; on
// Windows it is locked, so it is renamed aside first and dropped on a later update.
// shortcut: replaces the real file, so a symlink pointing at it keeps the old name but not the old content.
func install(exe string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(exe), ".router-go-update-*")
	if err != nil {
		return fmt.Errorf("update: %w (is the directory writable?)", err)
	}
	name := tmp.Name()
	fail := func(err error) error {
		os.Remove(name)
		return fmt.Errorf("update: %w", err)
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return fail(err)
	}
	if err := os.Chmod(name, 0o755); err != nil {
		return fail(err)
	}
	if runtime.GOOS == "windows" {
		old := exe + ".old"
		_ = os.Remove(old)
		if err := os.Rename(exe, old); err != nil {
			return fail(err)
		}
		if err := os.Rename(name, exe); err != nil {
			_ = os.Rename(old, exe)
			return fail(err)
		}
		return nil
	}
	if err := os.Rename(name, exe); err != nil {
		return fail(err)
	}
	return nil
}

func fileSHA(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("update: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("update: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func sum(b []byte) []byte {
	s := sha256.Sum256(b)
	return s[:]
}
