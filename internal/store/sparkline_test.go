package store

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pv-udpv/svchealth/internal/checks"
)

func TestSparklineWidthAndAlignment(t *testing.T) {
	samples := []Sample{
		{Status: checks.StatusGreen},
		{Status: checks.StatusRed},
		{Status: checks.StatusYellow},
	}
	str, stats := Sparkline(samples, 6)
	if n := utf8.RuneCountInString(str); n != 6 {
		t.Errorf("sparkline rune width = %d, want 6", n)
	}
	if len(stats) != 6 {
		t.Errorf("stats len = %d, want 6", len(stats))
	}
	// Right-aligned: first 3 cells are padding (Unknown), last 3 carry data.
	if stats[0] != checks.StatusUnknown || stats[2] != checks.StatusUnknown {
		t.Errorf("expected left padding to be Unknown, got %v", stats[:3])
	}
	if stats[3] != checks.StatusGreen || stats[5] != checks.StatusYellow {
		t.Errorf("data cells misaligned: %v", stats[3:])
	}
}

func TestSparklineTruncatesToWidth(t *testing.T) {
	var samples []Sample
	for i := 0; i < 50; i++ {
		samples = append(samples, Sample{Status: checks.StatusGreen})
	}
	str, _ := Sparkline(samples, 10)
	if n := utf8.RuneCountInString(str); n != 10 {
		t.Errorf("width = %d, want 10", n)
	}
	if strings.ContainsRune(str, ' ') {
		t.Error("full window should have no padding spaces")
	}
}

func TestLatencyGraphScaling(t *testing.T) {
	g, max := LatencyGraph([]int64{10, 50, 100}, 3)
	if max != 100 {
		t.Errorf("max = %d, want 100", max)
	}
	if utf8.RuneCountInString(g) != 3 {
		t.Errorf("graph width = %d, want 3", utf8.RuneCountInString(g))
	}
	runes := []rune(g)
	// Highest value maps to the tallest block.
	if runes[2] != '█' {
		t.Errorf("max value should render as full block, got %q", string(runes[2]))
	}
}

func TestLatencyGraphEmpty(t *testing.T) {
	g, max := LatencyGraph(nil, 4)
	if max != 0 {
		t.Errorf("empty max = %d, want 0", max)
	}
	if g != strings.Repeat(" ", 4) {
		t.Errorf("empty graph should be all spaces, got %q", g)
	}
}

func TestLatencyGraphZeroGap(t *testing.T) {
	// A zero in the middle should render as a blank, not a bar.
	g, _ := LatencyGraph([]int64{100, 0, 100}, 3)
	runes := []rune(g)
	if runes[1] != ' ' {
		t.Errorf("zero-latency sample should be blank, got %q", string(runes[1]))
	}
}
