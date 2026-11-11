package instance

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestMain doubles as the "old instance": a copy of this very binary that just waits, so its name matches ours.
func TestMain(m *testing.M) {
	if os.Getenv("INSTANCE_TEST_SLEEP") == "1" {
		time.Sleep(time.Minute)
		return
	}
	os.Exit(m.Run())
}

func spawn(t *testing.T, name string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "INSTANCE_TEST_SLEEP=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go cmd.Wait() // reap it, or it stays a zombie that still looks alive
	t.Cleanup(func() { cmd.Process.Kill() })
	return cmd
}

func alive(pid int) bool {
	return os.Getpid() != pid && exec.Command("kill", "-0", strconv.Itoa(pid)).Run() == nil
}

func TestReplaceStopsOldInstance(t *testing.T) {
	dir := t.TempDir()
	old := spawn(t, os.Args[0])
	os.WriteFile(filepath.Join(dir, "router-go.pid"), []byte(strconv.Itoa(old.Process.Pid)), 0o600)
	release, err := Replace(dir)
	if err != nil {
		t.Fatal(err)
	}
	if alive(old.Process.Pid) {
		t.Fatal("old instance still running")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "router-go.pid")); strings.TrimSpace(string(b)) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("pid file = %q, want our pid", b)
	}
	release()
	if _, err := os.Stat(filepath.Join(dir, "router-go.pid")); err == nil {
		t.Fatal("pid file not removed on release")
	}
}

// A pid that now belongs to some other program must be left alone.
func TestReplaceSparesOtherProgram(t *testing.T) {
	dir := t.TempDir()
	other := spawn(t, "sleep", "60")
	os.WriteFile(filepath.Join(dir, "router-go.pid"), []byte(strconv.Itoa(other.Process.Pid)), 0o600)
	if _, err := Replace(dir); err != nil {
		t.Fatal(err)
	}
	if !alive(other.Process.Pid) {
		t.Fatal("an unrelated process was killed")
	}
}
