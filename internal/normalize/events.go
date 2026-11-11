package normalize

import (
	"encoding/json"

	"rn1-strategy-extractor/internal/selection"
	"rn1-strategy-extractor/internal/store"
	"rn1-strategy-extractor/internal/util"
)

// MarketMetasFromEvent expands a Gamma /events row into per-market
// selection.MarketMeta records. This is the projection used by callers
// that only need pre-trade structural metadata (e.g. the live scanner)
// and want to avoid the internal/store dependency.
func MarketMetasFromEvent(event map[string]any) ([]selection.MarketMeta, error) {
	rows, err := MarketsFromEvent(event)
	if err != nil {
		return nil, err
	}
	out := make([]selection.MarketMeta, 0, len(rows))
	for _, r := range rows {
		out = append(out, selection.MarketMeta{
			ConditionID:  r.ConditionID,
			MarketSlug:   r.MarketSlug,
			EventSlug:    r.EventSlug,
			EventID:      r.EventID,
			EventTitle:   r.EventTitle,
			Title:        r.Title,
			Category:     r.Category,
			EndDate:      r.EndDate,
			Active:       r.Active,
			Closed:       r.Closed,
			NegativeRisk: r.NegativeRisk,
			ClobTokenIDs: r.ClobTokenIDs,
		})
	}
	return out, nil
}

// MarketsFromEvent expands a Gamma /events row (single event) into one
// store.MarketBasicRow per nested market entry. The event-level slug, id,
// and title are propagated onto each market row.
func MarketsFromEvent(event map[string]any) ([]store.MarketBasicRow, error) {
	if event == nil {
		return nil, nil
	}
	eventSlug := AsString(event["slug"])
	eventID := AsString(event["id"])
	eventTitle := firstNonEmpty(AsString(event["title"]), AsString(event["question"]))
	eventNegRisk := AsBoolPtr(event["negRisk"])
	if eventNegRisk == nil {
		eventNegRisk = AsBoolPtr(event["enableNegRisk"])
	}

	marketsRaw, _ := event["markets"].([]any)
	if len(marketsRaw) == 0 {
		return nil, nil
	}

	out := make([]store.MarketBasicRow, 0, len(marketsRaw))
	for _, raw := range marketsRaw {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		row := store.MarketBasicRow{
			ConditionID:  AsString(m["conditionId"]),
			MarketSlug:   AsString(m["slug"]),
			EventSlug:    eventSlug,
			EventID:      eventID,
			EventTitle:   eventTitle,
			Title:        firstNonEmpty(AsString(m["question"]), AsString(m["title"])),
			Category:     AsString(m["category"]),
			Active:       AsBoolPtr(m["active"]),
			Closed:       AsBoolPtr(m["closed"]),
			NegativeRisk: firstNonNilBool(AsBoolPtr(m["negRisk"]), eventNegRisk),
		}
		if s := AsString(m["endDate"]); s != "" {
			if t, err := util.ParseFlexibleTime(s); err == nil && !t.IsZero() {
				tt := t
				row.EndDate = &tt
			}
		}
		if v, ok := m["clobTokenIds"]; ok {
			switch t := v.(type) {
			case string:
				row.ClobTokenIDs = t
			default:
				b, _ := json.Marshal(t)
				row.ClobTokenIDs = string(b)
			}
		}
		if row.ConditionID == "" {
			continue
		}
		b, err := json.Marshal(m)
		if err != nil {
			return nil, err
		}
		row.RawMetadata = b
		out = append(out, row)
	}
	return out, nil
}

func firstNonNilBool(args ...*bool) *bool {
	for _, p := range args {
		if p != nil {
			return p
		}
	}
	return nil
}
