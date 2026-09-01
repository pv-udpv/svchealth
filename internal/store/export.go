package store

import (
	"strings"
	"time"

	"github.com/pv-udpv/svchealth/internal/checks"
)

// Export returns samples for an endpoint (all endpoints when endpoint is "") in
// oldest-first chronological order. A non-zero since restricts to samples within
// the lookback window; zero means no lower bound.
func (s *Store) Export(endpoint string, since time.Duration) ([]Sample, error) {
	q := "SELECT endpoint,status,http_status,latency_ms,at,err FROM samples"
	var where []string
	var args []any
	if endpoint != "" {
		where = append(where, "endpoint=?")
		args = append(args, endpoint)
	}
	if since > 0 {
		where = append(where, "at>=?")
		args = append(args, time.Now().Add(-since).UnixNano())
	}
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY at ASC"

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Sample
	for rows.Next() {
		var sm Sample
		var st, atNanos int64
		if err := rows.Scan(&sm.Endpoint, &st, &sm.HTTPStatus, &sm.LatencyMs, &atNanos, &sm.Err); err != nil {
			return nil, err
		}
		sm.Status = checks.Status(st)
		sm.At = time.Unix(0, atNanos)
		out = append(out, sm)
	}
	return out, rows.Err()
}
