package normalize

import (
	"encoding/json"
	"time"

	"rn1-strategy-extractor/internal/store"
	"rn1-strategy-extractor/internal/util"
)

type PositionContext struct {
	SourceRunID    int64
	AccountAddress string
	ProxyWallet    string
	RawResponseID  int64
}

func PositionCurrent(ctx PositionContext, raw map[string]any) (store.PositionCurrentRow, error) {
	row := store.PositionCurrentRow{
		SourceRunID:    ctx.SourceRunID,
		AccountAddress: ctx.AccountAddress,
		ProxyWallet:    ctx.ProxyWallet,
		RawResponseID:  ctx.RawResponseID,
	}
	row.ConditionID = AsString(raw["conditionId"])
	row.MarketSlug = AsString(raw["slug"])
	row.EventSlug = AsString(raw["eventSlug"])
	row.TokenID = AsString(raw["asset"])
	row.Outcome = AsString(raw["outcome"])
	row.Size = AsFloatPtr(raw["size"])
	row.AvgPrice = AsFloatPtr(raw["avgPrice"])
	row.CurrentValue = AsFloatPtr(raw["currentValue"])
	row.CashPnl = AsFloatPtr(raw["cashPnl"])
	row.RealizedPnl = AsFloatPtr(raw["realizedPnl"])
	row.Redeemable = AsBoolPtr(raw["redeemable"])
	row.Mergeable = AsBoolPtr(raw["mergeable"])
	row.NegativeRisk = AsBoolPtr(raw["negativeRisk"])

	b, err := json.Marshal(raw)
	if err != nil {
		return row, err
	}
	row.RawRow = b
	return row, nil
}

func PositionClosed(ctx PositionContext, raw map[string]any) (store.PositionClosedRow, error) {
	row := store.PositionClosedRow{
		SourceRunID:    ctx.SourceRunID,
		AccountAddress: ctx.AccountAddress,
		ProxyWallet:    ctx.ProxyWallet,
		RawResponseID:  ctx.RawResponseID,
	}
	row.ConditionID = AsString(raw["conditionId"])
	row.MarketSlug = AsString(raw["slug"])
	row.EventSlug = AsString(raw["eventSlug"])
	row.TokenID = AsString(raw["asset"])
	row.Outcome = AsString(raw["outcome"])
	row.AvgPrice = AsFloatPtr(raw["avgPrice"])
	row.TotalBought = AsFloatPtr(raw["totalBought"])
	row.RealizedPnl = AsFloatPtr(raw["realizedPnl"])
	if v, ok := raw["cashPnl"]; ok {
		row.CashPnl = AsFloatPtr(v)
	}
	row.CurPrice = AsFloatPtr(raw["curPrice"])
	if tsRaw := AsInt64Ptr(raw["timestamp"]); tsRaw != nil {
		t := util.ParseUnixSeconds(*tsRaw)
		if !t.IsZero() {
			row.TimestampUTC = &t
		}
	} else if s := AsString(raw["timestamp"]); s != "" {
		if t, err := util.ParseFlexibleTime(s); err == nil && !t.IsZero() {
			row.TimestampUTC = &t
		}
	}
	if s := AsString(raw["endDate"]); s != "" {
		if t, err := util.ParseFlexibleTime(s); err == nil && !t.IsZero() {
			tt := t
			row.EndDate = &tt
		}
	} else if v, ok := raw["endDate"]; ok {
		if tsRaw := AsInt64Ptr(v); tsRaw != nil {
			t := util.ParseUnixSeconds(*tsRaw)
			if !t.IsZero() {
				row.EndDate = &t
			}
		}
	}

	b, err := json.Marshal(raw)
	if err != nil {
		return row, err
	}
	row.RawRow = b
	return row, nil
}

func Market(raw map[string]any) (store.MarketBasicRow, error) {
	row := store.MarketBasicRow{}
	row.ConditionID = AsString(raw["conditionId"])
	row.MarketSlug = AsString(raw["slug"])
	row.Title = firstNonEmpty(AsString(raw["question"]), AsString(raw["title"]))
	row.Category = AsString(raw["category"])
	if s := AsString(raw["endDate"]); s != "" {
		if t, err := util.ParseFlexibleTime(s); err == nil && !t.IsZero() {
			tt := t
			row.EndDate = &tt
		}
	}
	row.Active = AsBoolPtr(raw["active"])
	row.Closed = AsBoolPtr(raw["closed"])
	row.NegativeRisk = AsBoolPtr(raw["negRisk"])
	if v, ok := raw["clobTokenIds"]; ok {
		switch t := v.(type) {
		case string:
			row.ClobTokenIDs = t
		default:
			b, _ := json.Marshal(t)
			row.ClobTokenIDs = string(b)
		}
	}
	if ev, ok := raw["events"]; ok {
		if arr, ok := ev.([]any); ok && len(arr) > 0 {
			if m, ok := arr[0].(map[string]any); ok {
				row.EventSlug = AsString(m["slug"])
				row.EventID = AsString(m["id"])
				row.EventTitle = firstNonEmpty(AsString(m["title"]), AsString(m["question"]))
			}
		}
		if m, ok := ev.(map[string]any); ok {
			row.EventSlug = firstNonEmpty(row.EventSlug, AsString(m["slug"]))
			row.EventID = firstNonEmpty(row.EventID, AsString(m["id"]))
			row.EventTitle = firstNonEmpty(row.EventTitle, AsString(m["title"]))
		}
	}
	row.EventSlug = firstNonEmpty(row.EventSlug, AsString(raw["eventSlug"]))

	b, err := json.Marshal(raw)
	if err != nil {
		return row, err
	}
	row.RawMetadata = b
	return row, nil
}

func MarketFromEvent(raw map[string]any) (slug, eventID, title string, ok bool) {
	slug = AsString(raw["slug"])
	eventID = AsString(raw["id"])
	title = firstNonEmpty(AsString(raw["title"]), AsString(raw["question"]))
	ok = slug != ""
	return
}

func firstNonEmpty(args ...string) string {
	for _, s := range args {
		if s != "" {
			return s
		}
	}
	return ""
}

var _ = time.Time{}
