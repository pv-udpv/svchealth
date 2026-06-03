// Package config loads endpoint definitions and runtime settings from a TOML file.
package config

import (
	"fmt"
	"os"
	"time"

	"github.com/BurntSushi/toml"
)

// Config is the top-level configuration loaded from config.toml.
type Config struct {
	// Settings holds global runtime options.
	Settings Settings `toml:"settings"`
	// Endpoints is the list of monitored targets.
	Endpoints []Endpoint `toml:"endpoint"`
}

// Settings holds global, non-endpoint-specific options.
type Settings struct {
	// IntervalSeconds is the default polling cadence for scheduled checks.
	IntervalSeconds int `toml:"interval_seconds"`
	// TimeoutSeconds is the default per-request timeout.
	TimeoutSeconds int `toml:"timeout_seconds"`
	// HistorySize is the number of recent samples kept per endpoint for sparklines.
	HistorySize int `toml:"history_size"`
	// DBPath is the SQLite file used for short-term history persistence.
	DBPath string `toml:"db_path"`
	// DegradedLatencyMs marks a check YELLOW (degraded) above this latency even if 2xx.
	DegradedLatencyMs int `toml:"degraded_latency_ms"`
	// CriticalLatencyMs marks a check RED above this latency even if the code is
	// healthy (treats an extremely slow endpoint as down). 0 disables.
	CriticalLatencyMs int `toml:"critical_latency_ms"`
	// AlertAfter is the number of consecutive RED checks before a sustained-down
	// alert fires (debounce). Recovery requires AlertClearAfter healthy checks.
	AlertAfter int `toml:"alert_after"`
	// AlertClearAfter is the number of consecutive healthy checks required to
	// clear a firing alert (hysteresis). Defaults to 1.
	AlertClearAfter int `toml:"alert_clear_after"`
	// ShowLocalMetrics toggles the local host load/disk header bar.
	ShowLocalMetrics bool `toml:"show_local_metrics"`
	// MetricsListen, when non-empty (e.g. "127.0.0.1:9899"), starts an HTTP
	// exporter serving /metrics (Prometheus) and /snapshot (JSON). Can also be
	// set via the SVCHEALTH_METRICS_LISTEN env var, which takes precedence.
	MetricsListen string `toml:"metrics_listen"`
}

// Endpoint is a single monitored target.
type Endpoint struct {
	// Name is a short human label shown in the table.
	Name string `toml:"name"`
	// URL is the health-check target. If empty and SpecURI is set, targets are
	// derived from the spec.
	URL string `toml:"url"`
	// Method defaults to GET when empty.
	Method string `toml:"method"`
	// ExpectStatus is the HTTP status considered healthy. 0 means "any 2xx".
	ExpectStatus int `toml:"expect_status"`
	// SpecURI optionally points at an OpenAPI / JSON Schema document used to
	// discover and derive concrete health-check targets.
	SpecURI string `toml:"spec_uri"`
	// Headers are sent with each request (e.g. Authorization).
	Headers map[string]string `toml:"headers"`
	// IntervalSeconds overrides the global cadence for this endpoint.
	IntervalSeconds int `toml:"interval_seconds"`
	// TimeoutSeconds overrides the global timeout for this endpoint.
	TimeoutSeconds int `toml:"timeout_seconds"`
	// MetricsPath optionally points at a JSON payload exposing remote
	// load/disk metrics (e.g. "/metrics" or "/healthz").
	MetricsPath string `toml:"metrics_path"`
	// DegradedLatencyMs overrides the global degraded threshold for this endpoint.
	// 0 means "inherit global".
	DegradedLatencyMs int `toml:"degraded_latency_ms"`
	// CriticalLatencyMs overrides the global critical (RED) latency threshold for
	// this endpoint. 0 means "inherit global".
	CriticalLatencyMs int `toml:"critical_latency_ms"`
}

// Defaults applied when a value is omitted.
const (
	defaultInterval        = 30
	defaultTimeout         = 8
	defaultHistorySize     = 60
	defaultDBPath          = "svchealth.db"
	defaultDegradedMs      = 1500
	defaultHTTPMethod      = "GET"
	defaultExpectStatus    = 0 // any 2xx
	defaultAlertAfter      = 3
	defaultAlertClearAfter = 1
)

// Load reads and validates the TOML config at path, applying defaults.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	var c Config
	if _, err := toml.Decode(string(raw), &c); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	s := &c.Settings
	if s.IntervalSeconds <= 0 {
		s.IntervalSeconds = defaultInterval
	}
	if s.TimeoutSeconds <= 0 {
		s.TimeoutSeconds = defaultTimeout
	}
	if s.HistorySize <= 0 {
		s.HistorySize = defaultHistorySize
	}
	if s.DBPath == "" {
		s.DBPath = defaultDBPath
	}
	if s.DegradedLatencyMs <= 0 {
		s.DegradedLatencyMs = defaultDegradedMs
	}
	if s.AlertAfter <= 0 {
		s.AlertAfter = defaultAlertAfter
	}
	if s.AlertClearAfter <= 0 {
		s.AlertClearAfter = defaultAlertClearAfter
	}
	for i := range c.Endpoints {
		e := &c.Endpoints[i]
		if e.Method == "" {
			e.Method = defaultHTTPMethod
		}
		if e.IntervalSeconds <= 0 {
			e.IntervalSeconds = s.IntervalSeconds
		}
		if e.TimeoutSeconds <= 0 {
			e.TimeoutSeconds = s.TimeoutSeconds
		}
		// Per-endpoint latency thresholds inherit the global value when unset.
		if e.DegradedLatencyMs <= 0 {
			e.DegradedLatencyMs = s.DegradedLatencyMs
		}
		if e.CriticalLatencyMs <= 0 {
			e.CriticalLatencyMs = s.CriticalLatencyMs
		}
	}
}

func (c *Config) validate() error {
	if len(c.Endpoints) == 0 {
		return fmt.Errorf("config has no [[endpoint]] entries")
	}
	seen := map[string]bool{}
	for i, e := range c.Endpoints {
		if e.Name == "" {
			return fmt.Errorf("endpoint #%d has empty name", i+1)
		}
		if seen[e.Name] {
			return fmt.Errorf("duplicate endpoint name %q", e.Name)
		}
		seen[e.Name] = true
		if e.URL == "" && e.SpecURI == "" {
			return fmt.Errorf("endpoint %q has neither url nor spec_uri", e.Name)
		}
	}
	return nil
}

// Interval returns the effective polling interval for an endpoint.
func (e Endpoint) Interval() time.Duration {
	return time.Duration(e.IntervalSeconds) * time.Second
}

// Timeout returns the effective request timeout for an endpoint.
func (e Endpoint) Timeout() time.Duration {
	return time.Duration(e.TimeoutSeconds) * time.Second
}
