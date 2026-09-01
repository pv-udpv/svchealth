package store

import (
	"testing"
	"time"

	"github.com/pv-udpv/svchealth/internal/checks"
)

func TestExportAllOldestFirst(t *testing.T) {
	st := openTmp(t)
	now := time.Now()
	sample(st, t, "a", checks.StatusGreen, 10, now.Add(-time.Hour), "")
	sample(st, t, "b", checks.StatusGreen, 30, now, "")
	sample(st, t, "a", checks.StatusRed, 20, now, "boom")

	all, err := st.Export("", 0)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("got %d, want 3", len(all))
	}
	// oldest-first: the -1h sample of "a" is first.
	if all[0].Endpoint != "a" || all[0].Status != checks.StatusGreen || all[0].LatencyMs != 10 {
		t.Errorf("first sample wrong: %+v", all[0])
	}
	if all[2].Status != checks.StatusRed || all[2].Err != "boom" {
		t.Errorf("last sample wrong: %+v", all[2])
	}
}

func TestExportEndpointFilter(t *testing.T) {
	st := openTmp(t)
	now := time.Now()
	sample(st, t, "a", checks.StatusGreen, 10, now, "")
	sample(st, t, "b", checks.StatusGreen, 30, now, "")

	got, err := st.Export("a", 0)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(got) != 1 || got[0].Endpoint != "a" {
		t.Errorf("endpoint filter failed: %+v", got)
	}
}

func TestExportSinceWindow(t *testing.T) {
	st := openTmp(t)
	now := time.Now()
	sample(st, t, "a", checks.StatusGreen, 10, now.Add(-time.Hour), "")
	sample(st, t, "a", checks.StatusRed, 20, now, "")

	got, err := st.Export("a", 30*time.Minute)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(got) != 1 || got[0].LatencyMs != 20 {
		t.Errorf("since-window filter failed: %+v", got)
	}
}
