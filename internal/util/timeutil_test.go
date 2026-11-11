package util

import (
	"testing"
	"time"
)

func TestMonthWindows_SpansMonths(t *testing.T) {
	start := time.Date(2024, 12, 15, 0, 0, 0, 0, time.UTC)
	end := time.Date(2025, 2, 10, 0, 0, 0, 0, time.UTC)
	ws := MonthWindows(start, end)
	if len(ws) != 3 {
		t.Fatalf("expected 3 windows, got %d", len(ws))
	}
	if !ws[0].Start.Equal(start) {
		t.Errorf("first start mismatch: %v", ws[0].Start)
	}
	if !ws[len(ws)-1].End.Equal(end) {
		t.Errorf("last end mismatch: %v", ws[len(ws)-1].End)
	}
}

func TestWindowSplit_HalvesDuration(t *testing.T) {
	w := TimeWindow{
		Start: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC),
	}
	l, r := w.Split()
	if l.Duration() != 12*time.Hour {
		t.Errorf("left duration: %v", l.Duration())
	}
	if r.Duration() != 12*time.Hour {
		t.Errorf("right duration: %v", r.Duration())
	}
	if !l.End.Equal(r.Start) {
		t.Errorf("split not contiguous: %v vs %v", l.End, r.Start)
	}
}
