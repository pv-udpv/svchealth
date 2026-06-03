package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pv-udpv/svchealth/internal/checks"
)

// Supabase is an optional secondary writer that batches check samples to a
// Supabase table via the PostgREST REST API. It is fully decoupled from the
// SQLite store: the TUI keeps working if Supabase is unconfigured or down.
//
// Configuration (env, picked up by FromEnv):
//
//	SVCHEALTH_SUPABASE_URL   e.g. https://<ref>.supabase.co
//	SVCHEALTH_SUPABASE_KEY   anon or service key
//	SVCHEALTH_SUPABASE_TABLE optional, defaults to "svchealth_samples"
type Supabase struct {
	endpoint string // full PostgREST URL: <url>/rest/v1/<table>
	apiKey   string
	client   *http.Client

	mu    sync.Mutex
	buf   []supaRow
	flush int // flush when buffer reaches this size
}

type supaRow struct {
	Endpoint   string `json:"endpoint"`
	Status     int    `json:"status"`
	StatusText string `json:"status_text"`
	HTTPStatus int    `json:"http_status"`
	LatencyMs  int64  `json:"latency_ms"`
	CheckedAt  string `json:"checked_at"` // RFC3339
	Err        string `json:"err,omitempty"`
}

// FromEnv builds a Supabase writer from environment variables, or returns
// (nil, nil) if SVCHEALTH_SUPABASE_URL / _KEY are not both set.
func FromEnv() (*Supabase, error) {
	url := strings.TrimRight(os.Getenv("SVCHEALTH_SUPABASE_URL"), "/")
	key := os.Getenv("SVCHEALTH_SUPABASE_KEY")
	if url == "" || key == "" {
		return nil, nil // not configured -> disabled, not an error
	}
	table := os.Getenv("SVCHEALTH_SUPABASE_TABLE")
	if table == "" {
		table = "svchealth_samples"
	}
	return &Supabase{
		endpoint: fmt.Sprintf("%s/rest/v1/%s", url, table),
		apiKey:   key,
		client:   &http.Client{Timeout: 10 * time.Second},
		flush:    20,
	}, nil
}

// Enqueue buffers a result; flushes automatically when the batch is full.
func (s *Supabase) Enqueue(ctx context.Context, r checks.Result) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.buf = append(s.buf, supaRow{
		Endpoint:   r.Endpoint,
		Status:     int(r.Status),
		StatusText: r.Status.String(),
		HTTPStatus: r.HTTPStatus,
		LatencyMs:  r.Latency.Milliseconds(),
		CheckedAt:  r.At.UTC().Format(time.RFC3339Nano),
		Err:        r.Err,
	})
	full := len(s.buf) >= s.flush
	s.mu.Unlock()
	if full {
		_ = s.Flush(ctx)
	}
}

// Flush writes all buffered rows in a single bulk insert. Buffer is preserved
// on failure so the next flush retries (bounded by buffer growth).
func (s *Supabase) Flush(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if len(s.buf) == 0 {
		s.mu.Unlock()
		return nil
	}
	batch := s.buf
	s.buf = nil
	s.mu.Unlock()

	body, err := json.Marshal(batch)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		s.requeue(batch)
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", s.apiKey)
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Prefer", "return=minimal")

	resp, err := s.client.Do(req)
	if err != nil {
		s.requeue(batch)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.requeue(batch)
		return fmt.Errorf("supabase insert: status %d", resp.StatusCode)
	}
	return nil
}

// requeue prepends a failed batch back to the buffer, capped to avoid unbounded
// growth during prolonged outages.
func (s *Supabase) requeue(batch []supaRow) {
	const cap = 500
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf = append(batch, s.buf...)
	if len(s.buf) > cap {
		s.buf = s.buf[len(s.buf)-cap:]
	}
}
