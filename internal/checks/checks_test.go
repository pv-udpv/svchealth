package checks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pv-udpv/svchealth/internal/config"
)

func TestClassify(t *testing.T) {
	c := &Checker{
		ep:         config.Endpoint{ExpectStatus: 0},
		degradedMs: 1000,
		criticalMs: 5000,
	}
	cases := []struct {
		name    string
		code    int
		latency time.Duration
		want    Status
	}{
		{"fast 200", 200, 100 * time.Millisecond, StatusGreen},
		{"slow 200 -> degraded", 200, 2 * time.Second, StatusYellow},
		{"very slow 200 -> down", 200, 6 * time.Second, StatusRed},
		{"500 -> down", 500, 50 * time.Millisecond, StatusRed},
		{"404 -> down", 404, 50 * time.Millisecond, StatusRed},
		{"299 ok", 299, 10 * time.Millisecond, StatusGreen},
		{"300 redirect -> down", 300, 10 * time.Millisecond, StatusRed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.classify(tc.code, tc.latency); got != tc.want {
				t.Errorf("classify(%d, %v) = %v, want %v", tc.code, tc.latency, got, tc.want)
			}
		})
	}
}

func TestClassifyExpectStatus(t *testing.T) {
	c := &Checker{ep: config.Endpoint{ExpectStatus: 204}, degradedMs: 1000}
	if got := c.classify(204, 0); got != StatusGreen {
		t.Errorf("expected 204 -> green, got %v", got)
	}
	if got := c.classify(200, 0); got != StatusRed {
		t.Errorf("expected 200 (not 204) -> red, got %v", got)
	}
}

func TestClassifyThresholdsDisabled(t *testing.T) {
	c := &Checker{ep: config.Endpoint{}, degradedMs: 0, criticalMs: 0}
	if got := c.classify(200, time.Hour); got != StatusGreen {
		t.Errorf("with thresholds disabled, slow 2xx should stay green, got %v", got)
	}
}

func TestStatusString(t *testing.T) {
	cases := map[Status]string{
		StatusGreen:   "UP",
		StatusYellow:  "DEGRADED",
		StatusRed:     "DOWN",
		StatusUnknown: "UNKNOWN",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("Status(%d).String() = %q, want %q", s, got, want)
		}
	}
}

func TestResultHealthy(t *testing.T) {
	if !(Result{Status: StatusGreen}).Healthy() {
		t.Error("green should be healthy")
	}
	if !(Result{Status: StatusYellow}).Healthy() {
		t.Error("yellow should be healthy")
	}
	if (Result{Status: StatusRed}).Healthy() {
		t.Error("red should not be healthy")
	}
}

// TestCheckEndToEnd exercises the public Check path against a local server.
func TestCheckEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.WriteHeader(200)
		case "/fail":
			w.WriteHeader(503)
		}
	}))
	defer srv.Close()

	settings := config.Settings{DegradedLatencyMs: 1500, TimeoutSeconds: 5}

	ok := New(config.Endpoint{Name: "ok", URL: srv.URL + "/ok", TimeoutSeconds: 5}, settings)
	if res := ok.Check(context.Background()); res.Status != StatusGreen || res.HTTPStatus != 200 {
		t.Errorf("ok endpoint: status=%v http=%d err=%q", res.Status, res.HTTPStatus, res.Err)
	}

	bad := New(config.Endpoint{Name: "fail", URL: srv.URL + "/fail", TimeoutSeconds: 5}, settings)
	res := bad.Check(context.Background())
	if res.Status != StatusRed || res.HTTPStatus != 503 {
		t.Errorf("fail endpoint: status=%v http=%d", res.Status, res.HTTPStatus)
	}
	if res.Err == "" {
		t.Error("expected non-empty Err on 503")
	}
}

func TestCheckConnectionError(t *testing.T) {
	// Point at a closed port to force a transport error.
	c := New(config.Endpoint{Name: "dead", URL: "http://127.0.0.1:1/nope", TimeoutSeconds: 2}, config.Settings{})
	res := c.Check(context.Background())
	if res.Status != StatusRed {
		t.Errorf("connection error should be red, got %v", res.Status)
	}
	if res.Err == "" {
		t.Error("expected non-empty Err on connection failure")
	}
}
