// Package store persists short-term health-check history in SQLite and exposes
// rolling uptime + sparkline helpers.
package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo)

	"github.com/pv-udpv/svchealth/internal/checks"
)

// Store wraps a SQLite database of check samples.
type Store struct {
	db          *sql.DB
	historySize int
}

// Sample is a persisted check outcome.
type Sample struct {
	Endpoint   string
	Status     checks.Status
	HTTPStatus int
	LatencyMs  int64
	At         time.Time
	Err        string
}

// Open initializes (and migrates) the SQLite store at path.
func Open(path string, historySize int) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}
	db.SetMaxOpenConns(1) // SQLite single-writer; avoid lock contention
	s := &Store{db: db, historySize: historySize}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS samples (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	endpoint    TEXT    NOT NULL,
	status      INTEGER NOT NULL,
	http_status INTEGER NOT NULL DEFAULT 0,
	latency_ms  INTEGER NOT NULL DEFAULT 0,
	at          INTEGER NOT NULL, -- unix nanos
	err         TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_samples_ep_at ON samples(endpoint, at);
PRAGMA journal_mode=WAL;
`
	_, err := s.db.Exec(ddl)
	return err
}

// Insert records a check result.
func (s *Store) Insert(r checks.Result) error {
	_, err := s.db.Exec(
		`INSERT INTO samples(endpoint,status,http_status,latency_ms,at,err) VALUES(?,?,?,?,?,?)`,
		r.Endpoint, int(r.Status), r.HTTPStatus, r.Latency.Milliseconds(), r.At.UnixNano(), r.Err,
	)
	return err
}

// Recent returns up to historySize most-recent samples for an endpoint,
// oldest-first (suitable for sparkline rendering).
func (s *Store) Recent(endpoint string) ([]Sample, error) {
	rows, err := s.db.Query(
		`SELECT endpoint,status,http_status,latency_ms,at,err
		 FROM samples WHERE endpoint=? ORDER BY at DESC LIMIT ?`,
		endpoint, s.historySize,
	)
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
	// reverse to oldest-first
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

// Uptime returns the fraction (0..1) of healthy samples within the window.
func (s *Store) Uptime(endpoint string, window time.Duration) (float64, int, error) {
	since := time.Now().Add(-window).UnixNano()
	var total, healthy int
	err := s.db.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(CASE WHEN status IN (?,?) THEN 1 ELSE 0 END),0)
		 FROM samples WHERE endpoint=? AND at>=?`,
		int(checks.StatusGreen), int(checks.StatusYellow), endpoint, since,
	).Scan(&total, &healthy)
	if err != nil {
		return 0, 0, err
	}
	if total == 0 {
		return 0, 0, nil
	}
	return float64(healthy) / float64(total), total, nil
}

// Latest returns the most recent sample for an endpoint, ok=false if none.
func (s *Store) Latest(endpoint string) (Sample, bool, error) {
	var sm Sample
	var st, atNanos int64
	err := s.db.QueryRow(
		`SELECT endpoint,status,http_status,latency_ms,at,err
		 FROM samples WHERE endpoint=? ORDER BY at DESC LIMIT 1`,
		endpoint,
	).Scan(&sm.Endpoint, &st, &sm.HTTPStatus, &sm.LatencyMs, &atNanos, &sm.Err)
	if err == sql.ErrNoRows {
		return Sample{}, false, nil
	}
	if err != nil {
		return Sample{}, false, err
	}
	sm.Status = checks.Status(st)
	sm.At = time.Unix(0, atNanos)
	return sm, true, nil
}

// Prune deletes samples older than the retention window to keep the DB small.
func (s *Store) Prune(retention time.Duration) error {
	cutoff := time.Now().Add(-retention).UnixNano()
	_, err := s.db.Exec(`DELETE FROM samples WHERE at < ?`, cutoff)
	return err
}
