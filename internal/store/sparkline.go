package store

import "github.com/pv-udpv/svchealth/internal/checks"

// statusBlocks maps a status to a colored-friendly block glyph for sparklines.
// The TUI applies color; these glyphs encode height/intensity by status.
var statusBlocks = map[checks.Status]rune{
	checks.StatusGreen:   '█',
	checks.StatusYellow:  '▄',
	checks.StatusRed:     '▁',
	checks.StatusUnknown: ' ',
}

// Sparkline renders recent samples as a status-encoded block string of width n
// (right-aligned, most recent on the right). Returns the raw string plus a
// parallel slice of statuses so the caller can colorize per-cell.
func Sparkline(samples []Sample, width int) (string, []checks.Status) {
	if width <= 0 {
		width = 20
	}
	// Take the last `width` samples.
	start := 0
	if len(samples) > width {
		start = len(samples) - width
	}
	view := samples[start:]
	runes := make([]rune, 0, width)
	stats := make([]checks.Status, 0, width)
	// left-pad with blanks so it stays right-aligned at fixed width
	for i := 0; i < width-len(view); i++ {
		runes = append(runes, ' ')
		stats = append(stats, checks.StatusUnknown)
	}
	for _, s := range view {
		g, ok := statusBlocks[s.Status]
		if !ok {
			g = ' '
		}
		runes = append(runes, g)
		stats = append(stats, s.Status)
	}
	return string(runes), stats
}

// latencyLevels are the 8 vertical block glyphs from low to high.
var latencyLevels = []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// LatencyGraph renders latency values (ms) as a vertical bar mini-graph of the
// given width, oldest-left / newest-right, scaled to the window max. It also
// returns the max value used for scaling so the caller can label the axis.
// Empty input yields a blank graph and max 0.
func LatencyGraph(latencies []int64, width int) (string, int64) {
	if width <= 0 {
		width = 30
	}
	start := 0
	if len(latencies) > width {
		start = len(latencies) - width
	}
	view := latencies[start:]

	var max int64
	for _, v := range view {
		if v > max {
			max = v
		}
	}

	runes := make([]rune, 0, width)
	// left-pad so the graph stays right-aligned at fixed width
	for i := 0; i < width-len(view); i++ {
		runes = append(runes, ' ')
	}
	for _, v := range view {
		if v <= 0 || max <= 0 {
			runes = append(runes, ' ')
			continue
		}
		// Map v into 1..8 levels (always show at least the lowest bar for >0).
		level := int((v*int64(len(latencyLevels)-1) + max - 1) / max)
		if level < 0 {
			level = 0
		}
		if level >= len(latencyLevels) {
			level = len(latencyLevels) - 1
		}
		runes = append(runes, latencyLevels[level])
	}
	return string(runes), max
}
