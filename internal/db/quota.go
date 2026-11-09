package db

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// QuotaLimit is a usage budget the user sets on a provider or a single key: N requests or tokens per window.
// It is tracked from the usage history, so it works for every provider; with Enforce set the gateway also stops
// sending traffic to a target whose budget is used up (a combo or the next key takes over).
type QuotaLimit struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Scope     string `json:"scope"`  // "provider" | "connection"
	Target    string `json:"target"` // node/provider id, or connection id
	Metric    string `json:"metric"` // "requests" | "tokens" (prompt + completion)
	Window    string `json:"window"` // "hour" | "day" | "week" | "month"
	Mode      string `json:"mode"`   // "calendar" (resets on the boundary, server local time) | "rolling" (the last window)
	Limit     int64  `json:"limit"`
	Enforce   bool   `json:"enforce"`
	CreatedAt string `json:"createdAt"`
}

// QuotaState is a limit measured against the usage history.
type QuotaState struct {
	Used        int64     `json:"used"`
	WindowStart time.Time `json:"windowStart"`
	ResetAt     time.Time `json:"resetAt"`
	Exhausted   bool      `json:"exhausted"`
}

const quotaScope = "quotaLimits"

func (s *Store) QuotaLimits() ([]QuotaLimit, error) {
	rows, err := s.DB.Query(`SELECT value FROM kv WHERE scope=? ORDER BY key`, quotaScope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []QuotaLimit{}
	for rows.Next() {
		var raw string
		var l QuotaLimit
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(raw), &l) == nil && l.ID != "" {
			out = append(out, l)
		}
	}
	return out, rows.Err()
}

func (s *Store) QuotaLimit(id string) (*QuotaLimit, error) {
	var raw string
	if err := s.DB.QueryRow(`SELECT value FROM kv WHERE scope=? AND key=?`, quotaScope, id).Scan(&raw); err == sql.ErrNoRows {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	var l QuotaLimit
	if err := json.Unmarshal([]byte(raw), &l); err != nil {
		return nil, err
	}
	return &l, nil
}

// SaveQuotaLimit creates a limit (empty ID) or replaces an existing one.
func (s *Store) SaveQuotaLimit(l QuotaLimit) (*QuotaLimit, error) {
	if l.ID == "" {
		l.ID, l.CreatedAt = uuid.NewString(), now()
	} else if old, err := s.QuotaLimit(l.ID); err != nil {
		return nil, err
	} else {
		l.CreatedAt = old.CreatedAt
	}
	v, _ := json.Marshal(l)
	_, err := s.DB.Exec(`INSERT INTO kv(scope,key,value) VALUES(?,?,?) ON CONFLICT(scope,key) DO UPDATE SET value=excluded.value`, quotaScope, l.ID, string(v))
	return &l, err
}

func (s *Store) DeleteQuotaLimit(id string) error {
	r, err := s.DB.Exec(`DELETE FROM kv WHERE scope=? AND key=?`, quotaScope, id)
	return affected(r, err)
}

// windowBounds is where the current window starts, and for calendar windows when it ends.
func windowBounds(window, mode string, now time.Time) (start, end time.Time) {
	y, m, d := now.Date()
	loc := now.Location()
	if mode == "rolling" {
		dur := map[string]time.Duration{"hour": time.Hour, "day": 24 * time.Hour, "week": 7 * 24 * time.Hour, "month": 30 * 24 * time.Hour}[window]
		return now.Add(-dur), time.Time{}
	}
	switch window {
	case "hour":
		start = now.Truncate(time.Hour)
		end = start.Add(time.Hour)
	case "week": // Monday 00:00
		wd := (int(now.Weekday()) + 6) % 7
		start = time.Date(y, m, d-wd, 0, 0, 0, 0, loc)
		end = start.AddDate(0, 0, 7)
	case "month":
		start = time.Date(y, m, 1, 0, 0, 0, 0, loc)
		end = start.AddDate(0, 1, 0)
	default: // day
		start = time.Date(y, m, d, 0, 0, 0, 0, loc)
		end = start.AddDate(0, 0, 1)
	}
	return start, end
}

// QuotaState measures a limit against the usage history at time now.
func (s *Store) QuotaState(l QuotaLimit, now time.Time) (QuotaState, error) {
	start, end := windowBounds(l.Window, l.Mode, now)
	col := map[string]string{"provider": "provider", "connection": "connectionId"}[l.Scope]
	expr := "COUNT(*)"
	if l.Metric == "tokens" {
		expr = "COALESCE(SUM(promptTokens+completionTokens),0)"
	}
	var used int64
	var first sql.NullString
	err := s.DB.QueryRow(`SELECT `+expr+`, MIN(timestamp) FROM usageHistory WHERE `+col+`=? AND timestamp>=? AND status<>'canceled'`,
		l.Target, start.UTC().Format("2006-01-02T15:04:05.000Z")).Scan(&used, &first)
	if err != nil {
		return QuotaState{}, err
	}
	if l.Mode == "rolling" { // usage starts dropping when the oldest request leaves the window
		dur := now.Sub(start)
		if t, perr := time.Parse(time.RFC3339Nano, first.String); perr == nil {
			end = t.Add(dur)
		} else {
			end = now.Add(dur)
		}
	}
	return QuotaState{Used: used, WindowStart: start, ResetAt: end, Exhausted: l.Limit > 0 && used >= l.Limit}, nil
}

// ProviderUsage: requests and tokens per provider for today and for the current month (calendar, server local time).
func (s *Store) ProviderUsage(now time.Time) ([]map[string]any, error) {
	day, _ := windowBounds("day", "calendar", now)
	month, _ := windowBounds("month", "calendar", now)
	rows, err := s.DB.Query(`SELECT COALESCE(provider,''),
		COALESCE(SUM(CASE WHEN timestamp>=? THEN 1 ELSE 0 END),0), COALESCE(SUM(CASE WHEN timestamp>=? THEN promptTokens+completionTokens ELSE 0 END),0),
		COUNT(*), COALESCE(SUM(promptTokens+completionTokens),0)
		FROM usageHistory WHERE timestamp>=? AND status<>'canceled' AND provider<>'' GROUP BY 1 ORDER BY 5 DESC`,
		day.UTC().Format("2006-01-02T15:04:05.000Z"), day.UTC().Format("2006-01-02T15:04:05.000Z"), month.UTC().Format("2006-01-02T15:04:05.000Z"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var p string
		var rd, td, rm, tm int64
		if err := rows.Scan(&p, &rd, &td, &rm, &tm); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"provider": p, "requests": map[string]int64{"today": rd, "month": rm}, "tokens": map[string]int64{"today": td, "month": tm}})
	}
	return out, rows.Err()
}

// ConnectionUsage is what one connection has used: requests and tokens today, over the last 7 days and this month
// (calendar, server local time), and when it was last used.
type ConnectionUsage struct {
	ConnectionID string
	Provider     string
	Requests     [3]int64 // today, last 7 days, this month
	Tokens       [3]int64
	LastUsed     string
}

func (s *Store) ConnectionUsage(now time.Time) ([]ConnectionUsage, error) {
	day, _ := windowBounds("day", "calendar", now)
	month, _ := windowBounds("month", "calendar", now)
	week := now.AddDate(0, 0, -7)
	f := func(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
	since := month
	if week.Before(since) {
		since = week
	}
	rows, err := s.DB.Query(`SELECT COALESCE(connectionId,''), COALESCE(provider,''),
		SUM(timestamp>=?), SUM(CASE WHEN timestamp>=? THEN promptTokens+completionTokens ELSE 0 END),
		SUM(timestamp>=?), SUM(CASE WHEN timestamp>=? THEN promptTokens+completionTokens ELSE 0 END),
		SUM(timestamp>=?), SUM(CASE WHEN timestamp>=? THEN promptTokens+completionTokens ELSE 0 END),
		MAX(timestamp)
		FROM usageHistory WHERE timestamp>=? AND status<>'canceled' AND connectionId<>'' GROUP BY 1,2`,
		f(day), f(day), f(week), f(week), f(month), f(month), f(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConnectionUsage
	for rows.Next() {
		var u ConnectionUsage
		if err := rows.Scan(&u.ConnectionID, &u.Provider, &u.Requests[0], &u.Tokens[0], &u.Requests[1], &u.Tokens[1], &u.Requests[2], &u.Tokens[2], &u.LastUsed); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
