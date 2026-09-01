package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/pv-udpv/svchealth/internal/checks"
	"github.com/pv-udpv/svchealth/internal/config"
)

// mkRow builds a row with the given name, group, status and latency.
func mkRow(name, group string, status checks.Status, latMs int64) row {
	return row{
		name:  name,
		group: group,
		last: checks.Result{
			Endpoint:   name,
			Status:     status,
			HTTPStatus: 200,
			Latency:    time.Duration(latMs) * time.Millisecond,
		},
	}
}

func namesOf(rows []row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.name
	}
	return out
}

// testModel assembles a Model for pure rendering tests (no engine/store).
func testModel(rows []row, width int) Model {
	return Model{
		rows:         rows,
		order:        namesOf(rows),
		width:        width,
		cfg:          &config.Config{Settings: config.Settings{HistorySize: 60}},
		uptimeWindow: time.Hour,
	}
}

func TestClamp(t *testing.T) {
	cases := []struct{ v, lo, hi, want int }{
		{5, 0, 10, 5},
		{-1, 0, 10, 0},
		{20, 0, 10, 10},
	}
	for _, c := range cases {
		if got := clamp(c.v, c.lo, c.hi); got != c.want {
			t.Errorf("clamp(%d,%d,%d) = %d, want %d", c.v, c.lo, c.hi, got, c.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("short string changed: %q", got)
	}
	if got := truncate("hello", 5); got != "hello" {
		t.Errorf("exact-width string changed: %q", got)
	}
	if got := truncate("world", 4); got != "wor…" {
		t.Errorf("truncation = %q, want %q", got, "wor…")
	}
	if got := truncate("x", 0); got != "" {
		t.Errorf("max<=0 should be empty, got %q", got)
	}
	if got := truncate("xy", 1); got != "x" {
		t.Errorf("max=1 should be single rune, got %q", got)
	}
}

func TestPadRight(t *testing.T) {
	if got := padRight("ab", 4); got != "ab  " {
		t.Errorf("padRight = %q, want %q", got, "ab  ")
	}
	if got := padRight("abcd", 4); got != "abcd" {
		t.Errorf("padRight(equal) = %q, want unchanged", got)
	}
	if got := padRight("abcdef", 4); got != "abcdef" {
		t.Errorf("padRight(longer) should be unchanged, got %q", got)
	}
	// ANSI-styled text has visual width 2 but raw length > 2.
	styled := lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Render("xy")
	padded := padRight(styled, 4)
	if lipgloss.Width(padded) != 4 {
		t.Errorf("padRight on styled text visual width = %d, want 4", lipgloss.Width(padded))
	}
}

func TestStatusRank(t *testing.T) {
	if statusRank(checks.StatusRed) >= statusRank(checks.StatusYellow) ||
		statusRank(checks.StatusYellow) >= statusRank(checks.StatusGreen) {
		t.Error("statusRank should order red < yellow < green")
	}
	if statusRank(checks.StatusUnknown) < statusRank(checks.StatusGreen) {
		t.Error("unknown should rank after green")
	}
}

func TestVisibleFilterAndSearch(t *testing.T) {
	rows := []row{
		mkRow("api", "", checks.StatusRed, 10),
		mkRow("db", "", checks.StatusGreen, 20),
		mkRow("cache", "", checks.StatusYellow, 30),
	}
	m := testModel(rows, 100)

	// problems-only filter drops green.
	m.filter = filterProblems
	vis := m.visible()
	if len(vis) != 2 {
		t.Fatalf("problems filter len = %d, want 2", len(vis))
	}

	// search narrows by name (case-insensitive), combined with filter.
	m.filter = filterAll
	m.search = "CA"
	vis = m.visible()
	if len(vis) != 1 || rows[vis[0]].name != "cache" {
		t.Errorf("search 'CA' = %v, want only cache", vis)
	}
}

func TestVisibleWorstFirstSort(t *testing.T) {
	rows := []row{
		mkRow("green", "", checks.StatusGreen, 10),
		mkRow("red", "", checks.StatusRed, 10),
		mkRow("yellow", "", checks.StatusYellow, 10),
	}
	m := testModel(rows, 100)
	m.sort = sortWorstFirst
	vis := m.visible()
	want := []string{"red", "yellow", "green"}
	if len(vis) != 3 {
		t.Fatalf("len = %d, want 3", len(vis))
	}
	for i, w := range want {
		if rows[vis[i]].name != w {
			t.Errorf("visible[%d] = %s, want %s", i, rows[vis[i]].name, w)
		}
	}
}

func TestRenderTableEmpty(t *testing.T) {
	m := testModel(nil, 100)
	out := m.renderTable()
	if !strings.Contains(out, "no endpoints match") {
		t.Errorf("empty table = %q, want no-match message", out)
	}
}

func TestRenderSingle(t *testing.T) {
	rows := []row{
		mkRow("api", "", checks.StatusGreen, 42),
		mkRow("db", "", checks.StatusRed, 12),
	}
	m := testModel(rows, 100)
	out := m.renderTable()
	for _, want := range []string{"ENDPOINT", "STATUS", "LAT", "UPTIME", "HISTORY", "api", "db", "42ms"} {
		if !strings.Contains(out, want) {
			t.Errorf("single layout missing %q:\n%s", want, out)
		}
	}
	// default sort + no groups + <16 rows => single column, one header.
	if strings.Count(out, "ENDPOINT") != 1 {
		t.Errorf("single layout should have one header, got %d 'ENDPOINT'", strings.Count(out, "ENDPOINT"))
	}
}

func TestRenderGrouped(t *testing.T) {
	rows := []row{
		mkRow("a", "core", checks.StatusGreen, 10),
		mkRow("b", "core", checks.StatusYellow, 20),
		mkRow("c", "edge", checks.StatusGreen, 5),
	}
	m := testModel(rows, 100)
	out := m.renderTable()
	for _, want := range []string{"core", "edge"} {
		if !strings.Contains(out, want) {
			t.Errorf("grouped layout missing group %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "▸") {
		t.Errorf("grouped layout missing group marker '▸':\n%s", out)
	}
}

func TestRenderColumns(t *testing.T) {
	var rows []row
	for i := 0; i < 16; i++ {
		rows = append(rows, mkRow("ep-"+string(rune('a'+i)), "", checks.StatusGreen, 10))
	}
	m := testModel(rows, 120)
	out := m.renderTable()
	// two vertical columns => two "ENDPOINT" headers.
	if strings.Count(out, "ENDPOINT") != 2 {
		t.Errorf("columns layout should have two headers, got %d:\n%s", strings.Count(out, "ENDPOINT"), out)
	}
	// both the first and last endpoints must appear (left and right columns).
	if !strings.Contains(out, "ep-a") || !strings.Contains(out, "ep-p") {
		t.Errorf("columns layout missing first/last endpoint:\n%s", out)
	}
}

func TestRowCellNoSparkline(t *testing.T) {
	m := testModel([]row{mkRow("my-service", "", checks.StatusGreen, 42)}, 120)
	cell := m.rowCell(0, 40)
	if !strings.Contains(cell, "my-service") || !strings.Contains(cell, "UP") || !strings.Contains(cell, "42ms") {
		t.Errorf("rowCell = %q, want name+status+latency", cell)
	}
}

func TestSummaryLine(t *testing.T) {
	rows := []row{
		mkRow("a", "", checks.StatusGreen, 0),
		mkRow("b", "", checks.StatusGreen, 0),
		mkRow("c", "", checks.StatusYellow, 0),
		mkRow("d", "", checks.StatusRed, 0),
	}
	m := testModel(rows, 100)
	out := m.summaryLine()
	if !strings.Contains(out, "2 up") || !strings.Contains(out, "1 deg") || !strings.Contains(out, "1 down") {
		t.Errorf("summaryLine = %q, want 2 up / 1 deg / 1 down", out)
	}
}

func TestIndicatorLine(t *testing.T) {
	m := testModel(nil, 100)
	if m.indicatorLine() != "" {
		t.Errorf("default indicatorLine should be empty, got %q", m.indicatorLine())
	}
	m.sort = sortWorstFirst
	m.filter = filterProblems
	m.search = "x"
	out := m.indicatorLine()
	for _, want := range []string{"worst-first", "problems", "x"} {
		if !strings.Contains(out, want) {
			t.Errorf("indicatorLine missing %q: %q", want, out)
		}
	}
}

func TestColorSparkEmpty(t *testing.T) {
	m := testModel(nil, 100)
	got := m.colorSpark("", nil, 4)
	if got != "    " {
		t.Errorf("empty spark should be 4 spaces, got %q", got)
	}
}

func TestColorSparkColored(t *testing.T) {
	m := testModel(nil, 100)
	spark := "▁▂▃"
	stats := []checks.Status{checks.StatusGreen, checks.StatusYellow, checks.StatusRed}
	got := m.colorSpark(spark, stats, 3)
	// Visual width is preserved whether or not the environment applies ANSI
	// color (lipgloss strips color outside a TTY, so we assert on runes + width,
	// not on raw ANSI bytes).
	if lipgloss.Width(got) != 3 {
		t.Errorf("colored spark visual width = %d, want 3", lipgloss.Width(got))
	}
	for _, r := range []string{"▁", "▂", "▃"} {
		if !strings.Contains(got, r) {
			t.Errorf("colored spark lost rune %q: %q", r, got)
		}
	}
}
