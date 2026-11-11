package normalize

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	root := mustRepoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, "testdata", "fixtures", name))
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return b
}

func mustRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
			return wd
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			t.Fatal("repo root not found")
		}
		wd = parent
	}
}

func TestActivity_NormalizationMapsDocumentedFields(t *testing.T) {
	b := loadFixture(t, "activity_single_page.json")
	var rows []map[string]any
	if err := json.Unmarshal(b, &rows); err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation("Asia/Seoul")
	ctx := ActivityContext{
		SourceRunID:    1,
		AccountAddress: "0xinput",
		ProxyWallet:    "0xabc",
		RawResponseID:  9,
		Location:       loc,
	}
	first, err := Activity(ctx, rows[0])
	if err != nil {
		t.Fatal(err)
	}
	if first.ActionType != "TRADE" {
		t.Errorf("action_type: %s", first.ActionType)
	}
	if first.ConditionID != "0xcond_heroic_series" {
		t.Errorf("condition_id: %s", first.ConditionID)
	}
	if first.MarketSlug != "counter-strike-aurora-vs-heroic-bo3" {
		t.Errorf("market_slug: %s", first.MarketSlug)
	}
	if first.EventSlug != "counter-strike-aurora-vs-heroic" {
		t.Errorf("event_slug: %s", first.EventSlug)
	}
	if first.Asset != "1234567890" || first.TokenID != first.Asset {
		t.Errorf("asset/token_id mismatch")
	}
	if first.Price == nil || *first.Price != 0.557 {
		t.Errorf("price: %v", first.Price)
	}
	if first.Size == nil || *first.Size != 25772.5 {
		t.Errorf("size: %v", first.Size)
	}
	if first.UsdcSize == nil || *first.UsdcSize != 14355.66 {
		t.Errorf("usdc_size: %v", first.UsdcSize)
	}
	if first.TimestampUTC == nil {
		t.Error("timestamp_utc missing")
	}
	if first.DateKST == nil {
		t.Error("date_kst missing")
	}
	if first.Fingerprint == "" {
		t.Error("fingerprint empty")
	}
}

func TestActivity_UnknownActionTypePreserved(t *testing.T) {
	b := loadFixture(t, "activity_unknown_action_type.json")
	var rows []map[string]any
	if err := json.Unmarshal(b, &rows); err != nil {
		t.Fatal(err)
	}
	row, err := Activity(ActivityContext{}, rows[0])
	if err != nil {
		t.Fatal(err)
	}
	if row.ActionType != "FUTURE_FLAG_TYPE" {
		t.Errorf("action_type was coerced: %s", row.ActionType)
	}
	if string(row.RawRow) == "" || string(row.RawRow) == "null" {
		t.Error("raw_row not preserved")
	}
}

func TestActivity_FingerprintStable(t *testing.T) {
	b := loadFixture(t, "activity_single_page.json")
	var rows []map[string]any
	if err := json.Unmarshal(b, &rows); err != nil {
		t.Fatal(err)
	}
	a, err := Activity(ActivityContext{ProxyWallet: "0xabc"}, rows[0])
	if err != nil {
		t.Fatal(err)
	}
	b2, err := Activity(ActivityContext{ProxyWallet: "0xabc"}, rows[0])
	if err != nil {
		t.Fatal(err)
	}
	if a.Fingerprint != b2.Fingerprint {
		t.Errorf("fingerprint not stable: %s vs %s", a.Fingerprint, b2.Fingerprint)
	}
}
