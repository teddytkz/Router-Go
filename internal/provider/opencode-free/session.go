package opencodefree

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

var (
	sessionOnce sync.Once
	sessionID   string
)

// stableSession is one canonical session for the process. Upstream meters the free allowance per session, so minting
// a new one per request burns the allowance; the CLI keeps one per conversation.
func stableSession() string {
	sessionOnce.Do(func() { sessionID = newSessionID(time.Now()) })
	return sessionID
}

// newSessionID mints "ses_" + 12 hex + 14 base62. The hex half is the CLI's descending timestamp prefix: upstream
// decodes it and refuses sessions whose time is in the future, so random hex does not pass.
func newSessionID(now time.Time) string {
	const mask48 = 1<<48 - 1
	prefix := ^(uint64(now.UnixMilli())<<12 + 1) & mask48
	return "ses_" + hex.EncodeToString([]byte{
		byte(prefix >> 40), byte(prefix >> 32), byte(prefix >> 24),
		byte(prefix >> 16), byte(prefix >> 8), byte(prefix),
	}) + randBase62(14)
}

func randBase62(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = base62[int(b[i])%62]
	}
	return string(b)
}
