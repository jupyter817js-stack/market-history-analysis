package polymarket

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestGetActivity_SinglePage(t *testing.T) {
	body := loadFixture(t, "activity_single_page.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("user") == "" {
			t.Errorf("user missing")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	c := newTestClient(t, srv.URL, srv.URL)
	defer c.Close()

	res, err := c.GetActivity(context.Background(), ActivityRequest{User: "0xabc"})
	if err != nil {
		t.Fatalf("GetActivity: %v", err)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(res.Rows))
	}
	if res.Rows[0]["type"] != "TRADE" {
		t.Errorf("expected first type=TRADE")
	}
}

func TestGetActivity_OffsetPagination(t *testing.T) {
	body1 := loadFixture(t, "activity_multi_page_p1.json")
	body2 := loadFixture(t, "activity_multi_page_p2.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		if offset == 0 {
			_, _ = w.Write(body1)
		} else {
			_, _ = w.Write(body2)
		}
	}))
	defer srv.Close()
	c := newTestClient(t, srv.URL, srv.URL)
	defer c.Close()

	res, err := c.GetActivity(context.Background(), ActivityRequest{User: "0xabc", Offset: 0, Limit: 500})
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("page1 rows: %d", len(res.Rows))
	}
	res2, err := c.GetActivity(context.Background(), ActivityRequest{User: "0xabc", Offset: 500, Limit: 500})
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(res2.Rows) != 1 {
		t.Fatalf("page2 rows: %d", len(res2.Rows))
	}
}
