package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pv-udpv/svchealth/internal/checks"
	"github.com/pv-udpv/svchealth/internal/config"
	"github.com/pv-udpv/svchealth/internal/connectors"
	"github.com/pv-udpv/svchealth/internal/store"
)

const endpointA = `
[settings]
[[endpoint]]
name = "a"
url  = "https://a.example"

[[endpoint]]
name = "b"
url  = "https://b.example"
`

const endpointABC = `
[settings]
[[endpoint]]
name = "a"
url  = "https://a.example"

[[endpoint]]
name = "b"
url  = "https://b.example"

[[endpoint]]
name = "c"
url  = "https://b.example/extra"
`

// newReloadHarness builds a Model wired to a real engine + store, watching a
// temp config file.
func newReloadHarness(t *testing.T, body string) (Model, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	st, err := store.Open(filepath.Join(dir, "test.db"), 60)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	eng := checks.NewEngine(context.Background(), cfg, connectors.Hooks{})
	m := New(context.Background(), eng, st, nil, cfg).WithReload(cfgPath)
	return m, cfgPath
}

func TestNewPopulatesRowsAndGroups(t *testing.T) {
	m, _ := newReloadHarness(t, endpointA)
	if len(m.rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(m.rows))
	}
	if m.rows[0].name != "a" || m.rows[0].url != "https://a.example" {
		t.Errorf("row[0] = %+v", m.rows[0])
	}
	if m.cfgPath == "" || m.cfgMod.IsZero() {
		t.Error("WithReload should record cfgPath and cfgMod")
	}
}

func TestGroupOf(t *testing.T) {
	cfg := &config.Config{Endpoints: []config.Endpoint{
		{Name: "a", Group: "core"},
		{Name: "b"},
	}}
	m := Model{cfg: cfg}
	if m.groupOf("a") != "core" {
		t.Errorf("groupOf(a) = %q, want core", m.groupOf("a"))
	}
	if m.groupOf("b") != "" {
		t.Errorf("groupOf(b) = %q, want empty", m.groupOf("b"))
	}
	if m.groupOf("missing") != "" {
		t.Errorf("groupOf(missing) = %q, want empty", m.groupOf("missing"))
	}
}

func TestMaybeReloadNoChange(t *testing.T) {
	m, _ := newReloadHarness(t, endpointA)
	m2, cmds := m.maybeReload()
	if len(cmds) != 0 {
		t.Errorf("no-change reload should return no commands, got %d", len(cmds))
	}
	if len(m2.rows) != 2 {
		t.Errorf("rows changed unexpectedly: %d", len(m2.rows))
	}
}

func TestMaybeReloadAppliesChanges(t *testing.T) {
	m, cfgPath := newReloadHarness(t, endpointA)

	// Rewrite config with an extra endpoint and bump the mtime forward.
	if err := os.WriteFile(cfgPath, []byte(endpointABC), 0o600); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(cfgPath, future, future); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	m2, cmds := m.maybeReload()
	if len(m2.rows) != 3 {
		t.Fatalf("after reload rows = %d, want 3", len(m2.rows))
	}
	if !m2.engine.Has("c") {
		t.Error("engine should know endpoint c after reload")
	}
	// Commands: re-check all 3 + schedule the 1 new endpoint.
	if len(cmds) != 4 {
		t.Errorf("cmds = %d, want 4 (3 checks + 1 schedule)", len(cmds))
	}
	if m2.lastErr != "" {
		t.Errorf("unexpected lastErr: %q", m2.lastErr)
	}
}

func TestMaybeReloadInvalidConfigKeepsOld(t *testing.T) {
	m, cfgPath := newReloadHarness(t, endpointA)

	if err := os.WriteFile(cfgPath, []byte("not [[ valid toml"), 0o600); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(cfgPath, future, future); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	m2, cmds := m.maybeReload()
	if len(m2.rows) != 2 {
		t.Errorf("invalid config should keep old rows, got %d", len(m2.rows))
	}
	if len(cmds) != 0 {
		t.Errorf("invalid config should return no commands, got %d", len(cmds))
	}
	if m2.lastErr == "" {
		t.Error("expected lastErr to record the reload failure")
	}
}

func TestApplyResultUpdatesRow(t *testing.T) {
	m, _ := newReloadHarness(t, endpointA)
	// Seed one green sample so Recent/Uptime have history to read back.
	now := time.Now()
	if err := m.store.Insert(checks.Result{Endpoint: "a", Status: checks.StatusGreen, HTTPStatus: 200, Latency: 42 * time.Millisecond, At: now}); err != nil {
		t.Fatalf("seed Insert: %v", err)
	}

	_ = m.applyResult(checks.Result{Endpoint: "a", Status: checks.StatusRed, HTTPStatus: 500, Latency: 5 * time.Millisecond, Err: "boom"})

	var target *row
	for i := range m.rows {
		if m.rows[i].name == "a" {
			target = &m.rows[i]
		}
	}
	if target == nil {
		t.Fatal("row 'a' not found after applyResult")
	}
	if target.last.Status != checks.StatusRed || target.last.Err != "boom" {
		t.Errorf("applyResult did not update last result: %+v", target.last)
	}
	if target.samples != 1 {
		t.Errorf("samples = %d, want 1 (from seeded history)", target.samples)
	}
	if !strings.Contains(m.lastErr, "boom") {
		t.Errorf("lastErr = %q, want it to contain 'boom'", m.lastErr)
	}
}

func TestRenderDetail(t *testing.T) {
	m, _ := newReloadHarness(t, endpointA)
	m.width = 80 // wide enough to avoid wrapping assertions across the border
	m.detail = true
	m.detailName = "a"
	m.detailStats = store.Stats{P50: 10, P90: 20, P99: 30, MinMs: 5, MaxMs: 40, Green: 3, Yellow: 1, Red: 1, Samples: 5}
	m.detailLatency = []int64{10, 20, 30}
	m.detailWindows = []store.WindowAgg{{Label: "1h", Samples: 5, Uptime: 0.8, P50: 10, P99: 30}}

	out := m.renderDetail()
	for _, want := range []string{
		"p50 10ms", "p90 20ms", "p99 30ms",
		"3 up", "1 deg", "1 down",
		"lat/time", "1h", "80.0%",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("renderDetail missing %q:\n%s", want, out)
		}
	}
}
