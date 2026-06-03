// Command svchealth is a terminal Service Health Board: it runs automated
// health checks against configured API endpoints (with OpenAPI/JSON Schema
// discovery), shows color-coded status, short-term uptime history, and local +
// remote host metrics in a responsive Bubble Tea TUI.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/pv-udpv/svchealth/internal/checks"
	"github.com/pv-udpv/svchealth/internal/config"
	"github.com/pv-udpv/svchealth/internal/connectors"
	"github.com/pv-udpv/svchealth/internal/exporter"
	"github.com/pv-udpv/svchealth/internal/store"
	"github.com/pv-udpv/svchealth/internal/ui"
)

func main() {
	cfgPath := flag.String("config", "config.toml", "path to TOML config file")
	flag.Parse()

	if err := run(*cfgPath); err != nil {
		fmt.Fprintln(os.Stderr, "svchealth:", err)
		os.Exit(1)
	}
}

func run(cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}

	st, err := store.Open(cfg.Settings.DBPath, cfg.Settings.HistorySize)
	if err != nil {
		return err
	}
	defer st.Close()

	// Optional Supabase mirror (enabled only when env vars are set).
	supa, err := store.FromEnv()
	if err != nil {
		return err
	}

	ctx := context.Background()

	// Connector hooks default to no-ops; real Cloudflare/Linear/Clerk
	// implementations are injected when their env vars are set.
	hooks, herr := buildHooks()
	if herr != nil {
		return herr
	}

	eng := checks.NewEngine(ctx, cfg, hooks)

	// Optional HTTP exporter (/metrics + /snapshot). Enabled by config or the
	// SVCHEALTH_METRICS_LISTEN env var (env takes precedence).
	var exp *exporter.Exporter
	if addr := metricsAddr(cfg); addr != "" {
		exp = exporter.New(addr, eng, st, time.Hour)
		_ = exp.Start()
		defer func() {
			sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = exp.Shutdown(sctx)
		}()
	}

	model := ui.New(ctx, eng, st, supa, cfg)
	p := tea.NewProgram(model, tea.WithAltScreen())
	_, err = p.Run()
	// Best-effort final flush of any buffered Supabase rows.
	if supa != nil {
		_ = supa.Flush(ctx)
	}
	return err
}

// metricsAddr resolves the exporter listen address, env overriding config.
func metricsAddr(cfg *config.Config) string {
	if v := os.Getenv("SVCHEALTH_METRICS_LISTEN"); v != "" {
		return v
	}
	return cfg.Settings.MetricsListen
}

// buildHooks assembles connector hooks from the environment. Each connector
// returns (nil, nil) when not configured, so the no-op defaults are used and
// the binary stays fully self-contained with zero required external services.
func buildHooks() (connectors.Hooks, error) {
	// Start from zero-value hooks (all nil). The engine treats nil fields as
	// no-ops, and the UI only surfaces edge status when a real Edge connector is
	// present — so unconfigured connectors leave no misleading "n/a" output.
	var hooks connectors.Hooks

	// Notifiers fan out: Linear (issue tracking) and Telegram (chat alerts) can
	// both be active. Collected into a MultiNotifier when more than one is set.
	var notifiers connectors.MultiNotifier
	if lin, err := connectors.NewLinearFromEnv(); err != nil {
		return hooks, err
	} else if lin != nil {
		notifiers = append(notifiers, lin)
	}
	if tg, err := connectors.NewTelegramFromEnv(); err != nil {
		return hooks, err
	} else if tg != nil {
		notifiers = append(notifiers, tg)
	}
	switch len(notifiers) {
	case 0:
		// leave hooks.Notifier nil (no-op)
	case 1:
		hooks.Notifier = notifiers[0]
	default:
		hooks.Notifier = notifiers
	}

	if cf, err := connectors.NewCloudflareFromEnv(); err != nil {
		return hooks, err
	} else if cf != nil {
		hooks.Edge = cf
	}

	if ck, err := connectors.NewClerkFromEnv(); err != nil {
		return hooks, err
	} else if ck != nil {
		hooks.Auth = ck
	}

	return hooks, nil
}
