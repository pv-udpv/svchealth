package main

import (
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pv-udpv/svchealth/internal/checks"
	"github.com/pv-udpv/svchealth/internal/store"
)

func sampleList() []store.Sample {
	return []store.Sample{
		{Endpoint: "api", Status: checks.StatusGreen, HTTPStatus: 200, LatencyMs: 42, At: time.Unix(1_700_000_000, 0), Err: ""},
		{Endpoint: "db", Status: checks.StatusRed, HTTPStatus: 500, LatencyMs: 7, At: time.Unix(1_700_000_060, 0), Err: "HTTP 500"},
	}
}

func TestToRow(t *testing.T) {
	s := store.Sample{Endpoint: "api", Status: checks.StatusYellow, HTTPStatus: 200, LatencyMs: 123, At: time.Unix(1_700_000_000, 0), Err: "slow"}
	r := toRow(s)
	if r.Endpoint != "api" || r.Status != "DEGRADED" || r.HTTPStatus != 200 || r.LatencyMs != 123 {
		t.Errorf("toRow = %+v", r)
	}
	if !strings.HasSuffix(r.At, "Z") {
		t.Errorf("At should be RFC3339 UTC, got %q", r.At)
	}
	if r.Err != "slow" {
		t.Errorf("Err = %q, want slow", r.Err)
	}
}

func TestWriteJSON(t *testing.T) {
	f := tmpFile(t)
	defer f.Close()

	if err := writeJSON(f, sampleList()); err != nil {
		t.Fatalf("writeJSON: %v", err)
	}

	raw, _ := os.ReadFile(f.Name())
	var doc struct {
		Count   int         `json:"count"`
		Samples []exportRow `json:"samples"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if doc.Count != 2 || len(doc.Samples) != 2 {
		t.Errorf("count=%d samples=%d, want 2/2", doc.Count, len(doc.Samples))
	}
	if doc.Samples[0].Endpoint != "api" || doc.Samples[0].Status != "UP" {
		t.Errorf("samples[0] = %+v, want api/UP", doc.Samples[0])
	}
	if doc.Samples[1].Err != "HTTP 500" {
		t.Errorf("samples[1].Err = %q, want HTTP 500", doc.Samples[1].Err)
	}
}

func TestWriteCSV(t *testing.T) {
	f := tmpFile(t)
	defer f.Close()

	if err := writeCSV(f, sampleList()); err != nil {
		t.Fatalf("writeCSV: %v", err)
	}

	raw, _ := os.ReadFile(f.Name())
	r := csv.NewReader(strings.NewReader(string(raw)))
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatalf("invalid CSV: %v", err)
	}
	if len(rows) != 3 { // header + 2 data rows
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	if strings.Join(rows[0], ",") != "endpoint,status,http_status,latency_ms,at,err" {
		t.Errorf("header = %v", rows[0])
	}
	if rows[1][0] != "api" || rows[1][1] != "UP" || rows[1][3] != "42" {
		t.Errorf("row[1] = %v", rows[1])
	}
	if rows[2][1] != "DOWN" || rows[2][5] != "HTTP 500" {
		t.Errorf("row[2] = %v", rows[2])
	}
}

func TestDefaultDBPath(t *testing.T) {
	// With a config that sets db_path, it should be honored.
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	os.WriteFile(cfgPath, []byte("[settings]\ndb_path = \"/tmp/custom.db\"\n\n[[endpoint]]\nname = \"a\"\nurl = \"https://a.example\"\n"), 0o600)
	if got := defaultDBPath(cfgPath); got != "/tmp/custom.db" {
		t.Errorf("defaultDBPath(config) = %q, want /tmp/custom.db", got)
	}
	// Missing config falls back to the default.
	if got := defaultDBPath(filepath.Join(dir, "missing.toml")); got != "svchealth.db" {
		t.Errorf("defaultDBPath(missing) = %q, want svchealth.db", got)
	}
}

func tmpFile(t *testing.T) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "export*.out")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestRunExportEndToEnd(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "svc.db")

	st, err := store.Open(dbPath, 60)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	now := time.Now()
	if err := st.Insert(checks.Result{Endpoint: "api", Status: checks.StatusGreen, HTTPStatus: 200, Latency: 42 * time.Millisecond, At: now}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := st.Insert(checks.Result{Endpoint: "api", Status: checks.StatusRed, HTTPStatus: 500, Latency: 5 * time.Millisecond, At: now, Err: "HTTP 500"}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	st.Close()

	out := filepath.Join(dir, "out.json")
	if err := runExport([]string{"--format", "json", "--db", dbPath, "--out", out, "--endpoint", "api"}); err != nil {
		t.Fatalf("runExport: %v", err)
	}

	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	var doc struct {
		Count   int         `json:"count"`
		Samples []exportRow `json:"samples"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if doc.Count != 2 {
		t.Fatalf("count = %d, want 2", doc.Count)
	}
	if doc.Samples[0].Status != "UP" || doc.Samples[1].Status != "DOWN" {
		t.Errorf("samples = %+v, want UP then DOWN (oldest-first)", doc.Samples)
	}
}
