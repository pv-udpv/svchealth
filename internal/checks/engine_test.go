package checks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pv-udpv/svchealth/internal/config"
	"github.com/pv-udpv/svchealth/internal/connectors"
)

// stubUptime is a fixed UptimeSource for threshold-alert tests.
type stubUptime struct {
	up float64
	n  int
}

func (s stubUptime) Uptime(string, time.Duration) (float64, int, error) { return s.up, s.n, nil }

// recordingNotifier implements both Notifier and UptimeAlerter, tallying calls.
type recordingNotifier struct {
	downs, recovers, uptimeAlerts, uptimeRecovers int
}

func (r *recordingNotifier) OnSustainedDown(context.Context, string, int, connectors.CheckSummary) error {
	r.downs++
	return nil
}
func (r *recordingNotifier) OnRecovered(context.Context, string) error { r.recovers++; return nil }
func (r *recordingNotifier) OnUptimeAlert(context.Context, string, float64, time.Duration, int) error {
	r.uptimeAlerts++
	return nil
}
func (r *recordingNotifier) OnUptimeRecovered(context.Context, string, float64) error {
	r.uptimeRecovers++
	return nil
}

func TestReloadAddsAndRemoves(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	cfgA := &config.Config{
		Settings: config.Settings{DegradedLatencyMs: 1500},
		Endpoints: []config.Endpoint{
			{Name: "a", URL: srv.URL},
			{Name: "b", URL: srv.URL},
		},
	}
	applyDefaultsForTest(t, cfgA)

	eng := NewEngine(ctx, cfgA, connectors.Hooks{})
	if names := eng.Endpoints(); len(names) != 2 {
		t.Fatalf("initial endpoints = %v", names)
	}

	cfgB := &config.Config{
		Settings: config.Settings{DegradedLatencyMs: 1500},
		Endpoints: []config.Endpoint{
			{Name: "b", URL: srv.URL},
			{Name: "c", URL: srv.URL},
		},
	}
	applyDefaultsForTest(t, cfgB)
	eng.Reload(ctx, cfgB)

	names := eng.Endpoints()
	if len(names) != 2 || names[0] != "b" || names[1] != "c" {
		t.Fatalf("after reload endpoints = %v, want [b c]", names)
	}
	if eng.Has("a") {
		t.Error("endpoint a should be removed after reload")
	}
	if !eng.Has("c") {
		t.Error("endpoint c should exist after reload")
	}
}

func TestUptimeAlertFiresAndRecovers(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	cfg := &config.Config{
		Settings:  config.Settings{DegradedLatencyMs: 1500, AlertAfter: 3},
		Endpoints: []config.Endpoint{{Name: "api", URL: srv.URL}},
	}
	applyDefaultsForTest(t, cfg)

	rec := &recordingNotifier{}
	hooks := connectors.Hooks{Uptime: rec}
	eng := NewEngine(ctx, cfg, hooks)

	// Below threshold (50% < 90%) with enough samples -> alert fires.
	eng.SetUptime(stubUptime{up: 0.5, n: 3}, 90, time.Hour)
	if _, ok := eng.CheckOne(ctx, "api"); !ok {
		t.Fatal("check failed")
	}
	if rec.uptimeAlerts != 1 {
		t.Errorf("uptime alerts = %d, want 1", rec.uptimeAlerts)
	}
	if rec.downs != 0 {
		t.Errorf("no sustained-down should fire on a healthy check, got %d", rec.downs)
	}

	// Back above threshold -> recovery fires exactly once.
	eng.SetUptime(stubUptime{up: 1.0, n: 3}, 90, time.Hour)
	if _, ok := eng.CheckOne(ctx, "api"); !ok {
		t.Fatal("check failed")
	}
	if rec.uptimeRecovers != 1 {
		t.Errorf("uptime recovers = %d, want 1", rec.uptimeRecovers)
	}
}

func TestUptimeAlertNeedsMinSamples(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	cfg := &config.Config{
		Settings:  config.Settings{DegradedLatencyMs: 1500, AlertAfter: 3},
		Endpoints: []config.Endpoint{{Name: "api", URL: srv.URL}},
	}
	applyDefaultsForTest(t, cfg)

	rec := &recordingNotifier{}
	eng := NewEngine(ctx, cfg, connectors.Hooks{Uptime: rec})
	eng.SetUptime(stubUptime{up: 0.1, n: 1}, 90, time.Hour) // n < alertAfter
	if _, ok := eng.CheckOne(ctx, "api"); !ok {
		t.Fatal("check failed")
	}
	if rec.uptimeAlerts != 0 {
		t.Errorf("should not alert with too few samples, got %d", rec.uptimeAlerts)
	}
}

// applyDefaultsForTest applies config defaults without a TOML file (the config
// package's applyDefaults is unexported, so the minimal fields are set inline).
func applyDefaultsForTest(t *testing.T, c *config.Config) {
	t.Helper()
	if c.Settings.IntervalSeconds <= 0 {
		c.Settings.IntervalSeconds = 30
	}
	if c.Settings.AlertAfter <= 0 {
		c.Settings.AlertAfter = 3
	}
	if c.Settings.AlertClearAfter <= 0 {
		c.Settings.AlertClearAfter = 1
	}
	if c.Settings.DegradedLatencyMs <= 0 {
		c.Settings.DegradedLatencyMs = 1500
	}
	for i := range c.Endpoints {
		if c.Endpoints[i].Method == "" {
			c.Endpoints[i].Method = "GET"
		}
		if c.Endpoints[i].DegradedLatencyMs <= 0 {
			c.Endpoints[i].DegradedLatencyMs = c.Settings.DegradedLatencyMs
		}
	}
}
