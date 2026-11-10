package db

import (
	"database/sql"
	"time"

	"router-go/internal/provider"
)

// Periods understood by Summary (same set as XRouter-Next's usage page).
var Periods = map[string]bool{"month": true, "today": true, "24h": true, "7d": true, "30d": true, "60d": true, "all": true}

// periodStart is the inclusive start of a period in server-local time; zero means "from the beginning".
func periodStart(period string, now time.Time) time.Time {
	y, m, d := now.Date()
	switch period {
	case "month":
		return time.Date(y, m, 1, 0, 0, 0, 0, now.Location())
	case "today":
		return time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	case "24h":
		return now.Add(-24 * time.Hour)
	case "7d":
		return now.AddDate(0, 0, -7)
	case "30d":
		return now.AddDate(0, 0, -30)
	case "60d":
		return now.AddDate(0, 0, -60)
	}
	return time.Time{}
}

// BreakRow is usage grouped by (model, provider node, connection, api key, endpoint); the UI regroups it by any of them.
type BreakRow struct {
	Model            string `json:"model"`
	Provider         string `json:"provider"` // node id
	ProviderName     string `json:"providerName"`
	ConnectionID     string `json:"connectionId"`
	Account          string `json:"account"` // connection name
	APIKey           string `json:"apiKey"`  // masked
	KeyName          string `json:"keyName"`
	Endpoint         string `json:"endpoint"`
	Requests         int    `json:"requests"`
	PromptTokens     int    `json:"promptTokens"`
	CompletionTokens int    `json:"completionTokens"`
	CacheTokens      int    `json:"cacheTokens"`
	LastUsed         string `json:"lastUsed"`
}

type Live struct {
	Requests     int     `json:"requests"`
	ModelsActive int     `json:"modelsActive"`
	RPM          float64 `json:"rpm"`
	AvgLatencyMs int     `json:"avgLatencyMs"`
}

type Summary struct {
	Period           string     `json:"period"`
	Requests         int        `json:"requests"`
	Errors           int        `json:"errors"`
	PromptTokens     int        `json:"promptTokens"`
	CompletionTokens int        `json:"completionTokens"`
	CacheTokens      int        `json:"cacheTokens"`
	Bucket           string     `json:"bucket"` // "hour" | "day"
	Series           []Bucket   `json:"series"`
	Breakdown        []BreakRow `json:"breakdown"`
	Live             Live       `json:"live"` // last 5 minutes, independent of the period
	Recent           []UsageRow `json:"recent"`
	// ProviderActivity covers the last 24 hours whatever the period: what each provider served and when.
	ProviderActivity []ProviderAct `json:"providerActivity"`
}

type ModelAct struct {
	Model    string `json:"model"`
	Requests int    `json:"requests"`
	LastUsed string `json:"lastUsed"`
}

type ProviderAct struct {
	Provider string     `json:"provider"` // node id
	LastUsed string     `json:"lastUsed"`
	Models   []ModelAct `json:"models"` // most recently used first
}

func (s *Store) providerActivity(now time.Time) ([]ProviderAct, error) {
	rows, err := s.DB.Query(`SELECT COALESCE(provider,''), COALESCE(model,''), COUNT(*), MAX(timestamp) FROM usageHistory
		WHERE timestamp>=? AND provider<>'' GROUP BY 1,2 ORDER BY MAX(timestamp) DESC`, now.Add(-24*time.Hour).UTC().Format("2006-01-02T15:04:05.000Z"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out, idx := []ProviderAct{}, map[string]int{}
	for rows.Next() {
		var p string
		var m ModelAct
		if err := rows.Scan(&p, &m.Model, &m.Requests, &m.LastUsed); err != nil {
			return nil, err
		}
		i, ok := idx[p]
		if !ok {
			i = len(out)
			idx[p] = i
			out = append(out, ProviderAct{Provider: p, LastUsed: m.LastUsed, Models: []ModelAct{}})
		}
		out[i].Models = append(out[i].Models, m)
	}
	return out, rows.Err()
}

func (s *Store) names(q string) map[string]string {
	m := map[string]string{}
	rows, err := s.DB.Query(q)
	if err != nil {
		return m
	}
	defer rows.Close()
	for rows.Next() {
		var k, v sql.NullString
		if rows.Scan(&k, &v) == nil {
			m[k.String] = v.String
		}
	}
	return m
}

// Summary powers the Usage page: totals, series, per-dimension breakdown, live 5-minute pills and recent requests.
func (s *Store) Summary(period string) (*Summary, error) {
	now := time.Now()
	since := periodStart(period, now)
	ts := ""
	if !since.IsZero() {
		ts = since.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	sum := &Summary{Period: period, Bucket: "day", Series: []Bucket{}, Breakdown: []BreakRow{}}
	if period == "today" || period == "24h" {
		sum.Bucket = "hour"
	}
	err := s.DB.QueryRow(`SELECT COUNT(*), COALESCE(SUM(status='error'),0), COALESCE(SUM(promptTokens),0), COALESCE(SUM(completionTokens),0), COALESCE(SUM(`+cacheExpr+`),0)
		FROM usageHistory WHERE timestamp>=?`, ts).Scan(&sum.Requests, &sum.Errors, &sum.PromptTokens, &sum.CompletionTokens, &sum.CacheTokens)
	if err != nil {
		return nil, err
	}
	if sum.Series, err = s.series(since, sum.Bucket == "day"); err != nil {
		return nil, err
	}

	nodeNames := s.names(`SELECT id,name FROM providerNodes`)
	for _, p := range provider.All() { // built-ins are not stored in providerNodes
		if _, ok := nodeNames[p.ID]; !ok {
			nodeNames[p.ID] = p.Name
		}
	}
	connNames := s.names(`SELECT id,name FROM providerConnections`)
	keyNames := map[string]string{}
	if keys, err := s.APIKeys(); err == nil {
		for _, k := range keys {
			keyNames[MaskKey(k.Key)] = k.Name
		}
	}
	rows, err := s.DB.Query(`SELECT COALESCE(model,''), COALESCE(provider,''), COALESCE(connectionId,''), COALESCE(apiKey,''), COALESCE(endpoint,''),
		COUNT(*), COALESCE(SUM(promptTokens),0), COALESCE(SUM(completionTokens),0), COALESCE(SUM(`+cacheExpr+`),0), MAX(timestamp)
		FROM usageHistory WHERE timestamp>=? GROUP BY 1,2,3,4,5 ORDER BY COUNT(*) DESC`, ts)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var r BreakRow
		if err := rows.Scan(&r.Model, &r.Provider, &r.ConnectionID, &r.APIKey, &r.Endpoint, &r.Requests, &r.PromptTokens, &r.CompletionTokens, &r.CacheTokens, &r.LastUsed); err != nil {
			rows.Close()
			return nil, err
		}
		r.ProviderName, r.Account, r.KeyName = nodeNames[r.Provider], connNames[r.ConnectionID], keyNames[r.APIKey]
		if r.ProviderName == "" {
			r.ProviderName = r.Provider
		}
		if r.KeyName == "" {
			r.KeyName = r.APIKey
		}
		sum.Breakdown = append(sum.Breakdown, r)
	}
	rows.Close()

	five := now.Add(-5 * time.Minute).UTC().Format("2006-01-02T15:04:05.000Z")
	var avg sql.NullFloat64
	if err := s.DB.QueryRow(`SELECT COUNT(*), COUNT(DISTINCT model), AVG(latencyMs) FROM usageHistory WHERE timestamp>=?`, five).
		Scan(&sum.Live.Requests, &sum.Live.ModelsActive, &avg); err != nil {
		return nil, err
	}
	sum.Live.AvgLatencyMs = int(avg.Float64)
	sum.Live.RPM = float64(sum.Live.Requests) / 5
	if sum.Recent, _, err = s.History(30, 0, "", "", ""); err != nil {
		return nil, err
	}
	sum.ProviderActivity, err = s.providerActivity(now)
	return sum, err
}

// ResetUsage deletes all request history and daily totals; it returns the number of history rows removed.
func (s *Store) ResetUsage() (int64, error) {
	var n int64
	err := s.withTx(func(tx *sql.Tx) error {
		r, err := tx.Exec(`DELETE FROM usageHistory`)
		if err != nil {
			return err
		}
		n, _ = r.RowsAffected()
		_, err = tx.Exec(`DELETE FROM usageDaily`)
		return err
	})
	return n, err
}

// RouteStat is a provider's recent traffic: requests (cancelled ones excluded), how many succeeded, mean latency of successes.
type RouteStat struct {
	Requests     int
	OK           int
	AvgLatencyMs float64
}

func (s *Store) RouteStats(since time.Time) (map[string]RouteStat, error) {
	rows, err := s.DB.Query(`SELECT COALESCE(provider,''), COUNT(*), COALESCE(SUM(status='ok'),0), COALESCE(AVG(CASE WHEN status='ok' THEN latencyMs END),0)
		FROM usageHistory WHERE timestamp>=? AND status<>'canceled' GROUP BY 1`, since.UTC().Format("2006-01-02T15:04:05.000Z"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]RouteStat{}
	for rows.Next() {
		var p string
		var r RouteStat
		if err := rows.Scan(&p, &r.Requests, &r.OK, &r.AvgLatencyMs); err != nil {
			return nil, err
		}
		out[p] = r
	}
	return out, rows.Err()
}
