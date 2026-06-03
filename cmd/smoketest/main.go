// Command smoketest exercises the engine headlessly (no TTY) to validate
// config loading, spec discovery, live checks, classification, and history.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/pv-udpv/svchealth/internal/checks"
	"github.com/pv-udpv/svchealth/internal/config"
	"github.com/pv-udpv/svchealth/internal/connectors"
	"github.com/pv-udpv/svchealth/internal/metrics"
	"github.com/pv-udpv/svchealth/internal/store"
)

func main() {
	cfg, err := config.Load("config.toml")
	must(err)
	fmt.Printf("loaded %d endpoints; db=%s\n", len(cfg.Endpoints), cfg.Settings.DBPath)

	st, err := store.Open(cfg.Settings.DBPath, cfg.Settings.HistorySize)
	must(err)
	defer st.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	eng := checks.NewEngine(ctx, cfg, connectors.DefaultHooks())

	fmt.Println("\n-- local metrics --")
	fmt.Println(metrics.ReadLocal(".").Format())

	fmt.Println("\n-- resolved targets --")
	for _, n := range eng.Endpoints() {
		fmt.Printf("  %-16s -> %s\n", n, eng.TargetURL(n))
	}

	fmt.Println("\n-- two check rounds --")
	for round := 1; round <= 2; round++ {
		for _, n := range eng.Endpoints() {
			res, _ := eng.CheckOne(ctx, n)
			_ = st.Insert(res)
			fmt.Printf("  [r%d] %-16s %-8s http=%-3d lat=%5dms %s\n",
				round, n, res.Status, res.HTTPStatus, res.Latency.Milliseconds(), res.Err)
		}
	}

	fmt.Println("\n-- uptime + sparkline --")
	for _, n := range eng.Endpoints() {
		samples, _ := st.Recent(n)
		up, total, _ := st.Uptime(n, time.Hour)
		spark, _ := store.Sparkline(samples, 20)
		fmt.Printf("  %-16s up=%.0f%% (n=%d) [%s]\n", n, up*100, total, spark)
	}

	fmt.Println("\nOK")
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "FATAL:", err)
		os.Exit(1)
	}
}
