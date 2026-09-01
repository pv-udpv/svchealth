package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/pv-udpv/svchealth/internal/config"
	"github.com/pv-udpv/svchealth/internal/store"
)

// exportRow is the JSON shape of a single exported sample.
type exportRow struct {
	Endpoint   string `json:"endpoint"`
	Status     string `json:"status"`
	HTTPStatus int    `json:"http_status"`
	LatencyMs  int64  `json:"latency_ms"`
	At         string `json:"at"`
	Err        string `json:"err,omitempty"`
}

// runExport implements the `svchealth export` subcommand: it dumps persisted
// health-check samples from SQLite to JSON or CSV (stdout or a file).
func runExport(args []string) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	format := fs.String("format", "json", "output format: json or csv")
	out := fs.String("out", "", "output file path (default: stdout)")
	endpoint := fs.String("endpoint", "", "restrict to a single endpoint name")
	since := fs.Duration("since", 0, "lookback window (e.g. 24h); empty = all")
	dbPath := fs.String("db", "", "sqlite db path (default: from config or svchealth.db)")
	cfgPath := fs.String("config", "config.toml", "config file (for default db path)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *format != "json" && *format != "csv" {
		return fmt.Errorf("unsupported format %q (want json or csv)", *format)
	}

	db := *dbPath
	if db == "" {
		db = defaultDBPath(*cfgPath)
	}

	st, err := store.Open(db, 0)
	if err != nil {
		return err
	}
	defer st.Close()

	samples, err := st.Export(*endpoint, *since)
	if err != nil {
		return err
	}

	var w *os.File
	if *out == "" {
		w = os.Stdout
	} else {
		f, err := os.Create(*out)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}

	switch *format {
	case "json":
		return writeJSON(w, samples)
	default:
		return writeCSV(w, samples)
	}
}

func defaultDBPath(cfgPath string) string {
	if cfg, err := config.Load(cfgPath); err == nil && cfg.Settings.DBPath != "" {
		return cfg.Settings.DBPath
	}
	return "svchealth.db"
}

func writeJSON(w *os.File, samples []store.Sample) error {
	type doc struct {
		GeneratedAt string      `json:"generated_at"`
		Count       int         `json:"count"`
		Samples     []exportRow `json:"samples"`
	}
	rows := make([]exportRow, 0, len(samples))
	for _, s := range samples {
		rows = append(rows, toRow(s))
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(doc{GeneratedAt: time.Now().UTC().Format(time.RFC3339), Count: len(rows), Samples: rows})
}

func writeCSV(w *os.File, samples []store.Sample) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"endpoint", "status", "http_status", "latency_ms", "at", "err"}); err != nil {
		return err
	}
	for _, s := range samples {
		if err := cw.Write([]string{
			s.Endpoint,
			s.Status.String(),
			strconv.Itoa(s.HTTPStatus),
			strconv.FormatInt(s.LatencyMs, 10),
			s.At.UTC().Format(time.RFC3339),
			s.Err,
		}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func toRow(s store.Sample) exportRow {
	return exportRow{
		Endpoint:   s.Endpoint,
		Status:     s.Status.String(),
		HTTPStatus: s.HTTPStatus,
		LatencyMs:  s.LatencyMs,
		At:         s.At.UTC().Format(time.RFC3339),
		Err:        s.Err,
	}
}
