package polymarket

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestGetPositions_SizeThresholdZero(t *testing.T) {
	body := loadFixture(t, "positions_current_page.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("sizeThreshold"); got != "0" {
			t.Errorf("expected sizeThreshold=0, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	c := newTestClient(t, srv.URL, srv.URL)
	defer c.Close()

	zero := 0.0
	res, err := c.GetPositions(context.Background(), PositionsRequest{User: "0xabc", SizeThreshold: &zero})
	if err != nil {
		t.Fatalf("GetPositions: %v", err)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(res.Rows))
	}
}

func TestGetClosedPositions_TimestampSort(t *testing.T) {
	body := loadFixture(t, "closed_positions_multi_page.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("sortBy"); got != "TIMESTAMP" {
			t.Errorf("expected sortBy=TIMESTAMP, got %q", got)
		}
		if lim, _ := strconv.Atoi(r.URL.Query().Get("limit")); lim != 50 {
			t.Errorf("expected limit=50, got %d", lim)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	c := newTestClient(t, srv.URL, srv.URL)
	defer c.Close()

	res, err := c.GetClosedPositions(context.Background(), ClosedPositionsRequest{User: "0xabc"})
	if err != nil {
		t.Fatalf("GetClosedPositions: %v", err)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(res.Rows))
	}
}
