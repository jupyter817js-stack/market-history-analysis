package store

import (
	"context"
	"time"
)

type MarketBasicRow struct {
	ConditionID  string
	MarketSlug   string
	EventSlug    string
	EventID      string
	EventTitle   string
	Title        string
	Category     string
	EndDate      *time.Time
	Active       *bool
	Closed       *bool
	NegativeRisk *bool
	ClobTokenIDs string
	RawMetadata  []byte
}

func (s *Store) UpsertMarketBasic(ctx context.Context, r MarketBasicRow) error {
	if r.ConditionID == "" {
		return nil
	}
	if len(r.RawMetadata) == 0 {
		r.RawMetadata = []byte("null")
	}
	_, err := s.DB.ExecContext(ctx,
		`insert into markets_basic
		 (condition_id, market_slug, event_slug, event_id, event_title, title, category,
		  end_date, active, closed, negative_risk, clob_token_ids, raw_metadata, updated_at)
		 values ($1, nullif($2,''), nullif($3,''), nullif($4,''), nullif($5,''), nullif($6,''), nullif($7,''),
		         $8, $9, $10, $11, nullif($12,''), $13::jsonb, now())
		 on conflict (condition_id) do update
		   set market_slug    = coalesce(excluded.market_slug,    markets_basic.market_slug),
		       event_slug     = coalesce(excluded.event_slug,     markets_basic.event_slug),
		       event_id       = coalesce(excluded.event_id,       markets_basic.event_id),
		       event_title    = coalesce(excluded.event_title,    markets_basic.event_title),
		       title          = coalesce(excluded.title,          markets_basic.title),
		       category       = coalesce(excluded.category,       markets_basic.category),
		       end_date       = coalesce(excluded.end_date,       markets_basic.end_date),
		       active         = coalesce(excluded.active,         markets_basic.active),
		       closed         = coalesce(excluded.closed,         markets_basic.closed),
		       negative_risk  = coalesce(excluded.negative_risk,  markets_basic.negative_risk),
		       clob_token_ids = coalesce(excluded.clob_token_ids, markets_basic.clob_token_ids),
		       raw_metadata   = excluded.raw_metadata,
		       updated_at     = now()`,
		r.ConditionID, r.MarketSlug, r.EventSlug, r.EventID, r.EventTitle, r.Title, r.Category,
		nullTimeFromPtr(r.EndDate), nullBoolFromPtr(r.Active), nullBoolFromPtr(r.Closed), nullBoolFromPtr(r.NegativeRisk),
		r.ClobTokenIDs, string(r.RawMetadata),
	)
	return err
}

func (s *Store) TouchedConditionIDs(ctx context.Context) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx,
		`select distinct condition_id from (
		   select condition_id from account_actions  where condition_id is not null and condition_id <> ''
		   union
		   select condition_id from positions_current where condition_id is not null and condition_id <> ''
		   union
		   select condition_id from positions_closed  where condition_id is not null and condition_id <> ''
		 ) t
		 order by 1`,
	)
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

func (s *Store) TouchedEventSlugs(ctx context.Context) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx,
		`select distinct event_slug
		 from account_actions
		 where event_slug is not null and event_slug <> ''
		 order by 1`,
	)
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

func (s *Store) TouchedEventSlugsWithoutCondition(ctx context.Context) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx,
		`select distinct event_slug
		 from account_actions
		 where event_slug is not null and event_slug <> ''
		   and (condition_id is null or condition_id = '')
		 order by 1`,
	)
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
