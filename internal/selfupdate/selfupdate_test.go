package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// tinyBinary compiles a stand-in router-go, so verify sees a real executable of this platform.
func tinyBinary(t *testing.T, msg string) []byte {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte("package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\""+msg+"\") }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "bin")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	if b, err := exec.Command("go", "build", "-o", out, src).CombinedOutput(); err != nil {
		t.Skipf("cannot build a stand-in binary: %v %.200s", err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestInstallsDifferentBinaryAndSkipsTheSameOne(t *testing.T) {
	newBin := tinyBinary(t, "router-go 9.9.9")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write(newBin) }))
	defer srv.Close()

	exe := filepath.Join(t.TempDir(), "router-go")
	old := tinyBinary(t, "router-go 0.0.1")
	if err := os.WriteFile(exe, old, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CheckURL(srv.URL); err != nil {
		t.Fatalf("a loopback http URL must pass: %v", err)
	}
	got, err := check(srv.URL, exe)
	if err != nil || !got {
		t.Fatalf("expected an install, got %v, %v", got, err)
	}
	b, err := os.ReadFile(exe)
	if err != nil || string(b) != string(newBin) {
		t.Fatalf("binary not replaced: %v, %q", err, b)
	}
	if hex.EncodeToString(sum(old)) != fileSHABytes(old) {
		t.Fatal("hash helper disagrees with itself")
	}
	got, err = check(srv.URL, exe)
	if err != nil || got {
		t.Fatalf("same hash must be skipped, got %v, %v", got, err)
	}
	if fi, _ := os.Stat(exe); fi.Mode().Perm()&0o100 == 0 {
		t.Fatal("the installed binary is not executable")
	}
}

func TestRefusesBadDownloads(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "router-go")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"html error page": "<html>404</html>",
		"empty":           "",
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) }))
		got, err := check(srv.URL, exe)
		srv.Close()
		if err == nil || got {
			t.Fatalf("%s: expected a refusal, got %v, %v", name, got, err)
		}
		if b, _ := os.ReadFile(exe); string(b) != "old" {
			t.Fatalf("%s: the old binary was touched", name)
		}
	}
	for _, u := range []string{"http://example.com/router-go", "https://example.com/router-go"} {
		if err := CheckURL(u); err != nil {
			t.Fatalf("%s must be accepted: %v", u, err)
		}
	}
	if err := CheckURL("ftp://example.com/router-go"); err == nil {
		t.Fatal("a non-http URL must be refused")
	}
}

func TestDownloadRejectsNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) }))
	defer srv.Close()
	if _, err := download(srv.URL); err == nil {
		t.Fatal("a 404 must be an error")
	}
}

func fileSHABytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
