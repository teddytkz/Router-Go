package db

import (
	"database/sql"
	"fmt"
	"time"
)

type Usage struct {
	Timestamp, Provider, Model, ConnectionID, APIKey, Endpoint, Status string
	PromptTokens, CompletionTokens, CacheTokens, LatencyMs             int
}

func (s *Store) AddUsage(u Usage) error {
	if u.Timestamp == "" {
		u.Timestamp = now()
	}
	// `tokens` is the XRouter-Next JSON breakdown column; cached_tokens is the cache-read/creation share of prompt tokens.
	tokens := fmt.Sprintf(`{"prompt_tokens":%d,"completion_tokens":%d,"cached_tokens":%d}`, u.PromptTokens, u.CompletionTokens, u.CacheTokens)
	_, err := s.DB.Exec(`INSERT INTO usageHistory(timestamp,provider,model,connectionId,apiKey,endpoint,promptTokens,completionTokens,latencyMs,status,tokens)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`, u.Timestamp, u.Provider, u.Model, u.ConnectionID, u.APIKey, u.Endpoint, u.PromptTokens, u.CompletionTokens, u.LatencyMs, u.Status, tokens)
	return err
}

type UsageRow struct {
	ID               int64  `json:"id"`
	Timestamp        string `json:"timestamp"`
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	ConnectionID     string `json:"connectionId"`
	Endpoint         string `json:"endpoint"`
	PromptTokens     int    `json:"promptTokens"`
	CompletionTokens int    `json:"completionTokens"`
	LatencyMs        int    `json:"latencyMs"`
	Status           string `json:"status"`
}

// History returns recent usage rows, newest first. Empty filters are ignored.
func (s *Store) History(limit, offset int, provider, model, status string) ([]UsageRow, int, error) {
	where, args := ` WHERE 1=1`, []any{}
	for col, v := range map[string]string{"provider": provider, "model": model, "status": status} {
		if v != "" {
			where += ` AND ` + col + `=?`
			args = append(args, v)
		}
	}
	var total int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM usageHistory`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.DB.Query(`SELECT id,timestamp,COALESCE(provider,''),COALESCE(model,''),COALESCE(connectionId,''),COALESCE(endpoint,''),
		COALESCE(promptTokens,0),COALESCE(completionTokens,0),COALESCE(latencyMs,0),COALESCE(status,'') FROM usageHistory`+where+
		` ORDER BY timestamp DESC, id DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []UsageRow{}
	for rows.Next() {
		var r UsageRow
		if err := rows.Scan(&r.ID, &r.Timestamp, &r.Provider, &r.Model, &r.ConnectionID, &r.Endpoint, &r.PromptTokens, &r.CompletionTokens, &r.LatencyMs, &r.Status); err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// Bucket is one point of the usage time series (an hour, or a day for long ranges).
type Bucket struct {
	T                string `json:"t"`
	Requests         int    `json:"requests"`
	Errors           int    `json:"errors"`
	PromptTokens     int    `json:"promptTokens"`
	CompletionTokens int    `json:"completionTokens"`
	CacheTokens      int    `json:"cacheTokens"`
}

type Stats struct {
	Requests         int          `json:"requests"`
	Errors           int          `json:"errors"`
	PromptTokens     int          `json:"promptTokens"`
	CompletionTokens int          `json:"completionTokens"`
	CacheTokens      int          `json:"cacheTokens"`
	AvgLatencyMs     int          `json:"avgLatencyMs"`
	Bucket           string       `json:"bucket"` // "hour" | "day"
	Series           []Bucket     `json:"series"`
	TopModels        []ModelCount `json:"topModels"`
}

type ModelCount struct {
	Model    string `json:"model"`
	Requests int    `json:"requests"`
	Tokens   int    `json:"tokens"`
}

const cacheExpr = `COALESCE(json_extract(tokens,'$.cached_tokens'),0)`

// Stats aggregates one period (see Periods): hourly buckets for today and 24h, daily buckets beyond. status='error' counts as an error.
func (s *Store) Stats(period string) (*Stats, error) {
	now := time.Now()
	since := periodStart(period, now)
	ts := since.UTC().Format("2006-01-02T15:04:05.000Z")
	st := &Stats{Series: []Bucket{}, TopModels: []ModelCount{}, Bucket: "day"}
	if since.IsZero() { // "all": start at the first request
		ts = ""
	}
	if period == "today" || period == "24h" {
		st.Bucket = "hour"
	}
	var avg sql.NullFloat64
	err := s.DB.QueryRow(`SELECT COUNT(*), COALESCE(SUM(status='error'),0), COALESCE(SUM(promptTokens),0), COALESCE(SUM(completionTokens),0), COALESCE(SUM(`+cacheExpr+`),0), AVG(latencyMs)
		FROM usageHistory WHERE timestamp>=?`, ts).Scan(&st.Requests, &st.Errors, &st.PromptTokens, &st.CompletionTokens, &st.CacheTokens, &avg)
	if err != nil {
		return nil, err
	}
	st.AvgLatencyMs = int(avg.Float64)

	series, err := s.series(since, st.Bucket == "day")
	if err != nil {
		return nil, err
	}
	st.Series = series

	mr, err := s.DB.Query(`SELECT COALESCE(model,''), COUNT(*), COALESCE(SUM(promptTokens+completionTokens),0) FROM usageHistory
		WHERE timestamp>=? GROUP BY model ORDER BY COUNT(*) DESC LIMIT 8`, ts)
	if err != nil {
		return nil, err
	}
	defer mr.Close()
	for mr.Next() {
		var m ModelCount
		if err := mr.Scan(&m.Model, &m.Requests, &m.Tokens); err != nil {
			return nil, err
		}
		st.TopModels = append(st.TopModels, m)
	}
	return st, mr.Err()
}

// series is the dense per-hour (or per-day) usage series since `since`; empty buckets are zero so charts have no gaps.
// A zero `since` starts at the first recorded request.
func (s *Store) series(since time.Time, day bool) ([]Bucket, error) {
	step, keyLen, layout := time.Hour, 13, "2006-01-02T15"
	if day {
		step, keyLen, layout = 24*time.Hour, 10, "2006-01-02"
	}
	ts := ""
	if !since.IsZero() {
		ts = since.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	byKey := map[string]*Bucket{}
	rows, err := s.DB.Query(`SELECT substr(timestamp,1,?), COUNT(*), COALESCE(SUM(status='error'),0), COALESCE(SUM(promptTokens),0), COALESCE(SUM(completionTokens),0), COALESCE(SUM(`+cacheExpr+`),0)
		FROM usageHistory WHERE timestamp>=? GROUP BY 1`, keyLen, ts)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k string
		var b Bucket
		if err := rows.Scan(&k, &b.Requests, &b.Errors, &b.PromptTokens, &b.CompletionTokens, &b.CacheTokens); err != nil {
			rows.Close()
			return nil, err
		}
		byKey[k] = &b
	}
	rows.Close()
	if since.IsZero() { // "all": start at the first request
		var first sql.NullString
		_ = s.DB.QueryRow(`SELECT MIN(timestamp) FROM usageHistory`).Scan(&first)
		t, err := time.Parse(time.RFC3339Nano, first.String)
		if err != nil {
			return []Bucket{}, nil
		}
		since = t
	}
	out := []Bucket{}
	for t := since.UTC().Truncate(step); !t.After(time.Now().UTC()); t = t.Add(step) {
		b := Bucket{}
		if f, ok := byKey[t.Format(layout)]; ok {
			b = *f
		}
		b.T = t.Format("2006-01-02T15:04:05.000Z")
		out = append(out, b)
	}
	return out, nil
}
