// Package metrics collects local host metrics (load average, disk usage) and
// parses optional remote metrics payloads exposed by monitored endpoints.
package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Local is a snapshot of the host running the TUI.
type Local struct {
	Load1, Load5, Load15 float64
	DiskUsedPct          float64
	DiskUsedGiB          float64
	DiskTotalGiB         float64
	At                   time.Time
}

// Remote holds metrics parsed from an endpoint's metrics payload.
type Remote struct {
	Endpoint    string
	Load1       float64
	DiskUsedPct float64
	OK          bool
}

// ReadLocal gathers local load average and disk usage for the given path.
func ReadLocal(diskPath string) Local {
	l := Local{At: time.Now()}
	l.Load1, l.Load5, l.Load15 = readLoadAvg()
	if diskPath == "" {
		diskPath = "/"
	}
	l.DiskUsedPct, l.DiskUsedGiB, l.DiskTotalGiB = readDisk(diskPath)
	return l
}

// readLoadAvg parses /proc/loadavg (Linux). On unsupported platforms returns 0s.
func readLoadAvg() (l1, l5, l15 float64) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0
	}
	f := strings.Fields(string(b))
	if len(f) < 3 {
		return 0, 0, 0
	}
	l1, _ = strconv.ParseFloat(f[0], 64)
	l5, _ = strconv.ParseFloat(f[1], 64)
	l15, _ = strconv.ParseFloat(f[2], 64)
	return l1, l5, l15
}

// readDisk computes filesystem usage for the volume containing path. The
// implementation is platform-specific (see disk_unix.go / disk_windows.go).

// FetchRemote pulls a JSON metrics payload from url and extracts load/disk.
// It is tolerant of several common shapes (flat keys or nested objects).
func FetchRemote(ctx context.Context, endpoint, url string, headers map[string]string, timeout time.Duration) Remote {
	r := Remote{Endpoint: endpoint}
	if url == "" {
		return r
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
	if err != nil {
		return r
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return r
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return r
	}
	var doc map[string]any
	if json.Unmarshal(body, &doc) != nil {
		return r
	}
	r.Load1 = firstFloat(doc, "load1", "load_1", "load", "loadavg")
	r.DiskUsedPct = firstFloat(doc, "disk_used_pct", "diskUsedPct", "disk_pct", "disk")
	r.OK = true
	return r
}

// firstFloat searches doc for the first matching key (top-level) and coerces it.
func firstFloat(doc map[string]any, keys ...string) float64 {
	for _, k := range keys {
		if v, ok := doc[k]; ok {
			switch n := v.(type) {
			case float64:
				return n
			case string:
				if f, err := strconv.ParseFloat(n, 64); err == nil {
					return f
				}
			}
		}
	}
	return 0
}

// Format renders a compact one-line local summary.
func (l Local) Format() string {
	return fmt.Sprintf("load %.2f %.2f %.2f · disk %.0f%% (%.1f/%.1fG)",
		l.Load1, l.Load5, l.Load15, l.DiskUsedPct, l.DiskUsedGiB, l.DiskTotalGiB)
}
