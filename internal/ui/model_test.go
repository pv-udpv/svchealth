package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// keyMsg builds a tea.KeyMsg for a key name (single rune or a named key).
func keyMsg(s string) tea.Msg {
	switch s {
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

// applyKey routes a key through Update and returns the updated Model.
func applyKey(t *testing.T, m Model, s string) Model {
	t.Helper()
	m2, _ := m.Update(keyMsg(s))
	return m2.(Model)
}

func TestUpdateWindowSize(t *testing.T) {
	m, _ := newReloadHarness(t, endpointA)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	got := m2.(Model)
	if got.width != 120 || got.height != 40 {
		t.Errorf("width/height = %d/%d, want 120/40", got.width, got.height)
	}
}

func TestHandleKeyNavigationAndQuit(t *testing.T) {
	m, _ := newReloadHarness(t, endpointA) // 2 rows: a, b

	if got := applyKey(t, m, "down"); got.cursor != 1 {
		t.Errorf("cursor after down = %d, want 1", got.cursor)
	}
	// at bottom, down stays
	if got := applyKey(t, applyKey(t, m, "down"), "down"); got.cursor != 1 {
		t.Errorf("cursor should clamp at bottom")
	}
	if got := applyKey(t, applyKey(t, m, "down"), "up"); got.cursor != 0 {
		t.Errorf("cursor after up = %d, want 0", got.cursor)
	}
	if got := applyKey(t, m, "G"); got.cursor != 1 {
		t.Errorf("G cursor = %d, want 1 (last)", got.cursor)
	}
	if got := applyKey(t, applyKey(t, m, "G"), "g"); got.cursor != 0 {
		t.Errorf("g cursor = %d, want 0 (first)", got.cursor)
	}

	// quit
	q, cmd := m.Update(keyMsg("q"))
	if !q.(Model).quitting {
		t.Error("q should set quitting")
	}
	if cmd == nil {
		t.Error("q should return a quit command")
	}
}

func TestHandleKeySortFilterSearch(t *testing.T) {
	m, _ := newReloadHarness(t, endpointA)

	if got := applyKey(t, m, "s"); got.sort != sortWorstFirst {
		t.Errorf("s should toggle to worst-first, got %v", got.sort)
	}
	if got := applyKey(t, applyKey(t, m, "s"), "s"); got.sort != sortConfig {
		t.Errorf("s should toggle back to config order, got %v", got.sort)
	}

	if got := applyKey(t, m, "f"); got.filter != filterProblems {
		t.Errorf("f should toggle to problems filter, got %v", got.filter)
	}

	// search flow: / -> type -> enter
	s1 := applyKey(t, m, "/")
	if !s1.searching {
		t.Error("/ should enter search mode")
	}
	s2 := applyKey(t, s1, "a")
	if s2.search != "a" || !s2.searching {
		t.Errorf("typing in search: search=%q searching=%v", s2.search, s2.searching)
	}
	s3 := applyKey(t, s2, "backspace")
	if s3.search != "" {
		t.Errorf("backspace should clear search, got %q", s3.search)
	}
	s4 := applyKey(t, s3, "enter")
	if s4.searching {
		t.Error("enter should exit search mode")
	}
}

func TestHandleKeyDetailToggle(t *testing.T) {
	m, _ := newReloadHarness(t, endpointA)

	d1 := applyKey(t, m, "enter")
	if !d1.detail || d1.detailName != "a" {
		t.Errorf("enter should open detail for 'a': detail=%v name=%q", d1.detail, d1.detailName)
	}
	d2 := applyKey(t, d1, "enter")
	if d2.detail {
		t.Error("second enter should close the detail pane")
	}
}

func TestViewInitializing(t *testing.T) {
	m, _ := newReloadHarness(t, endpointA)
	if got := m.View(); got != "initializing…" {
		t.Errorf("zero-width View = %q, want initializing", got)
	}
}

func TestViewRenders(t *testing.T) {
	m, _ := newReloadHarness(t, endpointA)
	m.width, m.height = 120, 40
	out := m.View()
	for _, want := range []string{"SERVICE HEALTH BOARD", "a", "q quit"} {
		if !strings.Contains(out, want) {
			t.Errorf("View missing %q", want)
		}
	}
}

func TestRefreshDetailSetsName(t *testing.T) {
	m, _ := newReloadHarness(t, endpointA)
	m.detail = true
	m.cursor = 1 // endpoint "b"
	m2, _ := m.refreshDetail()
	got := m2.(Model)
	if got.detailName != "b" {
		t.Errorf("refreshDetail name = %q, want b", got.detailName)
	}
}

func TestRemoteCmdNilWithoutMetricsPath(t *testing.T) {
	m, _ := newReloadHarness(t, endpointA)
	if cmd := m.remoteCmd("a"); cmd != nil {
		t.Errorf("remoteCmd should be nil when no metrics_path, got %v", cmd)
	}
	if cmd := m.remoteCmd("missing"); cmd != nil {
		t.Errorf("remoteCmd for unknown endpoint should be nil, got %v", cmd)
	}
}
