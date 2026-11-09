package db

import (
	"path/filepath"
	"testing"
	"time"
)

func TestWindowBounds(t *testing.T) {
	loc := time.FixedZone("x", 7*3600)
	now := time.Date(2026, 11, 11, 15, 30, 0, 0, loc) // a Wednesday
	for name, want := range map[string][2]time.Time{
		"day":   {time.Date(2026, 11, 11, 0, 0, 0, 0, loc), time.Date(2026, 11, 12, 0, 0, 0, 0, loc)},
		"week":  {time.Date(2026, 11, 9, 0, 0, 0, 0, loc), time.Date(2026, 11, 16, 0, 0, 0, 0, loc)}, // Monday to Monday
		"month": {time.Date(2026, 11, 1, 0, 0, 0, 0, loc), time.Date(2026, 12, 1, 0, 0, 0, 0, loc)},
	} {
		if s, e := windowBounds(name, "calendar", now); !s.Equal(want[0]) || !e.Equal(want[1]) {
			t.Errorf("%s: got %v..%v want %v..%v", name, s, e, want[0], want[1])
		}
	}
	if s, e := windowBounds("hour", "calendar", now); s.Minute() != 0 || e.Sub(s) != time.Hour {
		t.Errorf("hour: %v..%v", s, e)
	}
	if s, e := windowBounds("day", "rolling", now); !s.Equal(now.Add(-24*time.Hour)) || !e.IsZero() {
		t.Errorf("rolling: %v..%v", s, e)
	}
}

func TestQuotaStateCountsTheRightUsage(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "q.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	add := func(provider, conn, status string, tokens int, ago time.Duration) {
		s.AddUsage(Usage{Timestamp: now.Add(-ago).UTC().Format("2006-01-02T15:04:05.000Z"), Provider: provider, ConnectionID: conn, Status: status, PromptTokens: tokens, CompletionTokens: tokens})
	}
	add("p1", "c1", "ok", 10, time.Minute)
	add("p1", "c1", "error", 10, 2*time.Minute)
	add("p1", "c2", "ok", 5, 3*time.Minute)
	add("p1", "c1", "canceled", 100, time.Minute) // never counts
	add("p2", "c3", "ok", 1, time.Minute)
	add("p1", "c1", "ok", 50, 3*time.Hour) // outside the last hour

	l := QuotaLimit{Scope: "provider", Target: "p1", Metric: "requests", Window: "hour", Mode: "rolling", Limit: 3}
	if st, _ := s.QuotaState(l, now); st.Used != 3 || !st.Exhausted {
		t.Errorf("provider requests: %+v", st)
	}
	l.Metric, l.Limit = "tokens", 100
	if st, _ := s.QuotaState(l, now); st.Used != 50 || st.Exhausted { // (10+10)+(10+10)+(5+5)
		t.Errorf("provider tokens: %+v", st)
	}
	l = QuotaLimit{Scope: "connection", Target: "c1", Metric: "requests", Window: "hour", Mode: "rolling", Limit: 2}
	st, _ := s.QuotaState(l, now)
	if st.Used != 2 || !st.Exhausted || st.ResetAt.Before(now) || st.ResetAt.After(now.Add(time.Hour)) {
		t.Errorf("connection: %+v", st)
	}
	l.Limit = 3
	if st, _ := s.QuotaState(l, now); st.Exhausted {
		t.Errorf("below the limit must not be exhausted: %+v", st)
	}
}
