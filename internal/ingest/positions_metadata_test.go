package ingest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rn1-strategy-extractor/internal/polymarket"
	"rn1-strategy-extractor/internal/store"
)

func loadFixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	wd, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			t.Fatal("repo root not found")
		}
		wd = parent
	}
	b, err := os.ReadFile(filepath.Join(wd, "testdata", "fixtures", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPositions_RespectsSizeThresholdZero(t *testing.T) {
	body := loadFixtureBytes(t, "positions_current_page.json")
	var seenThreshold string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenThreshold = r.URL.Query().Get("sizeThreshold")
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	client := polymarket.NewClient(polymarket.ClientOptions{GammaBaseURL: srv.URL, DataBaseURL: srv.URL, HTTPTimeout: 5 * time.Second, GammaRPS: 1000, DataRPS: 1000, RetryMax: 1})
	defer client.Close()
	fs := newFakeSink()
	orch := &Orchestrator{Client: client, Store: fs, Logger: newDiscardLogger()}
	if err := orch.IngestPositions(context.Background(), AccountIdentity{Input: "0xinput", ProxyWallet: "0xabc"}); err != nil {
		t.Fatal(err)
	}
	if seenThreshold != "0" {
		t.Errorf("expected sizeThreshold=0, got %q", seenThreshold)
	}
	if len(fs.curPos) != 2 {
		t.Errorf("expected 2 stored positions, got %d", len(fs.curPos))
	}
}

func TestClosedPositions_TimestampSort(t *testing.T) {
	body := loadFixtureBytes(t, "closed_positions_multi_page.json")
	var seenSort string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenSort = r.URL.Query().Get("sortBy")
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	client := polymarket.NewClient(polymarket.ClientOptions{GammaBaseURL: srv.URL, DataBaseURL: srv.URL, HTTPTimeout: 5 * time.Second, GammaRPS: 1000, DataRPS: 1000, RetryMax: 1})
	defer client.Close()
	fs := newFakeSink()
	orch := &Orchestrator{Client: client, Store: fs, Logger: newDiscardLogger()}
	if err := orch.IngestClosedPositions(context.Background(), AccountIdentity{Input: "0xinput", ProxyWallet: "0xabc"}); err != nil {
		t.Fatal(err)
	}
	if seenSort != "TIMESTAMP" {
		t.Errorf("expected sortBy=TIMESTAMP, got %q", seenSort)
	}
	if len(fs.closedPos) != 2 {
		t.Errorf("expected 2 stored closed positions, got %d", len(fs.closedPos))
	}
}

func TestMetadata_EnrichesViaEventMultiSlug(t *testing.T) {
	bodyEvents := loadFixtureBytes(t, "events_by_multi_slug.json")
	var seenSlugs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/events") {
			http.NotFound(w, r)
			return
		}
		seenSlugs = append(seenSlugs, r.URL.Query()["slug"]...)
		_, _ = w.Write(bodyEvents)
	}))
	defer srv.Close()
	client := polymarket.NewClient(polymarket.ClientOptions{GammaBaseURL: srv.URL, DataBaseURL: srv.URL, HTTPTimeout: 5 * time.Second, GammaRPS: 1000, DataRPS: 1000, RetryMax: 1})
	defer client.Close()

	fs := newFakeSink()
	fs.actions["fp-seed-1"] = store.ActionRow{
		Fingerprint: "fp-seed-1",
		ActionType:  "TRADE",
		ConditionID: "0xcond_heroic_series",
		EventSlug:   "counter-strike-aurora-vs-heroic",
	}

	orch := &Orchestrator{Client: client, Store: fs, Logger: newDiscardLogger()}
	if err := orch.IngestMetadata(context.Background(), AccountIdentity{Input: "0xinput", ProxyWallet: "0xabc"}); err != nil {
		t.Fatal(err)
	}
	if len(seenSlugs) == 0 {
		t.Fatal("server never saw a slug query")
	}
	if seenSlugs[0] != "counter-strike-aurora-vs-heroic" {
		t.Errorf("unexpected slug query: %v", seenSlugs)
	}
	if _, ok := fs.markets["0xcond_heroic_series"]; !ok {
		t.Errorf("expected market enrichment for 0xcond_heroic_series; have: %v", keysOf(fs.markets))
	}
	if _, ok := fs.markets["0xcond_heroic_map1"]; !ok {
		t.Errorf("expected enrichment of map1 market too; have: %v", keysOf(fs.markets))
	}
	if got := fs.markets["0xcond_heroic_series"].EventID; got != "ev-100" {
		t.Errorf("expected event_id ev-100, got %q", got)
	}
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
