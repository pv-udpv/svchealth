// Package ui implements the Bubble Tea terminal interface for the Service
// Health Board: a responsive, color-coded, compact infrastructure overview.
package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/pv-udpv/svchealth/internal/checks"
	"github.com/pv-udpv/svchealth/internal/config"
	"github.com/pv-udpv/svchealth/internal/metrics"
	"github.com/pv-udpv/svchealth/internal/store"
)

// --- Styles (Lipgloss) ---

var (
	green  = lipgloss.Color("42")  // healthy
	yellow = lipgloss.Color("214") // degraded
	red    = lipgloss.Color("196") // down
	dim    = lipgloss.Color("241")
	accent = lipgloss.Color("69")
	fg     = lipgloss.Color("252")

	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231")).Background(accent).Padding(0, 1)
	headerStyle = lipgloss.NewStyle().Foreground(dim)
	footerStyle = lipgloss.NewStyle().Foreground(dim)
	selStyle    = lipgloss.NewStyle().Background(lipgloss.Color("236"))
	colHdrStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("250")).Underline(true)
	metricStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
)

func statusColor(s checks.Status) lipgloss.Color {
	switch s {
	case checks.StatusGreen:
		return green
	case checks.StatusYellow:
		return yellow
	case checks.StatusRed:
		return red
	default:
		return dim
	}
}

// --- Messages ---

type tickMsg struct{ name string }
type resultMsg struct{ res checks.Result }

// InjectResult wraps a check result as a Model message. Exposed for test/
// preview harnesses that drive the model without a live event loop.
func InjectResult(r checks.Result) tea.Msg { return resultMsg{res: r} }

type metricsMsg struct{ local metrics.Local }
type remoteMsg struct{ remote metrics.Remote }
type pruneMsg struct{}
type supaFlushMsg struct{}

// --- Row state ---

type row struct {
	name      string
	url       string
	last      checks.Result
	uptime    float64
	samples   int
	spark     string
	sparkStat []checks.Status
	remote    metrics.Remote
}

// sortMode controls row ordering.
type sortMode int

const (
	sortConfig     sortMode = iota // original config order
	sortWorstFirst                 // red, then yellow, then green
)

// filterMode controls which statuses are shown.
type filterMode int

const (
	filterAll      filterMode = iota
	filterProblems            // red + yellow only
)

// Model is the Bubble Tea model.
type Model struct {
	ctx    context.Context
	engine *checks.Engine
	store  *store.Store
	supa   *store.Supabase // optional secondary writer (nil if unconfigured)
	cfg    *config.Config

	width, height int
	cursor        int // index into the visible (filtered+sorted) list
	rows          []row
	order         []string // endpoint names in config order
	local         metrics.Local
	uptimeWindow  time.Duration
	lastErr       string
	quitting      bool

	// view state
	sort             sortMode
	filter           filterMode
	search           string // case-insensitive name substring filter
	searching        bool   // true while typing a search query
	detail           bool   // true when the detail/expand pane is shown
	detailName       string // endpoint shown in detail pane
	detailStats      store.Stats
	detailEdgeOK     bool              // edge connector configured for this endpoint
	detailEdgeUp     bool              // edge/tunnel reported healthy
	detailEdgeDetail string            // edge status detail string
	detailLatency    []int64           // recent latency history (ms) for the graph
	detailWindows    []store.WindowAgg // 1h / 24h aggregates
}

// New builds the initial model. supa may be nil to disable Supabase mirroring.
func New(ctx context.Context, eng *checks.Engine, st *store.Store, supa *store.Supabase, cfg *config.Config) Model {
	order := eng.Endpoints()
	rows := make([]row, len(order))
	for i, n := range order {
		rows[i] = row{name: n, url: eng.TargetURL(n)}
	}
	return Model{
		ctx:          ctx,
		engine:       eng,
		store:        st,
		supa:         supa,
		cfg:          cfg,
		rows:         rows,
		order:        order,
		uptimeWindow: time.Hour,
	}
}

// Init kicks off the first check for every endpoint plus the metrics+prune loops.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.metricsCmd(), m.pruneCmd()}
	if m.supa != nil {
		cmds = append(cmds, m.supaFlushCmd())
	}
	for _, n := range m.order {
		cmds = append(cmds, m.checkCmd(n))    // immediate first check
		cmds = append(cmds, m.scheduleCmd(n)) // arm recurring cadence
	}
	return tea.Batch(cmds...)
}

// --- Commands ---

func (m Model) checkCmd(name string) tea.Cmd {
	return func() tea.Msg {
		res, ok := m.engine.CheckOne(m.ctx, name)
		if !ok {
			return nil
		}
		_ = m.store.Insert(res)
		m.supa.Enqueue(m.ctx, res) // no-op if supa is nil
		return resultMsg{res: res}
	}
}

// supaFlushCmd periodically flushes the Supabase buffer.
func (m Model) supaFlushCmd() tea.Cmd {
	return tea.Tick(15*time.Second, func(time.Time) tea.Msg { return supaFlushMsg{} })
}

// scheduleCmd waits one interval then signals a re-check for name.
func (m Model) scheduleCmd(name string) tea.Cmd {
	d := m.engine.IntervalOf(name)
	return tea.Tick(d, func(time.Time) tea.Msg { return tickMsg{name: name} })
}

func (m Model) metricsCmd() tea.Cmd {
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg {
		return metricsMsg{local: metrics.ReadLocal(".")}
	})
}

func (m Model) pruneCmd() tea.Cmd {
	return tea.Tick(5*time.Minute, func(time.Time) tea.Msg { return pruneMsg{} })
}

// --- Update ---

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case tickMsg:
		// scheduled re-check, then reschedule
		return m, tea.Batch(m.checkCmd(msg.name), m.scheduleCmd(msg.name))

	case resultMsg:
		cmd := m.applyResult(msg.res)
		return m, cmd

	case remoteMsg:
		for i := range m.rows {
			if m.rows[i].name == msg.remote.Endpoint {
				m.rows[i].remote = msg.remote
				break
			}
		}
		return m, nil

	case metricsMsg:
		m.local = msg.local
		return m, m.metricsCmd()

	case pruneMsg:
		_ = m.store.Prune(24 * time.Hour)
		return m, m.pruneCmd()

	case supaFlushMsg:
		if m.supa != nil {
			go m.supa.Flush(m.ctx) // fire-and-forget; errors are retried on next flush
		}
		return m, m.supaFlushCmd()
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Search-input mode captures most keystrokes.
	if m.searching {
		return m.handleSearchKey(msg)
	}

	vis := m.visible()
	switch msg.String() {
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "esc":
		// close detail, else clear search/filter
		if m.detail {
			m.detail = false
		} else if m.search != "" {
			m.search = ""
		} else if m.filter != filterAll {
			m.filter = filterAll
		}
		return m, nil
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
		return m.refreshDetail()
	case "down", "j":
		if m.cursor < len(vis)-1 {
			m.cursor++
		}
		return m.refreshDetail()
	case "g":
		m.cursor = 0
		return m.refreshDetail()
	case "G":
		m.cursor = len(vis) - 1
		return m.refreshDetail()
	case "r", " ":
		// manual ping of selected endpoint
		if len(vis) > 0 {
			return m, m.checkCmd(m.rows[vis[m.cursor]].name)
		}
	case "R":
		// manual ping of ALL endpoints
		var cmds []tea.Cmd
		for _, n := range m.order {
			cmds = append(cmds, m.checkCmd(n))
		}
		return m, tea.Batch(cmds...)
	case "enter", "d":
		// toggle detail/expand pane for the selected endpoint
		if len(vis) > 0 {
			if m.detail && m.detailName == m.rows[vis[m.cursor]].name {
				m.detail = false
				return m, nil
			}
			m.detail = true
			return m.refreshDetail()
		}
	case "s":
		// toggle sort mode
		if m.sort == sortConfig {
			m.sort = sortWorstFirst
		} else {
			m.sort = sortConfig
		}
		m.cursor = 0
		return m.refreshDetail()
	case "f":
		// toggle problems-only filter
		if m.filter == filterAll {
			m.filter = filterProblems
		} else {
			m.filter = filterAll
		}
		m.cursor = 0
		return m.refreshDetail()
	case "/":
		// enter search-input mode
		m.searching = true
		return m, nil
	}
	return m, nil
}

// handleSearchKey processes keystrokes while in search-input mode.
func (m Model) handleSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter", "esc":
		m.searching = false
		m.cursor = 0
		return m.refreshDetail()
	case "backspace":
		if len(m.search) > 0 {
			r := []rune(m.search)
			m.search = string(r[:len(r)-1])
		}
		m.cursor = 0
		return m, nil
	case "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	default:
		if len(msg.Runes) > 0 {
			m.search += string(msg.Runes)
			m.cursor = 0
		}
		return m, nil
	}
}

// refreshDetail recomputes detail stats for the currently selected endpoint
// when the detail pane is open.
func (m Model) refreshDetail() (tea.Model, tea.Cmd) {
	if !m.detail {
		return m, nil
	}
	vis := m.visible()
	if len(vis) == 0 {
		m.detail = false
		return m, nil
	}
	if m.cursor >= len(vis) {
		m.cursor = len(vis) - 1
	}
	name := m.rows[vis[m.cursor]].name
	m.detailName = name
	if st, err := m.store.StatsFor(name, m.uptimeWindow, 5); err == nil {
		m.detailStats = st
	}
	if lat, err := m.store.LatencyHistory(name, 60); err == nil {
		m.detailLatency = lat
	}
	if aggs, err := m.store.WindowAggs(name, []struct {
		Label string
		Dur   time.Duration
	}{
		{Label: "1h", Dur: time.Hour},
		{Label: "24h", Dur: 24 * time.Hour},
	}); err == nil {
		m.detailWindows = aggs
	}
	// Edge (Cloudflare) status is cached (~30s) inside the connector, so this is
	// cheap on repeat opens. ok=false means no Edge connector is configured.
	if up, detail, ok := m.engine.EdgeStatus(m.ctx, name); ok {
		m.detailEdgeOK = true
		m.detailEdgeUp = up
		m.detailEdgeDetail = detail
	} else {
		m.detailEdgeOK = false
	}
	return m, nil
}

// visible returns indices into m.rows after applying filter, search, and sort.
func (m Model) visible() []int {
	idx := make([]int, 0, len(m.rows))
	q := strings.ToLower(m.search)
	for i, r := range m.rows {
		if m.filter == filterProblems &&
			r.last.Status != checks.StatusRed && r.last.Status != checks.StatusYellow {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(r.name), q) {
			continue
		}
		idx = append(idx, i)
	}
	if m.sort == sortWorstFirst {
		sort.SliceStable(idx, func(a, b int) bool {
			return statusRank(m.rows[idx[a]].last.Status) < statusRank(m.rows[idx[b]].last.Status)
		})
	}
	return idx
}

// applyResult updates the row for a result, then refreshes derived stats and
// optionally schedules a remote-metrics fetch.
func (m *Model) applyResult(res checks.Result) tea.Cmd {
	idx := -1
	for i := range m.rows {
		if m.rows[i].name == res.Endpoint {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	r := &m.rows[idx]
	r.last = res
	if res.Err != "" {
		m.lastErr = fmt.Sprintf("%s: %s", res.Endpoint, res.Err)
	}
	// refresh history-derived stats
	samples, err := m.store.Recent(res.Endpoint)
	if err == nil {
		r.spark, r.sparkStat = store.Sparkline(samples, m.sparkWidth())
	}
	up, n, err := m.store.Uptime(res.Endpoint, m.uptimeWindow)
	if err == nil {
		r.uptime, r.samples = up, n
	}
	return m.remoteCmd(res.Endpoint)
}

// remoteCmd fetches remote host metrics for an endpoint if it defines a
// metrics_path in config; otherwise returns nil.
func (m Model) remoteCmd(name string) tea.Cmd {
	var ep *config.Endpoint
	for i := range m.cfg.Endpoints {
		if m.cfg.Endpoints[i].Name == name {
			ep = &m.cfg.Endpoints[i]
			break
		}
	}
	if ep == nil || ep.MetricsPath == "" {
		return nil
	}
	url, headers, timeout := ep.MetricsPath, ep.Headers, ep.Timeout()
	return func() tea.Msg {
		return remoteMsg{remote: metrics.FetchRemote(m.ctx, name, url, headers, timeout)}
	}
}

// --- View ---

func (m Model) View() string {
	if m.quitting {
		return ""
	}
	if m.width == 0 {
		return "initializing…"
	}
	var b strings.Builder

	// Title bar
	title := titleStyle.Render(" SERVICE HEALTH BOARD ")
	summary := m.summaryLine()
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Center, title, "  ", summary))
	b.WriteString("\n")

	// Local metrics header bar
	if m.cfg.Settings.ShowLocalMetrics {
		b.WriteString(headerStyle.Render("host  " + m.local.Format()))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	// View-state indicator line (sort/filter/search), only when active.
	if ind := m.indicatorLine(); ind != "" {
		b.WriteString(ind)
		b.WriteString("\n")
	}

	// Table
	b.WriteString(m.renderTable())
	b.WriteString("\n")

	// Detail pane (expand)
	if m.detail {
		b.WriteString(m.renderDetail())
		b.WriteString("\n")
	}

	// Footer / help
	help := "↑/↓ move · enter detail · r ping · R all · s sort · f filter · / search · q quit"
	if m.searching {
		help = lipgloss.NewStyle().Foreground(accent).Render("search: "+m.search+"▏") + "   (enter/esc to apply)"
	} else if m.lastErr != "" {
		help = lipgloss.NewStyle().Foreground(red).Render("⚠ "+truncate(m.lastErr, m.width-2)) + "\n" + help
	}
	b.WriteString(footerStyle.Render(help))
	return b.String()
}

// indicatorLine shows active sort/filter/search state.
func (m Model) indicatorLine() string {
	var parts []string
	if m.sort == sortWorstFirst {
		parts = append(parts, lipgloss.NewStyle().Foreground(accent).Render("sort:worst-first"))
	}
	if m.filter == filterProblems {
		parts = append(parts, lipgloss.NewStyle().Foreground(yellow).Render("filter:problems"))
	}
	if m.search != "" {
		parts = append(parts, lipgloss.NewStyle().Foreground(accent).Render("search:"+m.search))
	}
	if len(parts) == 0 {
		return ""
	}
	return headerStyle.Render("▸ ") + strings.Join(parts, headerStyle.Render(" · "))
}

// renderDetail renders the expand pane for the selected endpoint.
func (m Model) renderDetail() string {
	st := m.detailStats
	name := m.detailName
	url := m.engine.TargetURL(name)

	boxW := clamp(m.width-2, 30, 120)
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(accent).
		Padding(0, 1).
		Width(boxW)

	var b strings.Builder
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231")).Render(name)
	b.WriteString(title + "  " + headerStyle.Render(truncate(url, boxW-len(name)-4)) + "\n")

	// latency percentiles
	b.WriteString(fmt.Sprintf("latency  p50 %dms · p90 %dms · p99 %dms · min %dms · max %dms\n",
		st.P50, st.P90, st.P99, st.MinMs, st.MaxMs))
	// counts
	g := lipgloss.NewStyle().Foreground(green).Render(fmt.Sprintf("%d up", st.Green))
	y := lipgloss.NewStyle().Foreground(yellow).Render(fmt.Sprintf("%d deg", st.Yellow))
	r := lipgloss.NewStyle().Foreground(red).Render(fmt.Sprintf("%d down", st.Red))
	b.WriteString(fmt.Sprintf("samples  %d (%s · %s · %s) over %s\n",
		st.Samples, g, y, r, m.uptimeWindow.String()))

	// latency mini-graph over time (oldest left -> newest right)
	if len(m.detailLatency) > 0 {
		gw := clamp(boxW-18, 10, 60)
		graph, gmax := store.LatencyGraph(m.detailLatency, gw)
		b.WriteString(fmt.Sprintf("lat/time %s %dms\n",
			lipgloss.NewStyle().Foreground(accent).Render(graph), gmax))
	}

	// windowed aggregates (1h / 24h)
	for _, w := range m.detailWindows {
		if w.Samples == 0 {
			b.WriteString(headerStyle.Render(fmt.Sprintf("%-4s     no samples yet\n", w.Label)))
			continue
		}
		upPct := w.Uptime * 100
		upStyle := lipgloss.NewStyle().Foreground(green)
		if upPct < 99 {
			upStyle = lipgloss.NewStyle().Foreground(yellow)
		}
		if upPct < 90 {
			upStyle = lipgloss.NewStyle().Foreground(red)
		}
		b.WriteString(fmt.Sprintf("%-4s     uptime %s · p50 %dms · p99 %dms · n=%d\n",
			w.Label, upStyle.Render(fmt.Sprintf("%.1f%%", upPct)), w.P50, w.P99, w.Samples))
	}

	// spec metadata + discovered paths
	if info, ok := m.engine.SpecMeta(name); ok {
		b.WriteString(headerStyle.Render(fmt.Sprintf("spec     %s %s (%s) base=%s\n",
			info.Kind, info.Version, info.Title, truncate(info.BaseURL, 40))))
		paths := m.engine.SpecPaths(name, 6)
		for _, p := range paths {
			b.WriteString(headerStyle.Render("  • "+truncate(p, boxW-6)) + "\n")
		}
	}

	// edge / tunnel status (Cloudflare), only when an Edge connector is wired
	if m.detailEdgeOK {
		var badge string
		if m.detailEdgeUp {
			badge = lipgloss.NewStyle().Foreground(green).Render("healthy")
		} else {
			badge = lipgloss.NewStyle().Foreground(red).Render("unhealthy")
		}
		b.WriteString(fmt.Sprintf("edge     %s  %s\n", badge, headerStyle.Render(truncate(m.detailEdgeDetail, boxW-20))))
	}

	// recent errors
	if len(st.RecentErrors) > 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(red).Render("recent errors") + "\n")
		for _, e := range st.RecentErrors {
			line := fmt.Sprintf("  %s  %s", e.At.Format("15:04:05"), e.Err)
			b.WriteString(truncate(line, boxW-2) + "\n")
		}
	} else {
		b.WriteString(headerStyle.Render("recent errors  none") + "\n")
	}

	return box.Render(strings.TrimRight(b.String(), "\n"))
}

func (m Model) summaryLine() string {
	var g, y, r int
	for _, row := range m.rows {
		switch row.last.Status {
		case checks.StatusGreen:
			g++
		case checks.StatusYellow:
			y++
		case checks.StatusRed:
			r++
		}
	}
	gs := lipgloss.NewStyle().Foreground(green).Render(fmt.Sprintf("● %d up", g))
	ys := lipgloss.NewStyle().Foreground(yellow).Render(fmt.Sprintf("◐ %d deg", y))
	rs := lipgloss.NewStyle().Foreground(red).Render(fmt.Sprintf("○ %d down", r))
	return strings.Join([]string{gs, ys, rs}, "  ")
}

// renderTable builds the responsive endpoint table sized to the terminal.
func (m Model) renderTable() string {
	// Column widths adapt to terminal width.
	nameW := clamp(m.width/4, 10, 28)
	statusW := 10
	latW := 7
	upW := 8
	sparkW := m.sparkWidth()

	var b strings.Builder

	// Header row
	hdr := fmt.Sprintf("%-2s %-*s %-*s %*s %*s  %s",
		"", nameW, "ENDPOINT", statusW, "STATUS", latW, "LAT", upW, "UPTIME", "HISTORY")
	b.WriteString(colHdrStyle.Render(truncate(hdr, m.width)))
	b.WriteString("\n")

	vis := m.visible()
	if len(vis) == 0 {
		b.WriteString(headerStyle.Render("  (no endpoints match current filter/search)"))
		b.WriteString("\n")
		return b.String()
	}

	for vi, ri := range vis {
		row := m.rows[ri]
		sym := lipgloss.NewStyle().Foreground(statusColor(row.last.Status)).Render(row.last.Status.Symbol())
		statusTxt := lipgloss.NewStyle().Foreground(statusColor(row.last.Status)).Render(
			fmt.Sprintf("%-*s", statusW, row.last.Status.String()))

		lat := "-"
		if row.last.Latency > 0 {
			lat = fmt.Sprintf("%dms", row.last.Latency.Milliseconds())
		}
		up := "-"
		if row.samples > 0 {
			up = fmt.Sprintf("%.1f%%", row.uptime*100)
		}
		name := truncate(row.name, nameW)

		spark := m.colorSpark(row.spark, row.sparkStat, sparkW)

		line := fmt.Sprintf("%s %-*s %s %*s %*s  %s",
			sym, nameW, name, statusTxt, latW, lat, upW, up, spark)

		// remote metrics appended if present
		if row.remote.OK && (row.remote.Load1 > 0 || row.remote.DiskUsedPct > 0) {
			line += metricStyle.Render(fmt.Sprintf("  [l%.2f d%.0f%%]", row.remote.Load1, row.remote.DiskUsedPct))
		}

		line = truncate(line, m.width)
		if vi == m.cursor {
			line = selStyle.Width(m.width).Render(line)
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// colorSpark applies per-cell color to the sparkline string.
func (m Model) colorSpark(spark string, stats []checks.Status, width int) string {
	if spark == "" {
		return strings.Repeat(" ", width)
	}
	runes := []rune(spark)
	var b strings.Builder
	for i, rn := range runes {
		if i < len(stats) {
			b.WriteString(lipgloss.NewStyle().Foreground(statusColor(stats[i])).Render(string(rn)))
		} else {
			b.WriteRune(rn)
		}
	}
	return b.String()
}

// sparkWidth computes available width for the history sparkline.
func (m Model) sparkWidth() int {
	used := 2 + clamp(m.width/4, 10, 28) + 10 + 7 + 8 + 6
	w := m.width - used
	return clamp(w, 10, m.cfg.Settings.HistorySize)
}

// --- helpers ---

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	// account for ANSI: this is a best-effort visual truncate on rune count of
	// the *plain* string is hard once styled, so we only truncate unstyled text.
	if lipgloss.Width(s) <= max {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return string(r[:max])
	}
	return string(r[:max-1]) + "…"
}

func statusRank(s checks.Status) int {
	switch s {
	case checks.StatusRed:
		return 0
	case checks.StatusYellow:
		return 1
	case checks.StatusGreen:
		return 2
	default:
		return 3
	}
}
