package store

import (
	"context"
	"database/sql"
	"time"

	"rn1-strategy-extractor/internal/selection"
)

// LoadMarketsByEvent returns the markets_basic rows for a single event,
// in the shape the selector package expects. Pre-trade information
// only.
func (s *Store) LoadMarketsByEvent(ctx context.Context, eventSlug string) ([]selection.MarketMeta, error) {
	rows, err := s.DB.QueryContext(ctx, `
        select condition_id, market_slug, event_slug, coalesce(event_id,''),
               coalesce(event_title,''), coalesce(title,''), coalesce(category,''),
               end_date, active, closed, negative_risk, coalesce(clob_token_ids,'')
        from markets_basic
        where event_slug = $1
    `, eventSlug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMarketsBasic(rows)
}

// LoadAllEvents returns one MarketMeta slice per event_slug for every
// event present in markets_basic. Use LoadMarketsByEvent to inspect
// one event at a time when memory matters.
func (s *Store) LoadAllEvents(ctx context.Context) (map[string][]selection.MarketMeta, error) {
	rows, err := s.DB.QueryContext(ctx, `
        select condition_id, market_slug, event_slug, coalesce(event_id,''),
               coalesce(event_title,''), coalesce(title,''), coalesce(category,''),
               end_date, active, closed, negative_risk, coalesce(clob_token_ids,'')
        from markets_basic
        where event_slug is not null and event_slug <> ''
        order by event_slug, condition_id
    `)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]selection.MarketMeta{}
	for rows.Next() {
		var m selection.MarketMeta
		var endDate sql.NullTime
		var active, closed, negRisk sql.NullBool
		if err := rows.Scan(
			&m.ConditionID, &m.MarketSlug, &m.EventSlug, &m.EventID,
			&m.EventTitle, &m.Title, &m.Category,
			&endDate, &active, &closed, &negRisk, &m.ClobTokenIDs,
		); err != nil {
			return nil, err
		}
		if endDate.Valid {
			t := endDate.Time
			m.EndDate = &t
		}
		if active.Valid {
			b := active.Bool
			m.Active = &b
		}
		if closed.Valid {
			b := closed.Bool
			m.Closed = &b
		}
		if negRisk.Valid {
			b := negRisk.Bool
			m.NegativeRisk = &b
		}
		out[m.EventSlug] = append(out[m.EventSlug], m)
	}
	return out, rows.Err()
}

func scanMarketsBasic(rows *sql.Rows) ([]selection.MarketMeta, error) {
	var out []selection.MarketMeta
	for rows.Next() {
		var m selection.MarketMeta
		var endDate sql.NullTime
		var active, closed, negRisk sql.NullBool
		if err := rows.Scan(
			&m.ConditionID, &m.MarketSlug, &m.EventSlug, &m.EventID,
			&m.EventTitle, &m.Title, &m.Category,
			&endDate, &active, &closed, &negRisk, &m.ClobTokenIDs,
		); err != nil {
			return nil, err
		}
		if endDate.Valid {
			t := endDate.Time
			m.EndDate = &t
		}
		if active.Valid {
			b := active.Bool
			m.Active = &b
		}
		if closed.Valid {
			b := closed.Bool
			m.Closed = &b
		}
		if negRisk.Valid {
			b := negRisk.Bool
			m.NegativeRisk = &b
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// EventSlugList returns just the event_slug values in markets_basic.
func (s *Store) EventSlugList(ctx context.Context) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, `
        select distinct event_slug from markets_basic
        where event_slug is not null and event_slug <> ''
        order by event_slug
    `)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// _ keeps the time import in scope when only LoadMarketsByEvent is
// referenced from tests.
var _ = time.Time{}
