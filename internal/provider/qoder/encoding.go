package qoder

import (
	"encoding/base64"
	"strings"
)

// Qoder's WAF bypass: standard base64, then the string is split into thirds and reordered as [tail][mid][head], then
// every alphabet character is swapped. The server decodes in reverse because the URL carries &Encode=1.
const (
	stdAlphabet    = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	customAlphabet = "_doRTgHZBKcGVjlvpC,@aFSx#DPuNJme&i*MzLOEn)sUrthbf%Y^w.(kIQyXqWA!"
)

var s2c = func() (t [128]byte) {
	for i := range t {
		t[i] = byte(i)
	}
	for i := range len(stdAlphabet) {
		t[stdAlphabet[i]] = customAlphabet[i]
	}
	t['='] = '$'
	return t
}()

func encodeBody(plain []byte) []byte {
	s := base64.StdEncoding.EncodeToString(plain)
	n := len(s)
	a := n / 3
	r := s[n-a:] + s[a:n-a] + s[:a]
	out := make([]byte, n)
	for i := range n {
		c := r[i]
		if c < 128 {
			out[i] = s2c[c]
		} else {
			out[i] = c
		}
	}
	return out
}

// decodeBody is encodeBody backwards: only used to check our own encoding.
func decodeBody(enc []byte) ([]byte, error) {
	n := len(enc)
	a := n / 3
	r := string(enc)
	// enc = tail(a) + mid(n-2a) + head(a), so head+mid+tail restores the base64 text
	s := r[n-a:] + r[a:n-a] + r[:a]
	var b strings.Builder
	b.Grow(n)
	for i := range n {
		c := s[i]
		switch {
		case c == '$':
			b.WriteByte('=')
		default:
			if j := strings.IndexByte(customAlphabet, c); j >= 0 {
				b.WriteByte(stdAlphabet[j])
			} else {
				b.WriteByte(c)
			}
		}
	}
	return base64.StdEncoding.DecodeString(b.String())
}
