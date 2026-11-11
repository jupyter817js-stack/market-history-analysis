package store

import (
	"context"
	"database/sql"
	"time"
)

type ActionRow struct {
	Fingerprint     string
	SourceRunID     int64
	AccountAddress  string
	ProxyWallet     string
	ActionType      string
	TimestampRaw    *int64
	TimestampUTC    *time.Time
	TimestampKST    *time.Time
	DateKST         *time.Time
	TransactionHash string
	ConditionID     string
	MarketSlug      string
	EventSlug       string
	TokenID         string
	Asset           string
	Outcome         string
	OutcomeIndex    *int
	Side            string
	Price           *float64
	Size            *float64
	UsdcSize        *float64
	Amount          *float64
	Title           string
	RawResponseID   int64
	RawRow          []byte
}

func (s *Store) InsertActionIfAbsent(ctx context.Context, r ActionRow) (inserted bool, err error) {
	if len(r.RawRow) == 0 {
		r.RawRow = []byte("null")
	}
	res, err := s.DB.ExecContext(ctx,
		`insert into account_actions
		 (action_fingerprint, source_run_id, account_address, proxy_wallet, action_type,
		  timestamp_raw, timestamp_utc, timestamp_kst, date_kst,
		  transaction_hash, condition_id, market_slug, event_slug,
		  token_id, asset, outcome, outcome_index, side,
		  price, size, usdc_size, amount, title,
		  raw_response_id, raw_row)
		 values ($1,$2,$3,nullif($4,''),$5,
		         $6,$7,$8,$9,
		         nullif($10,''), nullif($11,''), nullif($12,''), nullif($13,''),
		         nullif($14,''), nullif($15,''), nullif($16,''), $17, nullif($18,''),
		         $19,$20,$21,$22, nullif($23,''),
		         $24, $25::jsonb)
		 on conflict (action_fingerprint) do nothing`,
		r.Fingerprint, r.SourceRunID, r.AccountAddress, r.ProxyWallet, r.ActionType,
		nullInt64FromPtr(r.TimestampRaw), nullTimeFromPtr(r.TimestampUTC), nullTimeFromPtr(r.TimestampKST), nullTimeFromPtr(r.DateKST),
		r.TransactionHash, r.ConditionID, r.MarketSlug, r.EventSlug,
		r.TokenID, r.Asset, r.Outcome, nullInt32FromIntPtr(r.OutcomeIndex), r.Side,
		nullFloat64FromPtr(r.Price), nullFloat64FromPtr(r.Size), nullFloat64FromPtr(r.UsdcSize), nullFloat64FromPtr(r.Amount), r.Title,
		r.RawResponseID, string(r.RawRow),
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func nullInt64FromPtr(p *int64) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *p, Valid: true}
}

func nullInt32FromIntPtr(p *int) sql.NullInt32 {
	if p == nil {
		return sql.NullInt32{}
	}
	return sql.NullInt32{Int32: int32(*p), Valid: true}
}

func nullFloat64FromPtr(p *float64) sql.NullFloat64 {
	if p == nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: *p, Valid: true}
}

func nullTimeFromPtr(p *time.Time) sql.NullTime {
	if p == nil || p.IsZero() {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: *p, Valid: true}
}
