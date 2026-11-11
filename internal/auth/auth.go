// Package auth holds dashboard login (bcrypt + JWT cookie), the login limiter and gateway API keys.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

const CookieName = "auth_token"
const sessionTTL = 24 * time.Hour

// ---- password ----

func HashPassword(p string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(p), 10)
	return string(b), err
}

func CheckPassword(hash, p string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(p)) == nil
}

// ---- JWT session ----

type Session struct{ secret []byte }

func NewSession(secret []byte) *Session { return &Session{secret: secret} }

func (s *Session) Issue() (string, error) {
	return jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"authenticated": true, "iat": time.Now().Unix(), "exp": time.Now().Add(sessionTTL).Unix(),
	}).SignedString(s.secret)
}

func (s *Session) Valid(token string) bool {
	if token == "" {
		return false
	}
	t, err := jwt.Parse(token, func(*jwt.Token) (any, error) { return s.secret, nil }, jwt.WithValidMethods([]string{"HS256"}))
	return err == nil && t.Valid
}

func (s *Session) SetCookie(w http.ResponseWriter, secure bool) error {
	tok, err := s.Issue()
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: tok, Path: "/", HttpOnly: true, Secure: secure,
		SameSite: http.SameSiteLaxMode, MaxAge: int(sessionTTL.Seconds())})
	return nil
}

func ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", HttpOnly: true, MaxAge: -1})
}

func (s *Session) FromRequest(r *http.Request) bool {
	c, err := r.Cookie(CookieName)
	return err == nil && s.Valid(c.Value)
}

// ---- client IP ----

// ClientIP uses the TCP peer. X-Forwarded-For is honoured only when TRUST_PROXY is set and the peer is loopback,
// so a remote client cannot rotate the header to escape its own lockout bucket.
func ClientIP(r *http.Request, trustProxy bool) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if trustProxy && IsLoopback(host) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			return strings.TrimSpace(strings.Split(xff, ",")[0])
		}
	}
	return host
}

func IsLoopback(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ---- login limiter (progressive lockout, in-memory) ----

const maxFails = 5

var lockSteps = []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute}

type entry struct {
	fails, level      int
	lockUntil, lastAt time.Time
}

type Limiter struct {
	mu sync.Mutex
	m  map[string]*entry
}

func NewLimiter() *Limiter { return &Limiter{m: map[string]*entry{}} }

func (l *Limiter) get(ip string) *entry {
	e := l.m[ip]
	if e != nil && time.Since(e.lastAt) > time.Hour && time.Now().After(e.lockUntil) {
		delete(l.m, ip)
		return nil
	}
	return e
}

// Locked returns the remaining lock time in seconds (0 = not locked).
func (l *Limiter) Locked(ip string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.get(ip); e != nil && time.Now().Before(e.lockUntil) {
		return int(time.Until(e.lockUntil).Seconds()) + 1
	}
	return 0
}

// Fail records a failed attempt and returns attempts left before lockout.
func (l *Limiter) Fail(ip string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.get(ip)
	if e == nil {
		e = &entry{}
		l.m[ip] = e
	}
	e.fails++
	e.lastAt = time.Now()
	if e.fails >= maxFails {
		e.lockUntil = time.Now().Add(lockSteps[min(e.level, len(lockSteps)-1)])
		e.level++
		e.fails = 0
	}
	return maxFails - e.fails
}

func (l *Limiter) Success(ip string) { l.mu.Lock(); delete(l.m, ip); l.mu.Unlock() }

// ---- gateway API keys: sk-{machineId}-{keyId}-{crc8} ----

func crc(secret, machineID, keyID string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(machineID + keyID))
	return hex.EncodeToString(h.Sum(nil))[:8]
}

func NewAPIKey(secret, machineID string) string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	id := string(b)
	return "sk-" + machineID + "-" + id + "-" + crc(secret, machineID, id)
}

// BearerToken extracts the key from Authorization: Bearer or x-api-key.
func BearerToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return strings.TrimSpace(r.Header.Get("x-api-key"))
}
