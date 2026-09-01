package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTmp(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write tmp config: %v", err)
	}
	return p
}

func TestLoadAppliesDefaults(t *testing.T) {
	p := writeTmp(t, `
[settings]
[[endpoint]]
name = "a"
url  = "https://example.com"
`)
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	s := c.Settings
	if s.IntervalSeconds != defaultInterval {
		t.Errorf("IntervalSeconds = %d, want %d", s.IntervalSeconds, defaultInterval)
	}
	if s.TimeoutSeconds != defaultTimeout {
		t.Errorf("TimeoutSeconds = %d, want %d", s.TimeoutSeconds, defaultTimeout)
	}
	if s.HistorySize != defaultHistorySize {
		t.Errorf("HistorySize = %d, want %d", s.HistorySize, defaultHistorySize)
	}
	if s.DBPath != defaultDBPath {
		t.Errorf("DBPath = %q, want %q", s.DBPath, defaultDBPath)
	}
	if s.DegradedLatencyMs != defaultDegradedMs {
		t.Errorf("DegradedLatencyMs = %d, want %d", s.DegradedLatencyMs, defaultDegradedMs)
	}
	if s.AlertAfter != defaultAlertAfter {
		t.Errorf("AlertAfter = %d, want %d", s.AlertAfter, defaultAlertAfter)
	}
	if s.AlertClearAfter != defaultAlertClearAfter {
		t.Errorf("AlertClearAfter = %d, want %d", s.AlertClearAfter, defaultAlertClearAfter)
	}

	ep := c.Endpoints[0]
	if ep.Method != defaultHTTPMethod {
		t.Errorf("endpoint Method = %q, want %q", ep.Method, defaultHTTPMethod)
	}
	if ep.IntervalSeconds != defaultInterval {
		t.Errorf("endpoint IntervalSeconds = %d, want %d (inherit global)", ep.IntervalSeconds, defaultInterval)
	}
	// DegradedLatencyMs should inherit the (defaulted) global value.
	if ep.DegradedLatencyMs != defaultDegradedMs {
		t.Errorf("endpoint DegradedLatencyMs = %d, want %d (inherit)", ep.DegradedLatencyMs, defaultDegradedMs)
	}
}

func TestPerEndpointOverridesInherit(t *testing.T) {
	p := writeTmp(t, `
[settings]
interval_seconds   = 30
degraded_latency_ms = 1500
critical_latency_ms = 5000

[[endpoint]]
name = "override"
url  = "https://a.example"
interval_seconds    = 60
degraded_latency_ms = 800
critical_latency_ms = 3000

[[endpoint]]
name = "inherit"
url  = "https://b.example"
`)
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	o, in := c.Endpoints[0], c.Endpoints[1]
	if o.IntervalSeconds != 60 || o.DegradedLatencyMs != 800 || o.CriticalLatencyMs != 3000 {
		t.Errorf("override endpoint not honored: %+v", o)
	}
	if in.IntervalSeconds != 30 || in.DegradedLatencyMs != 1500 || in.CriticalLatencyMs != 5000 {
		t.Errorf("inherit endpoint did not inherit globals: %+v", in)
	}
}

func TestValidateErrors(t *testing.T) {
	cases := map[string]string{
		"no endpoints": `
[settings]
`,
		"empty name": `
[[endpoint]]
url = "https://x"
`,
		"duplicate name": `
[[endpoint]]
name = "dup"
url  = "https://a"
[[endpoint]]
name = "dup"
url  = "https://b"
`,
		"neither url nor spec": `
[[endpoint]]
name = "x"
`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeTmp(t, body)); err == nil {
				t.Errorf("expected validation error for %q, got nil", name)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load("/nonexistent/path/config.toml"); err == nil {
		t.Error("expected error for missing file, got nil")
	}
}

func TestUptimeWindowParsing(t *testing.T) {
	s := Settings{UptimeAlertWindow: "24h"}
	if d := s.UptimeWindow(); d != 24*time.Hour {
		t.Errorf("UptimeWindow() = %v, want 24h", d)
	}
	if d := (Settings{}).UptimeWindow(); d != time.Hour {
		t.Errorf("default UptimeWindow() = %v, want 1h", d)
	}
	if d := (Settings{UptimeAlertWindow: "garbage"}).UptimeWindow(); d != time.Hour {
		t.Errorf("invalid window should fall back to 1h, got %v", d)
	}
}

func TestGroupFieldParses(t *testing.T) {
	p := writeTmp(t, `
[[endpoint]]
name  = "core-api"
url   = "https://a.example"
group = "core"

[[endpoint]]
name = "edge"
url  = "https://b.example"
`)
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Endpoints[0].Group != "core" {
		t.Errorf("group = %q, want core", c.Endpoints[0].Group)
	}
	if c.Endpoints[1].Group != "" {
		t.Errorf("ungrouped endpoint group = %q, want empty", c.Endpoints[1].Group)
	}
}
