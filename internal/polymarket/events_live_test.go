package polymarket

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchActiveEvents_ParamsAndDecoding(t *testing.T) {
	body := []byte(`[{"id":"1","slug":"nfl-x","markets":[]},{"id":"2","slug":"nba-y","markets":[]}]`)
	var seenQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	c := newTestClient(t, srv.URL, srv.URL)
	defer c.Close()

	res, err := c.FetchActiveEvents(context.Background(), FetchActiveEventsRequest{
		StartUnix: 1715472000,
		EndUnix:   1715990400,
		Limit:     50,
	})
	if err != nil {
		t.Fatalf("FetchActiveEvents: %v", err)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(res.Rows))
	}
	for _, must := range []string{"active=true", "closed=false", "limit=50", "end_date_min=", "end_date_max="} {
		if !strings.Contains(seenQuery, must) {
			t.Errorf("query missing %q: got %s", must, seenQuery)
		}
	}
}
