package store

import (
	"sort"
	"time"

	"github.com/pv-udpv/svchealth/internal/checks"
)

// Stats summarizes recent behavior for an endpoint, used by the detail view.
type Stats struct {
	Endpoint string
	Samples  int
	// Latency percentiles in milliseconds over the stat window (successful or
	// not — reflects observed response time).
	P50, P90, P99 int64
	MinMs, MaxMs  int64
	// Counts by status over the window.
	Green, Yellow, Red int
	// RecentErrors holds the most recent non-empty error strings (newest first).
	RecentErrors []ErrEvent
}

// ErrEvent is a single recent error occurrence.
type ErrEvent struct {
	At  time.Time
	Err string
}

// StatsFor computes detail-view statistics for an endpoint over window.
func (s *Store) StatsFor(endpoint string, window time.Duration, maxErrors int) (Stats, error) {
	since := time.Now().Add(-window).UnixNano()
	rows, err := s.db.Query(
		`SELECT status, latency_ms, at, err FROM samples
		 WHERE endpoint=? AND at>=? ORDER BY at DESC`,
		endpoint, since,
	)
	if err != nil {
		return Stats{}, err
	}
	defer rows.Close()

	st := Stats{Endpoint: endpoint}
	var lats []int64
	for rows.Next() {
		var status, latency, atNanos int64
		var errStr string
		if err := rows.Scan(&status, &latency, &atNanos, &errStr); err != nil {
			return Stats{}, err
		}
		st.Samples++
		switch checks.Status(status) {
		case checks.StatusGreen:
			st.Green++
		case checks.StatusYellow:
			st.Yellow++
		case checks.StatusRed:
			st.Red++
		}
		if latency > 0 {
			lats = append(lats, latency)
		}
		if errStr != "" && len(st.RecentErrors) < maxErrors {
			st.RecentErrors = append(st.RecentErrors, ErrEvent{At: time.Unix(0, atNanos), Err: errStr})
		}
	}
	if err := rows.Err(); err != nil {
		return Stats{}, err
	}

	if len(lats) > 0 {
		sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })
		st.MinMs = lats[0]
		st.MaxMs = lats[len(lats)-1]
		st.P50 = percentile(lats, 0.50)
		st.P90 = percentile(lats, 0.90)
		st.P99 = percentile(lats, 0.99)
	}
	return st, nil
}

// WindowAgg is a compact uptime + latency summary over a single time window,
// used for the 1h / 24h rows in the detail pane.
type WindowAgg struct {
	Label   string  // e.g. "1h", "24h"
	Samples int     // number of samples in the window
	Uptime  float64 // healthy fraction 0..1
	P50     int64   // median latency ms
	P99     int64   // p99 latency ms
}

// WindowAggs returns uptime + latency aggregates for each requested window.
// Each window is labelled and computed independently from the samples table.
func (s *Store) WindowAggs(endpoint string, windows []struct {
	Label string
	Dur   time.Duration
}) ([]WindowAgg, error) {
	out := make([]WindowAgg, 0, len(windows))
	for _, w := range windows {
		since := time.Now().Add(-w.Dur).UnixNano()
		rows, err := s.db.Query(
			`SELECT status, latency_ms FROM samples WHERE endpoint=? AND at>=?`,
			endpoint, since,
		)
		if err != nil {
			return nil, err
		}
		agg := WindowAgg{Label: w.Label}
		var healthy int
		var lats []int64
		for rows.Next() {
			var status, latency int64
			if err := rows.Scan(&status, &latency); err != nil {
				rows.Close()
				return nil, err
			}
			agg.Samples++
			if checks.Status(status) == checks.StatusGreen || checks.Status(status) == checks.StatusYellow {
				healthy++
			}
			if latency > 0 {
				lats = append(lats, latency)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
		if agg.Samples > 0 {
			agg.Uptime = float64(healthy) / float64(agg.Samples)
		}
		if len(lats) > 0 {
			sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })
			agg.P50 = percentile(lats, 0.50)
			agg.P99 = percentile(lats, 0.99)
		}
		out = append(out, agg)
	}
	return out, nil
}

// LatencyHistory returns up to `limit` most-recent latency values (ms) for an
// endpoint, oldest-first, suitable for a time-series mini-graph. Samples with
// no measured latency (e.g. immediate connection failures) are recorded as 0
// so gaps remain visible in the graph.
func (s *Store) LatencyHistory(endpoint string, limit int) ([]int64, error) {
	if limit <= 0 {
		limit = 60
	}
	rows, err := s.db.Query(
		`SELECT latency_ms FROM samples WHERE endpoint=? ORDER BY at DESC LIMIT ?`,
		endpoint, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var lat int64
		if err := rows.Scan(&lat); err != nil {
			return nil, err
		}
		out = append(out, lat)
	}
	// reverse to oldest-first
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

// percentile returns the p-th percentile (0..1) of a sorted slice using the
// nearest-rank method.
func percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[len(sorted)-1]
	}
	rank := int(p*float64(len(sorted)-1) + 0.5)
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}
