package store

import (
	"context"
	"database/sql"

	"rn1-strategy-extractor/internal/replay"
)

// LoadActionsByEvent returns every account_actions row for one
// event_slug, ordered by timestamp_utc ascending. This is the input
// to the replay engine; it is read-only and pre-aggregation.
func (s *Store) LoadActionsByEvent(ctx context.Context, eventSlug string) ([]replay.Action, error) {
	rows, err := s.DB.QueryContext(ctx, `
        select coalesce(timestamp_utc, to_timestamp(0)),
               action_type,
               coalesce(side,''),
               coalesce(condition_id,''),
               coalesce(asset,''),
               coalesce(market_slug,''),
               coalesce(title,''),
               coalesce(outcome,''),
               coalesce(price, 0),
               coalesce(size, 0),
               coalesce(usdc_size, 0),
               coalesce(transaction_hash, '')
        from account_actions
        where event_slug = $1
        order by timestamp_utc, condition_id, asset, transaction_hash
    `, eventSlug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []replay.Action
	for rows.Next() {
		var a replay.Action
		var price, size, usdc sql.NullFloat64
		if err := rows.Scan(
			&a.TimestampUTC,
			&a.ActionType,
			&a.Side,
			&a.ConditionID,
			&a.TokenID,
			&a.MarketSlug,
			&a.Title,
			&a.Outcome,
			&price,
			&size,
			&usdc,
			&a.TxHash,
		); err != nil {
			return nil, err
		}
		if price.Valid {
			a.Price = price.Float64
		}
		if size.Valid {
			a.Size = size.Float64
		}
		if usdc.Valid {
			a.UsdcSize = usdc.Float64
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
