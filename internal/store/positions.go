package store

import (
	"context"
	"database/sql"
	"time"
)

type PositionCurrentRow struct {
	SourceRunID    int64
	AccountAddress string
	ProxyWallet    string
	ConditionID    string
	MarketSlug     string
	EventSlug      string
	TokenID        string
	Outcome        string
	Size           *float64
	AvgPrice       *float64
	CurrentValue   *float64
	CashPnl        *float64
	RealizedPnl    *float64
	Redeemable     *bool
	Mergeable      *bool
	NegativeRisk   *bool
	RawResponseID  int64
	RawRow         []byte
}

func (s *Store) InsertPositionCurrent(ctx context.Context, r PositionCurrentRow) error {
	if len(r.RawRow) == 0 {
		r.RawRow = []byte("null")
	}
	_, err := s.DB.ExecContext(ctx,
		`insert into positions_current
		 (source_run_id, account_address, proxy_wallet,
		  condition_id, market_slug, event_slug, token_id, outcome,
		  size, avg_price, current_value, cash_pnl, realized_pnl,
		  redeemable, mergeable, negative_risk,
		  raw_response_id, raw_row)
		 values ($1,$2,nullif($3,''),
		         nullif($4,''), nullif($5,''), nullif($6,''), nullif($7,''), nullif($8,''),
		         $9,$10,$11,$12,$13,
		         $14,$15,$16,
		         $17,$18::jsonb)
		 on conflict (source_run_id, proxy_wallet, condition_id, token_id) do nothing`,
		r.SourceRunID, r.AccountAddress, r.ProxyWallet,
		r.ConditionID, r.MarketSlug, r.EventSlug, r.TokenID, r.Outcome,
		nullFloat64FromPtr(r.Size), nullFloat64FromPtr(r.AvgPrice), nullFloat64FromPtr(r.CurrentValue), nullFloat64FromPtr(r.CashPnl), nullFloat64FromPtr(r.RealizedPnl),
		nullBoolFromPtr(r.Redeemable), nullBoolFromPtr(r.Mergeable), nullBoolFromPtr(r.NegativeRisk),
		r.RawResponseID, string(r.RawRow),
	)
	return err
}

type PositionClosedRow struct {
	SourceRunID    int64
	AccountAddress string
	ProxyWallet    string
	ConditionID    string
	MarketSlug     string
	EventSlug      string
	TokenID        string
	Outcome        string
	AvgPrice       *float64
	TotalBought    *float64
	RealizedPnl    *float64
	CashPnl        *float64
	CurPrice       *float64
	TimestampUTC   *time.Time
	EndDate        *time.Time
	RawResponseID  int64
	RawRow         []byte
}

func (s *Store) InsertPositionClosed(ctx context.Context, r PositionClosedRow) error {
	if len(r.RawRow) == 0 {
		r.RawRow = []byte("null")
	}
	_, err := s.DB.ExecContext(ctx,
		`insert into positions_closed
		 (source_run_id, account_address, proxy_wallet,
		  condition_id, market_slug, event_slug, token_id, outcome,
		  avg_price, total_bought, realized_pnl, cash_pnl, cur_price,
		  timestamp_utc, end_date,
		  raw_response_id, raw_row)
		 values ($1,$2,nullif($3,''),
		         nullif($4,''), nullif($5,''), nullif($6,''), nullif($7,''), nullif($8,''),
		         $9,$10,$11,$12,$13,
		         $14,$15,
		         $16,$17::jsonb)
		 on conflict (source_run_id, proxy_wallet, condition_id, token_id, timestamp_utc) do nothing`,
		r.SourceRunID, r.AccountAddress, r.ProxyWallet,
		r.ConditionID, r.MarketSlug, r.EventSlug, r.TokenID, r.Outcome,
		nullFloat64FromPtr(r.AvgPrice), nullFloat64FromPtr(r.TotalBought), nullFloat64FromPtr(r.RealizedPnl), nullFloat64FromPtr(r.CashPnl), nullFloat64FromPtr(r.CurPrice),
		nullTimeFromPtr(r.TimestampUTC), nullTimeFromPtr(r.EndDate),
		r.RawResponseID, string(r.RawRow),
	)
	return err
}

func nullBoolFromPtr(p *bool) sql.NullBool {
	if p == nil {
		return sql.NullBool{}
	}
	return sql.NullBool{Bool: *p, Valid: true}
}
