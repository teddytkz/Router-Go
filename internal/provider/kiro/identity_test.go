package kiro

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"testing"
)

// TestIdentityMatchesKiroIDE checks the exact shape Kiro's gate parses: an aws-sdk-js UA ending in KiroIDE-<version>-<64 hex>.
func TestIdentityMatchesKiroIDE(t *testing.T) {
	h := identity("seed", true)
	u := regexp.MustCompile(`^aws-sdk-js/1\.0\.0 ua/2\.1 os/windows#10\.0\.26200 lang/js md/nodejs#22\.21\.1 api/codewhispererstreaming#1\.0\.0 m/E KiroIDE-1\.4\.154-[0-9a-f]{64}$`)
	if !u.MatchString(h["User-Agent"]) {
		t.Errorf("streaming UA = %q", h["User-Agent"])
	}
	if !regexp.MustCompile(`^aws-sdk-js/1\.0\.0 KiroIDE-1\.4\.154-[0-9a-f]{64}$`).MatchString(h["X-Amz-User-Agent"]) {
		t.Errorf("X-Amz-User-Agent = %q", h["X-Amz-User-Agent"])
	}
	if r := identity("seed", false)["User-Agent"]; r == h["User-Agent"] || !regexp.MustCompile(`api/codewhispererruntime#1\.0\.0 m/N,E`).MatchString(r) {
		t.Errorf("runtime UA = %q", r)
	}
	// the machine id is stable per account and differs between accounts; the seed itself never appears
	a, b := identity("a", true), identity("b", true)
	if a["User-Agent"] == b["User-Agent"] || identity("a", true)["User-Agent"] != a["User-Agent"] {
		t.Error("machine id is not stable per seed")
	}
	h2 := sha256.Sum256([]byte("a"))
	if want := "KiroIDE-1.4.154-" + hex.EncodeToString(h2[:]); !regexp.MustCompile(regexp.QuoteMeta(want)).MatchString(a["User-Agent"]) {
		t.Errorf("machine id is not sha256(seed): %q", a["User-Agent"])
	}
	if credentialSeed(map[string]any{"refreshToken": "rt", "clientId": "cid"}, "tok") != "cid" ||
		credentialSeed(nil, "tok") != "tok" || credentialSeed(nil, "") != "" {
		t.Error("credentialSeed precedence")
	}
}
