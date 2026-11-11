package normalize

import (
	"encoding/json"
	"time"

	"rn1-strategy-extractor/internal/store"
	"rn1-strategy-extractor/internal/util"
)

type ActivityContext struct {
	SourceRunID    int64
	AccountAddress string
	ProxyWallet    string
	RawResponseID  int64
	Location       *time.Location
}

func Activity(ctx ActivityContext, raw map[string]any) (store.ActionRow, error) {
	row := store.ActionRow{
		SourceRunID:    ctx.SourceRunID,
		AccountAddress: ctx.AccountAddress,
		ProxyWallet:    ctx.ProxyWallet,
		RawResponseID:  ctx.RawResponseID,
	}

	row.ActionType = AsString(raw["type"])
	row.TransactionHash = AsString(raw["transactionHash"])
	row.ConditionID = AsString(raw["conditionId"])
	row.MarketSlug = AsString(raw["slug"])
	row.EventSlug = AsString(raw["eventSlug"])
	row.Asset = AsString(raw["asset"])
	row.TokenID = row.Asset
	row.Outcome = AsString(raw["outcome"])
	row.OutcomeIndex = AsIntPtr(raw["outcomeIndex"])
	row.Side = AsString(raw["side"])
	row.Price = AsFloatPtr(raw["price"])
	row.Size = AsFloatPtr(raw["size"])
	row.UsdcSize = AsFloatPtr(raw["usdcSize"])
	row.Amount = AsFloatPtr(raw["amount"])
	row.Title = AsString(raw["title"])
	row.TimestampRaw = AsInt64Ptr(raw["timestamp"])

	if row.TimestampRaw != nil {
		t := util.ParseUnixSeconds(*row.TimestampRaw)
		if !t.IsZero() {
			tutc := t
			row.TimestampUTC = &tutc
			if ctx.Location != nil {
				tkst := t.In(ctx.Location)
				row.TimestampKST = &tkst
				dk := time.Date(tkst.Year(), tkst.Month(), tkst.Day(), 0, 0, 0, 0, ctx.Location)
				row.DateKST = &dk
			}
		}
	}

	row.Fingerprint = util.SHA256Hex(
		row.ProxyWallet,
		row.ActionType,
		ptrInt64String(row.TimestampRaw),
		row.TransactionHash,
		row.ConditionID,
		row.Asset,
		row.Side,
		ptrFloatString(row.Price),
		ptrFloatString(row.Size),
	)

	rawJSON, err := json.Marshal(raw)
	if err != nil {
		return row, err
	}
	row.RawRow = rawJSON
	return row, nil
}

func ptrInt64String(p *int64) string {
	if p == nil {
		return ""
	}
	return formatInt64(*p)
}

func ptrFloatString(p *float64) string {
	if p == nil {
		return ""
	}
	return formatFloat(*p)
}
