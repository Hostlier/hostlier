package model

import (
	"net/url"
	"strconv"
	"strings"
	"time"
)

const HistoryRetention = 30 * 24 * time.Hour

// HistoryFilter defines an inclusive start, exclusive end and literal text search.
// BeforeTime and Before form a stable cursor even when timestamps are identical.
type HistoryFilter struct {
	From, To, BeforeTime time.Time
	Search, Level        string
	Before               int64
	Limit                int
}

func ParseHistoryFilter(q url.Values) (HistoryFilter, error) {
	f := HistoryFilter{Search: q.Get("q"), Level: strings.ToLower(q.Get("level")), Limit: 100}
	for _, key := range []string{"from", "to", "q", "level", "before", "beforeTime", "limit", "app", "component", "revision", "operationId"} {
		if len(q[key]) > 1 {
			return f, Err("INVALID_REQUEST", "Supply each history filter once")
		}
	}
	for key, target := range map[string]*time.Time{"from": &f.From, "to": &f.To, "beforeTime": &f.BeforeTime} {
		if value := q.Get(key); value != "" {
			t, err := time.Parse(time.RFC3339Nano, value)
			if err != nil || t.Year() < 1970 || t.Year() > 2261 {
				return f, Err("INVALID_REQUEST", "Use an ISO timestamp with timezone")
			}
			*target = t.UTC()
		}
	}
	if !f.From.IsZero() && !f.To.IsZero() && (!f.From.Before(f.To) || f.To.Sub(f.From) > HistoryRetention) {
		return f, Err("INVALID_REQUEST", "Choose a time range of up to 30 days")
	}
	if len(f.Search) > 256 {
		return f, Err("INVALID_REQUEST", "Search must be at most 256 bytes")
	}
	if f.Level != "" && f.Level != "debug" && f.Level != "info" && f.Level != "warn" && f.Level != "error" && f.Level != "fatal" {
		return f, Err("INVALID_REQUEST", "Unknown log level")
	}
	for key, target := range map[string]*int64{"before": &f.Before} {
		if value := q.Get(key); value != "" {
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil || n < 0 {
				return f, Err("INVALID_REQUEST", "Invalid history cursor")
			}
			*target = n
		}
	}
	if value := q.Get("limit"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 200 {
			return f, Err("INVALID_REQUEST", "History limit must be 1–200")
		}
		f.Limit = n
	}
	if !f.BeforeTime.IsZero() && f.Before == 0 {
		return f, Err("INVALID_REQUEST", "Timestamp cursor requires a record cursor")
	}
	return f, nil
}
