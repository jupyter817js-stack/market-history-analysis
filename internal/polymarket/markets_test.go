package polymarket

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGetMarketsKeyset_ByConditionIDs(t *testing.T) {
	body := loadFixture(t, "markets_keyset_by_condition_ids.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "condition_ids=") {
			t.Errorf("expected condition_ids query: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	c := newTestClient(t, srv.URL, srv.URL)
	defer c.Close()

	res, err := c.GetMarketsKeyset(context.Background(), MarketsKeysetRequest{
		ConditionIDs: []string{"0xcond_heroic_series", "0xcond_heroic_map1"},
	})
	if err != nil {
		t.Fatalf("GetMarketsKeyset: %v", err)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(res.Rows))
	}
	if got, _ := res.Rows[0]["conditionId"].(string); got != "0xcond_heroic_series" {
		t.Errorf("conditionId mismatch: %v", res.Rows[0]["conditionId"])
	}
}

func TestGetEventsKeyset_BySlug(t *testing.T) {
	body := loadFixture(t, "events_keyset_by_slug.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "slug=") {
			t.Errorf("expected slug query: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	c := newTestClient(t, srv.URL, srv.URL)
	defer c.Close()

	res, err := c.GetEventsKeyset(context.Background(), EventsKeysetRequest{Slugs: []string{"orphan-event-slug"}})
	if err != nil {
		t.Fatalf("GetEventsKeyset: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(res.Rows))
	}
}
