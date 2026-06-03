// Command viewtest renders the TUI View() once at a fixed size (no TTY needed)
// to validate layout, colors, and responsiveness.
package main

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/pv-udpv/svchealth/internal/checks"
	"github.com/pv-udpv/svchealth/internal/config"
	"github.com/pv-udpv/svchealth/internal/connectors"
	"github.com/pv-udpv/svchealth/internal/store"
	"github.com/pv-udpv/svchealth/internal/ui"
)

func main() {
	cfg, _ := config.Load("config.toml")
	st, _ := store.Open(cfg.Settings.DBPath, cfg.Settings.HistorySize)
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	eng := checks.NewEngine(ctx, cfg, connectors.DefaultHooks())

	// seed some history so sparklines/uptime show
	for i := 0; i < 3; i++ {
		for _, n := range eng.Endpoints() {
			res, _ := eng.CheckOne(ctx, n)
			_ = st.Insert(res)
		}
	}

	m := ui.New(ctx, eng, st, nil, cfg)
	// feed the model results so rows populate, at two widths
	for _, w := range []int{100, 60} {
		mm, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: 25})
		model := mm.(ui.Model)
		for _, n := range eng.Endpoints() {
			res, _ := eng.CheckOne(ctx, n)
			_ = st.Insert(res)
			m2, _ := model.Update(resultInjector(res))
			model = m2.(ui.Model)
		}
		fmt.Printf("\n========== WIDTH %d ==========\n", w)
		fmt.Println(model.View())
		m = model
	}

	// Drive interactive features at width 100: worst-first sort, then open
	// detail on the top (worst) row.
	mm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model := mm.(ui.Model)
	model = key(model, "s")     // sort worst-first
	model = key(model, "enter") // open detail on cursor (top = worst)
	fmt.Printf("\n========== WORST-FIRST + DETAIL ==========\n")
	fmt.Println(model.View())

	// Problems-only filter
	model = key(model, "esc") // close detail
	model = key(model, "f")   // filter problems
	fmt.Printf("\n========== FILTER: PROBLEMS ONLY ==========\n")
	fmt.Println(model.View())
}

func key(m ui.Model, s string) ui.Model {
	var km tea.KeyMsg
	switch s {
	case "enter":
		km = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		km = tea.KeyMsg{Type: tea.KeyEsc}
	default:
		km = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
	mm, _ := m.Update(km)
	return mm.(ui.Model)
}

// resultInjector wraps a result so the model's Update applies it. The ui
// package exposes InjectResult for test harnesses.
func resultInjector(r checks.Result) tea.Msg { return ui.InjectResult(r) }
