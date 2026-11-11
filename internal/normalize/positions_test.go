package normalize

import (
	"encoding/json"
	"testing"
)

func TestPositionCurrent_Map(t *testing.T) {
	b := loadFixture(t, "positions_current_page.json")
	var rows []map[string]any
	if err := json.Unmarshal(b, &rows); err != nil {
		t.Fatal(err)
	}
	row, err := PositionCurrent(PositionContext{}, rows[0])
	if err != nil {
		t.Fatal(err)
	}
	if row.ConditionID != "0xcond_spurs_twolves" {
		t.Errorf("condition_id: %s", row.ConditionID)
	}
	if row.TokenID != "tok-spurs" {
		t.Errorf("token_id should equal asset: %s", row.TokenID)
	}
	if row.AvgPrice == nil || *row.AvgPrice != 0.417 {
		t.Errorf("avg_price: %v", row.AvgPrice)
	}
}

func TestPositionClosed_NoCashPnlSynthesis(t *testing.T) {
	b := loadFixture(t, "closed_positions_multi_page.json")
	var rows []map[string]any
	if err := json.Unmarshal(b, &rows); err != nil {
		t.Fatal(err)
	}
	row, err := PositionClosed(PositionContext{}, rows[0])
	if err != nil {
		t.Fatal(err)
	}
	if row.CashPnl != nil {
		t.Errorf("cash_pnl must not be synthesized from realized_pnl, got %v", *row.CashPnl)
	}
	if row.RealizedPnl == nil || *row.RealizedPnl != 12.5 {
		t.Errorf("realized_pnl: %v", row.RealizedPnl)
	}
	if row.TimestampUTC == nil {
		t.Error("timestamp_utc missing")
	}
}

func TestMarketNormalize_BasicFields(t *testing.T) {
	b := loadFixture(t, "markets_keyset_by_condition_ids.json")
	var env map[string]any
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatal(err)
	}
	arr := env["data"].([]any)
	first := arr[0].(map[string]any)
	row, err := Market(first)
	if err != nil {
		t.Fatal(err)
	}
	if row.ConditionID != "0xcond_heroic_series" {
		t.Errorf("condition_id: %s", row.ConditionID)
	}
	if row.EventSlug != "counter-strike-aurora-vs-heroic" {
		t.Errorf("event_slug: %s", row.EventSlug)
	}
	if row.EventID != "ev-1" {
		t.Errorf("event_id: %s", row.EventID)
	}
}
