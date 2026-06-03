// Package exporter exposes svchealth's own health data over HTTP so the tool is
// observable by external systems: a Prometheus text endpoint at /metrics and a
// JSON snapshot at /snapshot. It reads exclusively from the SQLite store, so it
// is fully decoupled from the TUI.
package exporter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/pv-udpv/svchealth/internal/checks"
	"github.com/pv-udpv/svchealth/internal/store"
)

// source is the minimal surface the exporter needs from the engine.
type source interface {
	Endpoints() []string
	TargetURL(name string) string
}

// Exporter serves Prometheus + JSON views of current endpoint health.
type Exporter struct {
	src    source
	store  *store.Store
	window time.Duration
	srv    *http.Server
}

// New builds an Exporter bound to addr. window is the uptime window reported.
func New(addr string, src source, st *store.Store, window time.Duration) *Exporter {
	if window <= 0 {
		window = time.Hour
	}
	e := &Exporter{src: src, store: st, window: window}
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", e.handleMetrics)
	mux.HandleFunc("/snapshot", e.handleSnapshot)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	e.srv = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return e
}

// Start runs the HTTP server in a goroutine. It returns immediately; serve
// errors (other than clean shutdown) are sent to the returned channel.
func (e *Exporter) Start() <-chan error {
	errc := make(chan error, 1)
	go func() {
		err := e.srv.ListenAndServe()
		if err != nil && err != http.ErrServerClosed {
			errc <- err
		}
		close(errc)
	}()
	return errc
}

// Shutdown gracefully stops the server.
func (e *Exporter) Shutdown(ctx context.Context) error {
	return e.srv.Shutdown(ctx)
}

// endpointView is the per-endpoint JSON/Prometheus record.
type endpointView struct {
	Endpoint   string  `json:"endpoint"`
	URL        string  `json:"url"`
	Status     string  `json:"status"`      // UP / DEGRADED / DOWN / UNKNOWN
	StatusCode int     `json:"status_code"` // numeric status (0..3)
	HTTPStatus int     `json:"http_status"`
	LatencyMs  int64   `json:"latency_ms"`
	UptimePct  float64 `json:"uptime_pct"`
	Samples    int     `json:"samples"`
	LastCheck  string  `json:"last_check,omitempty"`
	Err        string  `json:"err,omitempty"`
}

func (e *Exporter) collect() []endpointView {
	names := e.src.Endpoints()
	out := make([]endpointView, 0, len(names))
	for _, name := range names {
		v := endpointView{Endpoint: name, URL: e.src.TargetURL(name), Status: "UNKNOWN"}
		if sm, ok, err := e.store.Latest(name); err == nil && ok {
			v.Status = sm.Status.String()
			v.StatusCode = int(sm.Status)
			v.HTTPStatus = sm.HTTPStatus
			v.LatencyMs = sm.LatencyMs
			v.LastCheck = sm.At.UTC().Format(time.RFC3339)
			v.Err = sm.Err
		}
		if up, n, err := e.store.Uptime(name, e.window); err == nil {
			v.UptimePct = up * 100
			v.Samples = n
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Endpoint < out[j].Endpoint })
	return out
}

func (e *Exporter) handleSnapshot(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(map[string]any{
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"window":       e.window.String(),
		"endpoints":    e.collect(),
	})
}

func (e *Exporter) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	views := e.collect()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	var b strings.Builder

	b.WriteString("# HELP svchealth_up Endpoint health: 1 if UP or DEGRADED, 0 if DOWN/UNKNOWN.\n")
	b.WriteString("# TYPE svchealth_up gauge\n")
	for _, v := range views {
		up := 0
		if v.StatusCode == int(checks.StatusGreen) || v.StatusCode == int(checks.StatusYellow) {
			up = 1
		}
		fmt.Fprintf(&b, "svchealth_up{endpoint=%q,status=%q} %d\n", v.Endpoint, v.Status, up)
	}

	b.WriteString("# HELP svchealth_status Endpoint status code (1=UP,2=DEGRADED,3=DOWN,0=UNKNOWN).\n")
	b.WriteString("# TYPE svchealth_status gauge\n")
	for _, v := range views {
		fmt.Fprintf(&b, "svchealth_status{endpoint=%q} %d\n", v.Endpoint, v.StatusCode)
	}

	b.WriteString("# HELP svchealth_latency_ms Latency of the most recent check in milliseconds.\n")
	b.WriteString("# TYPE svchealth_latency_ms gauge\n")
	for _, v := range views {
		fmt.Fprintf(&b, "svchealth_latency_ms{endpoint=%q} %d\n", v.Endpoint, v.LatencyMs)
	}

	b.WriteString("# HELP svchealth_http_status HTTP status code of the most recent check.\n")
	b.WriteString("# TYPE svchealth_http_status gauge\n")
	for _, v := range views {
		fmt.Fprintf(&b, "svchealth_http_status{endpoint=%q} %d\n", v.Endpoint, v.HTTPStatus)
	}

	b.WriteString("# HELP svchealth_uptime_ratio Healthy-sample ratio over the configured window (0..1).\n")
	b.WriteString("# TYPE svchealth_uptime_ratio gauge\n")
	for _, v := range views {
		fmt.Fprintf(&b, "svchealth_uptime_ratio{endpoint=%q} %.4f\n", v.Endpoint, v.UptimePct/100)
	}

	_, _ = w.Write([]byte(b.String()))
}
