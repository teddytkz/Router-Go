package kiro

import (
	"crypto/sha256"
	"encoding/hex"
)

// Kiro refuses clients that do not identify as a current Kiro IDE ("This version of Kiro is no longer supported",
// HTTP 400), so every Kiro service call carries the identity of a real IDE build: aws-sdk-js headers whose last field
// is a per-account machine id. Ported from glud123's fix for decolua/9router#5613 (2026-11-13); the TLS half of that
// fix is Node-specific, Go's crypto/tls already offers the curves AWS OIDC accepts.
const kiroVersion = "1.4.154"

// identity is the User-Agent / X-Amz-User-Agent pair for one credential. streaming selects the inference client
// (codewhispererstreaming, metrics E); everything else is the runtime service (codewhispererruntime, metrics N,E).
func identity(seed string, streaming bool) map[string]string {
	if seed == "" {
		seed = "kiro-anonymous"
	}
	h := sha256.Sum256([]byte(seed))
	api, metrics := "codewhispererruntime", "N,E"
	if streaming {
		api, metrics = "codewhispererstreaming", "E"
	}
	tag := "KiroIDE-" + kiroVersion + "-" + hex.EncodeToString(h[:])
	return map[string]string{
		"User-Agent":       "aws-sdk-js/1.0.0 ua/2.1 os/windows#10.0.26200 lang/js md/nodejs#22.21.1 api/" + api + "#1.0.0 m/" + metrics + " " + tag,
		"X-Amz-User-Agent": "aws-sdk-js/1.0.0 " + tag,
	}
}

// credentialSeed is the stable per-account value the machine id is hashed from; the credential itself never leaves
// in a header, only its hash does.
func credentialSeed(data map[string]any, token string) string {
	for _, k := range []string{"clientId", "refreshToken", "profileArn", "apiKey"} {
		if v := str(data, k); v != "" {
			return v
		}
	}
	return token
}
