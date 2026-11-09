package provider

import (
	"strconv"
	"strings"
	"time"
)

// Live quotas: what the provider itself reports for one connection (used / total / reset), as opposed to the limits the
// user defines in the router. The JSON tags are the shape the dashboard reads (same as XRouter-Next's /api/usage/{id}).

// UsageCall is what a Usage hook gets for one connection.
type UsageCall struct {
	Token   string         // the key sent upstream: the API key, or the (refreshed) access token
	Data    map[string]any // the connection's providerSpecificData
	BaseURL string         // the provider's base URL (overridable in tests)
}

// Quota is one quota row; ResetAt is an ISO timestamp or nil when unknown.
type Quota struct {
	Used        float64  `json:"used"`
	Total       float64  `json:"total"`
	Remaining   *float64 `json:"remaining,omitempty"`
	ResetAt     *string  `json:"resetAt"`
	Unlimited   bool     `json:"unlimited,omitempty"`
	DisplayName string   `json:"displayName,omitempty"`
	Recurring   *bool    `json:"recurring,omitempty"` // false = one-shot credit: ResetAt is an expiry, not a refill
}

// Usage is a connection's quota report. Message explains why Quotas is empty (expired sign-in, nothing to report).
type Usage struct {
	Plan    string           `json:"plan,omitempty"`
	Message string           `json:"message,omitempty"`
	Quotas  map[string]Quota `json:"quotas"`
	Raw     any              `json:"raw,omitempty"`
}

// ParseReset turns a reset value (ISO string, unix seconds or milliseconds, number or numeric string) into an ISO
// timestamp; nil when it is empty or not a date.
func ParseReset(v any) *string {
	var t time.Time
	switch x := v.(type) {
	case float64:
		t = unixAuto(x)
	case int64:
		t = unixAuto(float64(x))
	case string:
		x = strings.TrimSpace(x)
		if n, err := strconv.ParseFloat(x, 64); err == nil && !strings.ContainsAny(x, "-:TZ") {
			t = unixAuto(n)
		} else if p, err := time.Parse(time.RFC3339Nano, x); err == nil {
			t = p
		} else if p, err := time.Parse(time.DateTime, x); err == nil { // "2026-01-31 00:00:00", read as UTC
			t = p
		} else {
			return nil
		}
	default:
		return nil
	}
	if t.IsZero() {
		return nil
	}
	s := t.UTC().Format("2006-01-02T15:04:05.000Z")
	return &s
}

func unixAuto(n float64) time.Time {
	if n <= 0 {
		return time.Time{}
	}
	if n < 1e12 { // seconds
		return time.UnixMilli(int64(n * 1000))
	}
	return time.UnixMilli(int64(n))
}

// Num reads a JSON number, or a numeric string, as a float (0 when it is neither).
func Num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f
	}
	return 0
}

// Snippet shortens an upstream error body for a message.
func Snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
