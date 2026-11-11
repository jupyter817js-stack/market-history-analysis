package polymarket

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGetBook_ParsesLevelsAndComputesTopDepth(t *testing.T) {
	body := []byte(`{
		"bids":[{"price":"0.52","size":"100"},{"price":"0.51","size":"200"},{"price":"0.50","size":"300"}],
		"asks":[{"price":"0.54","size":"150"},{"price":"0.55","size":"220"},{"price":"0.56","size":"280"}]
	}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "token_id=tok-1") {
			t.Errorf("expected token_id query, got %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	c := NewClient(ClientOptions{ClobBaseURL: srv.URL, HTTPTimeout: 5e9, ClobRPS: 1000, RetryMax: 1})
	defer c.Close()

	book, err := c.GetBook(context.Background(), "tok-1")
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	bb, ok := book.BestBid()
	if !ok || bb.Price != 0.52 {
		t.Errorf("BestBid = %+v ok=%v, want 0.52", bb, ok)
	}
	ba, ok := book.BestAsk()
	if !ok || ba.Price != 0.54 {
		t.Errorf("BestAsk = %+v ok=%v, want 0.54", ba, ok)
	}
	if d := book.TopNDepth("bid", 3); d != 600 {
		t.Errorf("TopNDepth(bid,3) = %v, want 600", d)
	}
	if d := book.TopNDepth("ask", 3); d != 650 {
		t.Errorf("TopNDepth(ask,3) = %v, want 650", d)
	}
}

func TestGetBook_EmptyBookOk(t *testing.T) {
	body := []byte(`{"bids":[],"asks":[]}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	c := NewClient(ClientOptions{ClobBaseURL: srv.URL, HTTPTimeout: 5e9, ClobRPS: 1000, RetryMax: 1})
	defer c.Close()

	book, err := c.GetBook(context.Background(), "tok-x")
	if err != nil {
		t.Fatalf("GetBook empty: %v", err)
	}
	if _, ok := book.BestBid(); ok {
		t.Errorf("expected no best bid for empty book")
	}
	if _, ok := book.BestAsk(); ok {
		t.Errorf("expected no best ask for empty book")
	}
}
