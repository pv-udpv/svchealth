package exporter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pv-udpv/svchealth/internal/checks"
	"github.com/pv-udpv/svchealth/internal/store"
)

// fakeSource implements the exporter's source interface.
type fakeSource struct {
	names map[string]string // name -> url
	order []string
}

func (f fakeSource) Endpoints() []string          { return f.order }
func (f fakeSource) TargetURL(name string) string { return f.names[name] }

func newTestExporter(t *testing.T) (*Exporter, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "e.db"), 60)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	now := time.Now()
	_ = st.Insert(checks.Result{Endpoint: "api", Status: checks.StatusGreen, HTTPStatus: 200, Latency: 42 * time.Millisecond, At: now})
	_ = st.Insert(checks.Result{Endpoint: "down", Status: checks.StatusRed, HTTPStatus: 500, Latency: 5 * time.Millisecond, At: now, Err: "HTTP 500"})

	src := fakeSource{
		names: map[string]string{"api": "https://api.example", "down": "https://down.example"},
		order: []string{"api", "down"},
	}
	return New("127.0.0.1:0", src, st, time.Hour), st
}

func TestMetricsEndpoint(t *testing.T) {
	e, _ := newTestExporter(t)
	rr := httptest.NewRecorder()
	e.handleMetrics(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	body := rr.Body.String()
	mustContain := []string{
		`svchealth_up{endpoint="api",status="UP"} 1`,
		`svchealth_up{endpoint="down",status="DOWN"} 0`,
		`svchealth_status{endpoint="api"} 1`,
		`svchealth_latency_ms{endpoint="api"} 42`,
		`svchealth_http_status{endpoint="down"} 500`,
		`# TYPE svchealth_uptime_ratio gauge`,
	}
	for _, want := range mustContain {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics missing line: %q\n---\n%s", want, body)
		}
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
}

func TestSnapshotEndpoint(t *testing.T) {
	e, _ := newTestExporter(t)
	rr := httptest.NewRecorder()
	e.handleSnapshot(rr, httptest.NewRequest(http.MethodGet, "/snapshot", nil))

	var got struct {
		Window    string `json:"window"`
		Endpoints []struct {
			Endpoint   string `json:"endpoint"`
			Status     string `json:"status"`
			HTTPStatus int    `json:"http_status"`
			LatencyMs  int64  `json:"latency_ms"`
		} `json:"endpoints"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("snapshot not valid JSON: %v\n%s", err, rr.Body.String())
	}
	if got.Window != "1h0m0s" {
		t.Errorf("window = %q, want 1h0m0s", got.Window)
	}
	if len(got.Endpoints) != 2 {
		t.Fatalf("got %d endpoints, want 2", len(got.Endpoints))
	}
	// Sorted by name: api first.
	if got.Endpoints[0].Endpoint != "api" || got.Endpoints[0].Status != "UP" {
		t.Errorf("endpoint[0] = %+v, want api/UP", got.Endpoints[0])
	}
	if got.Endpoints[1].HTTPStatus != 500 {
		t.Errorf("down http_status = %d, want 500", got.Endpoints[1].HTTPStatus)
	}
}

func TestStartShutdown(t *testing.T) {
	e, _ := newTestExporter(t)
	errc := e.Start()
	// Give the listener a moment, then shut down cleanly.
	time.Sleep(50 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := e.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := <-errc; err != nil {
		t.Errorf("serve error: %v", err)
	}
}
