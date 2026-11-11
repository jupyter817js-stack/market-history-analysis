package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"rn1-strategy-extractor/internal/polymarket"
)

func newDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// TestActivityRecursiveSplit_TriggersBeforeOffsetCap simulates a window
// that is densely full and verifies the orchestrator splits the window
// before the offset cap is breached.
func TestActivityRecursiveSplit_TriggersBeforeOffsetCap(t *testing.T) {
	// Server returns `pageSize` rows for every request as long as the
	// requested window is wider than 6h. Narrower windows return a
	// partial last page so the recursion terminates.
	const pageSize = 5
	var totalRequests atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		totalRequests.Add(1)
		startUnix, _ := strconv.ParseInt(r.URL.Query().Get("start"), 10, 64)
		endUnix, _ := strconv.ParseInt(r.URL.Query().Get("end"), 10, 64)
		dur := time.Duration(endUnix-startUnix) * time.Second
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

		rows := make([]map[string]any, 0, pageSize)
		if dur > 6*time.Hour {
			for i := 0; i < pageSize; i++ {
				rows = append(rows, map[string]any{
					"proxyWallet":     "0xabc",
					"timestamp":       startUnix + int64(offset+i),
					"type":            "TRADE",
					"side":            "BUY",
					"price":           0.5,
					"size":            1.0,
					"asset":           "a1",
					"conditionId":     "c1",
					"slug":            "m1",
					"title":           fmt.Sprintf("dense-%d", offset+i),
					"transactionHash": fmt.Sprintf("0x%d-%d", offset, i),
				})
			}
		} else {
			rows = append(rows, map[string]any{
				"proxyWallet":     "0xabc",
				"timestamp":       startUnix,
				"type":            "TRADE",
				"side":            "BUY",
				"price":           0.5,
				"size":            1.0,
				"asset":           "a1",
				"conditionId":     "c1",
				"slug":            "m1",
				"title":           "partial",
				"transactionHash": fmt.Sprintf("0xpartial-%d-%d", startUnix, offset),
			})
		}
		b, _ := json.Marshal(rows)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	client := polymarket.NewClient(polymarket.ClientOptions{
		GammaBaseURL: srv.URL,
		DataBaseURL:  srv.URL,
		HTTPTimeout:  5 * time.Second,
		GammaRPS:     1000, DataRPS: 1000, RetryMax: 1,
	})
	defer client.Close()

	fs := newFakeSink()
	loc, _ := time.LoadLocation("Asia/Seoul")
	orch := &Orchestrator{
		Client: client, Store: fs, Logger: newDiscardLogger(),
		Location:        loc,
		PageSize:        pageSize,
		MaxOffsetSafety: 10, // 2 pages then must split
		MinWindow:       time.Hour,
	}

	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC) // 24h window

	if err := orch.IngestActivity(context.Background(), AccountIdentity{Input: "0xinput", ProxyWallet: "0xabc"}, start, end); err != nil {
		t.Fatalf("IngestActivity: %v", err)
	}

	if totalRequests.Load() < 4 {
		t.Errorf("expected at least 4 requests due to splits, got %d", totalRequests.Load())
	}
	if len(fs.actions) == 0 {
		t.Error("no actions persisted")
	}
	for _, raw := range fs.raws {
		if !strings.Contains(raw.RequestURL, "offset=") {
			t.Errorf("raw url missing offset: %s", raw.RequestURL)
		}
	}
}

// TestActivityIdempotent_FingerprintDedup verifies repeated ingestion of
// the same rows is deduped at the action_fingerprint level.
func TestActivityIdempotent_FingerprintDedup(t *testing.T) {
	rows := []map[string]any{
		{"proxyWallet": "0xabc", "timestamp": 1746489600, "type": "TRADE", "side": "BUY", "price": 0.5, "size": 100, "asset": "a1", "conditionId": "c1", "slug": "m1", "title": "X", "transactionHash": "0x01"},
	}
	body, _ := json.Marshal(rows)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	client := polymarket.NewClient(polymarket.ClientOptions{
		GammaBaseURL: srv.URL, DataBaseURL: srv.URL,
		HTTPTimeout: 5 * time.Second, GammaRPS: 1000, DataRPS: 1000, RetryMax: 1,
	})
	defer client.Close()

	fs := newFakeSink()
	loc, _ := time.LoadLocation("Asia/Seoul")
	orch := &Orchestrator{Client: client, Store: fs, Logger: newDiscardLogger(), Location: loc, PageSize: 500, MaxOffsetSafety: 9500, MinWindow: time.Hour}

	identity := AccountIdentity{Input: "0xinput", ProxyWallet: "0xabc"}
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC)

	if err := orch.IngestActivity(context.Background(), identity, start, end); err != nil {
		t.Fatal(err)
	}
	if len(fs.actions) != 1 {
		t.Fatalf("first run: expected 1 action, got %d", len(fs.actions))
	}
	if err := orch.IngestActivity(context.Background(), identity, start, end); err != nil {
		t.Fatal(err)
	}
	if len(fs.actions) != 1 {
		t.Errorf("second run: expected still 1 action after dedup, got %d", len(fs.actions))
	}
}

func TestProfileIngest_StoresProxyWallet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"proxyWallet":"0xresolved","name":"RN1","createdAt":"2024-12-01T00:00:00Z"}`))
	}))
	defer srv.Close()
	client := polymarket.NewClient(polymarket.ClientOptions{
		GammaBaseURL: srv.URL, DataBaseURL: srv.URL,
		HTTPTimeout: 5 * time.Second, GammaRPS: 1000, DataRPS: 1000, RetryMax: 1,
	})
	defer client.Close()

	fs := newFakeSink()
	orch := &Orchestrator{Client: client, Store: fs, Logger: newDiscardLogger()}
	id, err := orch.IngestProfile(context.Background(), "0xinput")
	if err != nil {
		t.Fatal(err)
	}
	if id.ProxyWallet != "0xresolved" {
		t.Errorf("proxy_wallet not extracted: %s", id.ProxyWallet)
	}
	acc, ok := fs.accounts["0xinput"]
	if !ok {
		t.Fatal("account not stored")
	}
	if acc.ProxyWallet != "0xresolved" {
		t.Errorf("stored proxy: %s", acc.ProxyWallet)
	}
}

func TestDryRun_DoesNotInsertNormalized(t *testing.T) {
	rows := []map[string]any{
		{"proxyWallet": "0xabc", "timestamp": 1746489600, "type": "TRADE", "side": "BUY", "price": 0.5, "size": 100, "asset": "a1", "conditionId": "c1", "slug": "m1", "title": "X", "transactionHash": "0x01"},
	}
	body, _ := json.Marshal(rows)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	client := polymarket.NewClient(polymarket.ClientOptions{GammaBaseURL: srv.URL, DataBaseURL: srv.URL, HTTPTimeout: 5 * time.Second, GammaRPS: 1000, DataRPS: 1000, RetryMax: 1})
	defer client.Close()
	fs := newFakeSink()
	orch := &Orchestrator{Client: client, Store: fs, Logger: newDiscardLogger(), PageSize: 500, MaxOffsetSafety: 9500, MinWindow: time.Hour, DryRun: true}
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC)
	if err := orch.IngestActivity(context.Background(), AccountIdentity{Input: "0xinput", ProxyWallet: "0xabc"}, start, end); err != nil {
		t.Fatal(err)
	}
	if len(fs.actions) != 0 {
		t.Errorf("dry-run wrote actions: %d", len(fs.actions))
	}
	if len(fs.raws) != 0 {
		t.Errorf("dry-run wrote raw rows: %d", len(fs.raws))
	}
	if orch.requestCount == 0 {
		t.Error("dry-run should still have made requests")
	}
}
