package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/pv-udpv/svchealth/internal/checks"
)

func openTmp(t *testing.T) *Store {
	t.Helper()
	p := filepath.Join(t.TempDir(), "test.db")
	st, err := Open(p, 60)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func sample(st *Store, t *testing.T, ep string, status checks.Status, latMs int64, at time.Time, errStr string) {
	t.Helper()
	r := checks.Result{
		Endpoint:   ep,
		Status:     status,
		HTTPStatus: 200,
		Latency:    time.Duration(latMs) * time.Millisecond,
		At:         at,
		Err:        errStr,
	}
	if err := st.Insert(r); err != nil {
		t.Fatalf("Insert: %v", err)
	}
}

func TestInsertRecentOrder(t *testing.T) {
	st := openTmp(t)
	now := time.Now()
	sample(st, t, "a", checks.StatusGreen, 10, now.Add(-3*time.Second), "")
	sample(st, t, "a", checks.StatusYellow, 20, now.Add(-2*time.Second), "")
	sample(st, t, "a", checks.StatusRed, 0, now.Add(-1*time.Second), "boom")

	recent, err := st.Recent("a")
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(recent) != 3 {
		t.Fatalf("got %d samples, want 3", len(recent))
	}
	// Oldest-first.
	if recent[0].Status != checks.StatusGreen || recent[2].Status != checks.StatusRed {
		t.Errorf("Recent not oldest-first: %+v", recent)
	}
}

func TestUptime(t *testing.T) {
	st := openTmp(t)
	now := time.Now()
	sample(st, t, "a", checks.StatusGreen, 10, now, "")
	sample(st, t, "a", checks.StatusYellow, 10, now, "")
	sample(st, t, "a", checks.StatusRed, 10, now, "")
	sample(st, t, "a", checks.StatusRed, 10, now, "")

	up, n, err := st.Uptime("a", time.Hour)
	if err != nil {
		t.Fatalf("Uptime: %v", err)
	}
	if n != 4 {
		t.Errorf("n = %d, want 4", n)
	}
	// 2 healthy (green+yellow) of 4 = 0.5.
	if up != 0.5 {
		t.Errorf("uptime = %v, want 0.5", up)
	}
}

func TestUptimeNoData(t *testing.T) {
	st := openTmp(t)
	up, n, err := st.Uptime("missing", time.Hour)
	if err != nil {
		t.Fatalf("Uptime: %v", err)
	}
	if up != 0 || n != 0 {
		t.Errorf("no-data uptime = (%v,%d), want (0,0)", up, n)
	}
}

func TestLatest(t *testing.T) {
	st := openTmp(t)
	if _, ok, err := st.Latest("nope"); err != nil || ok {
		t.Errorf("Latest(missing) = ok=%v err=%v, want ok=false", ok, err)
	}
	now := time.Now()
	sample(st, t, "a", checks.StatusGreen, 10, now.Add(-time.Minute), "")
	sample(st, t, "a", checks.StatusRed, 99, now, "down")
	sm, ok, err := st.Latest("a")
	if err != nil || !ok {
		t.Fatalf("Latest: ok=%v err=%v", ok, err)
	}
	if sm.Status != checks.StatusRed || sm.LatencyMs != 99 || sm.Err != "down" {
		t.Errorf("Latest returned wrong row: %+v", sm)
	}
}

func TestPrune(t *testing.T) {
	st := openTmp(t)
	now := time.Now()
	sample(st, t, "a", checks.StatusGreen, 10, now.Add(-48*time.Hour), "")
	sample(st, t, "a", checks.StatusGreen, 10, now, "")
	if err := st.Prune(24 * time.Hour); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	recent, _ := st.Recent("a")
	if len(recent) != 1 {
		t.Errorf("after prune got %d samples, want 1", len(recent))
	}
}

func TestStatsForPercentiles(t *testing.T) {
	st := openTmp(t)
	now := time.Now()
	for _, lat := range []int64{10, 20, 30, 40, 50} {
		sample(st, t, "a", checks.StatusGreen, lat, now, "")
	}
	sample(st, t, "a", checks.StatusRed, 0, now, "err1")

	s, err := st.StatsFor("a", time.Hour, 5)
	if err != nil {
		t.Fatalf("StatsFor: %v", err)
	}
	if s.Samples != 6 {
		t.Errorf("Samples = %d, want 6", s.Samples)
	}
	if s.Green != 5 || s.Red != 1 {
		t.Errorf("counts green=%d red=%d, want 5/1", s.Green, s.Red)
	}
	if s.MinMs != 10 || s.MaxMs != 50 {
		t.Errorf("min/max = %d/%d, want 10/50", s.MinMs, s.MaxMs)
	}
	if s.P50 != 30 {
		t.Errorf("P50 = %d, want 30", s.P50)
	}
	if len(s.RecentErrors) != 1 || s.RecentErrors[0].Err != "err1" {
		t.Errorf("RecentErrors = %+v, want one err1", s.RecentErrors)
	}
}

func TestWindowAggs(t *testing.T) {
	st := openTmp(t)
	now := time.Now()
	// Two in the last hour, one older (outside 1h, inside 24h).
	sample(st, t, "a", checks.StatusGreen, 10, now, "")
	sample(st, t, "a", checks.StatusRed, 20, now, "")
	sample(st, t, "a", checks.StatusGreen, 30, now.Add(-2*time.Hour), "")

	aggs, err := st.WindowAggs("a", []struct {
		Label string
		Dur   time.Duration
	}{
		{"1h", time.Hour},
		{"24h", 24 * time.Hour},
	})
	if err != nil {
		t.Fatalf("WindowAggs: %v", err)
	}
	if len(aggs) != 2 {
		t.Fatalf("got %d aggs, want 2", len(aggs))
	}
	if aggs[0].Label != "1h" || aggs[0].Samples != 2 || aggs[0].Uptime != 0.5 {
		t.Errorf("1h agg wrong: %+v", aggs[0])
	}
	if aggs[1].Label != "24h" || aggs[1].Samples != 3 {
		t.Errorf("24h agg wrong: %+v", aggs[1])
	}
}

func TestLatencyHistoryOldestFirst(t *testing.T) {
	st := openTmp(t)
	now := time.Now()
	sample(st, t, "a", checks.StatusGreen, 10, now.Add(-3*time.Second), "")
	sample(st, t, "a", checks.StatusGreen, 20, now.Add(-2*time.Second), "")
	sample(st, t, "a", checks.StatusGreen, 30, now.Add(-1*time.Second), "")

	hist, err := st.LatencyHistory("a", 60)
	if err != nil {
		t.Fatalf("LatencyHistory: %v", err)
	}
	want := []int64{10, 20, 30}
	if len(hist) != 3 {
		t.Fatalf("got %d, want 3", len(hist))
	}
	for i := range want {
		if hist[i] != want[i] {
			t.Errorf("hist[%d] = %d, want %d (should be oldest-first)", i, hist[i], want[i])
		}
	}
}

func TestPercentile(t *testing.T) {
	sorted := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	cases := []struct {
		p    float64
		want int64
	}{
		{0, 1},
		{1, 10},
		{0.5, 6}, // nearest-rank: int(0.5*9+0.5)=5 -> sorted[5]=6
		{0.9, 9}, // nearest-rank: int(0.9*9+0.5)=8 -> sorted[8]=9
	}
	for _, tc := range cases {
		if got := percentile(sorted, tc.p); got != tc.want {
			t.Errorf("percentile(p=%v) = %d, want %d", tc.p, got, tc.want)
		}
	}
	if got := percentile(nil, 0.5); got != 0 {
		t.Errorf("percentile(empty) = %d, want 0", got)
	}
}
