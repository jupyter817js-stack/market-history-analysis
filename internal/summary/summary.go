package summary

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"rn1-strategy-extractor/internal/store"
)

type Report struct {
	Account               string
	ProxyWallet           string
	RecentRuns            []store.SourceRun
	RawResponseTotals     map[string]int64
	ActivityByType        []KV
	BuySellMergeRedeem    map[string]int64
	May2026               []KV
	LatestKSTDay          *DayCount
	LatestActivityRows    []LatestActivityRow
	CurrentPositionCount  int64
	CurrentDistinctMarkets int64
	ClosedPositionCount   int64
	ClosedDistinctMarkets int64
	DistinctMarketsMonth  []KV
	DistinctEventsMonth   []KV
}

type KV struct {
	Key   string
	Value int64
}

type DayCount struct {
	Date  time.Time
	Count int64
}

type LatestActivityRow struct {
	ID              int64
	ActionType      string
	Side            sql.NullString
	Price           sql.NullFloat64
	Size            sql.NullFloat64
	UsdcSize        sql.NullFloat64
	ConditionID     sql.NullString
	MarketSlug      sql.NullString
	EventSlug       sql.NullString
	Title           sql.NullString
	TimestampUTC    sql.NullTime
	TimestampKST    sql.NullTime
	DateKST         sql.NullTime
	TransactionHash sql.NullString
}

func Build(ctx context.Context, s *store.Store, accountAddress string) (*Report, error) {
	r := &Report{Account: accountAddress, RawResponseTotals: map[string]int64{}, BuySellMergeRedeem: map[string]int64{}}
	if err := s.DB.QueryRowContext(ctx, `select coalesce(proxy_wallet,'') from accounts where account_address=$1`, accountAddress).Scan(&r.ProxyWallet); err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("read account: %w", err)
	}
	runs, err := s.RecentRuns(ctx, 20)
	if err != nil {
		return nil, fmt.Errorf("recent runs: %w", err)
	}
	r.RecentRuns = runs

	if err := scanKV(ctx, s.DB,
		`select endpoint, count(*)::bigint from raw_api_responses group by endpoint order by 2 desc`,
		func(k string, v int64) { r.RawResponseTotals[k] = v }); err != nil {
		return nil, fmt.Errorf("raw totals: %w", err)
	}

	rows, err := s.DB.QueryContext(ctx, `select action_type, n from v_action_counts_by_type`)
	if err != nil {
		return nil, fmt.Errorf("v_action_counts_by_type: %w", err)
	}
	for rows.Next() {
		var k string
		var v int64
		if err := rows.Scan(&k, &v); err != nil {
			rows.Close()
			return nil, err
		}
		r.ActivityByType = append(r.ActivityByType, KV{Key: k, Value: v})
	}
	rows.Close()

	var bsmr struct {
		Buys, Sells, Merges, Redeems, Splits, Conversions, MakerRebates, Rewards, Referrals sql.NullInt64
	}
	err = s.DB.QueryRowContext(ctx,
		`select buys, sells, merges, redeems, splits, conversions, maker_rebates, rewards, referral_rewards from v_buy_sell_merge_redeem_counts`,
	).Scan(&bsmr.Buys, &bsmr.Sells, &bsmr.Merges, &bsmr.Redeems, &bsmr.Splits, &bsmr.Conversions, &bsmr.MakerRebates, &bsmr.Rewards, &bsmr.Referrals)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("bsmr: %w", err)
	}
	r.BuySellMergeRedeem["buys"] = nz(bsmr.Buys)
	r.BuySellMergeRedeem["sells"] = nz(bsmr.Sells)
	r.BuySellMergeRedeem["merges"] = nz(bsmr.Merges)
	r.BuySellMergeRedeem["redeems"] = nz(bsmr.Redeems)
	r.BuySellMergeRedeem["splits"] = nz(bsmr.Splits)
	r.BuySellMergeRedeem["conversions"] = nz(bsmr.Conversions)
	r.BuySellMergeRedeem["maker_rebates"] = nz(bsmr.MakerRebates)
	r.BuySellMergeRedeem["rewards"] = nz(bsmr.Rewards)
	r.BuySellMergeRedeem["referral_rewards"] = nz(bsmr.Referrals)

	if err := scanKV(ctx, s.DB, `select action_type, n from v_may_2026_action_counts`,
		func(k string, v int64) { r.May2026 = append(r.May2026, KV{Key: k, Value: v}) }); err != nil {
		return nil, fmt.Errorf("v_may_2026_action_counts: %w", err)
	}

	var latest sql.NullTime
	var lcount sql.NullInt64
	err = s.DB.QueryRowContext(ctx, `select date_kst, n from v_latest_active_kst_day`).Scan(&latest, &lcount)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("latest kst day: %w", err)
	}
	if latest.Valid {
		r.LatestKSTDay = &DayCount{Date: latest.Time, Count: nz(lcount)}
	}

	lr, err := s.DB.QueryContext(ctx, `select id, action_type, side, price, size, usdc_size, condition_id, market_slug, event_slug, title, timestamp_utc, timestamp_kst, date_kst, transaction_hash from v_latest_activity_rows`)
	if err != nil {
		return nil, fmt.Errorf("v_latest_activity_rows: %w", err)
	}
	for lr.Next() {
		var row LatestActivityRow
		if err := lr.Scan(&row.ID, &row.ActionType, &row.Side, &row.Price, &row.Size, &row.UsdcSize, &row.ConditionID, &row.MarketSlug, &row.EventSlug, &row.Title, &row.TimestampUTC, &row.TimestampKST, &row.DateKST, &row.TransactionHash); err != nil {
			lr.Close()
			return nil, err
		}
		r.LatestActivityRows = append(r.LatestActivityRows, row)
	}
	lr.Close()

	if err := s.DB.QueryRowContext(ctx, `select n_open_rows, n_distinct_markets from v_current_position_counts`).Scan(&r.CurrentPositionCount, &r.CurrentDistinctMarkets); err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("current pos counts: %w", err)
	}
	if err := s.DB.QueryRowContext(ctx, `select n_closed_rows, n_distinct_markets from v_closed_position_counts`).Scan(&r.ClosedPositionCount, &r.ClosedDistinctMarkets); err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("closed pos counts: %w", err)
	}

	if err := scanKV(ctx, s.DB, `select month_utc, distinct_markets from v_distinct_markets_by_month`,
		func(k string, v int64) { r.DistinctMarketsMonth = append(r.DistinctMarketsMonth, KV{Key: k, Value: v}) }); err != nil {
		return nil, fmt.Errorf("v_distinct_markets_by_month: %w", err)
	}
	if err := scanKV(ctx, s.DB, `select month_utc, distinct_events from v_distinct_events_by_month`,
		func(k string, v int64) { r.DistinctEventsMonth = append(r.DistinctEventsMonth, KV{Key: k, Value: v}) }); err != nil {
		return nil, fmt.Errorf("v_distinct_events_by_month: %w", err)
	}

	return r, nil
}

func scanKV(ctx context.Context, db *sql.DB, q string, emit func(string, int64)) error {
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var v sql.NullInt64
		if err := rows.Scan(&k, &v); err != nil {
			return err
		}
		emit(k, nz(v))
	}
	return rows.Err()
}

func nz(n sql.NullInt64) int64 {
	if !n.Valid {
		return 0
	}
	return n.Int64
}

func (r *Report) WriteText(w io.Writer) {
	fmt.Fprintf(w, "Account:         %s\n", r.Account)
	fmt.Fprintf(w, "Proxy wallet:    %s\n", strDefault(r.ProxyWallet, "(unresolved)"))
	fmt.Fprintln(w, strings.Repeat("-", 60))

	fmt.Fprintln(w, "Recent source runs:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tRUN_TYPE\tSTATUS\tSTARTED_AT\tFINISHED_AT")
	for _, run := range r.RecentRuns {
		finished := ""
		if run.FinishedAt.Valid {
			finished = run.FinishedAt.Time.Format(time.RFC3339)
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n", run.ID, run.RunType, run.Status, run.StartedAt.Format(time.RFC3339), finished)
	}
	tw.Flush()
	fmt.Fprintln(w)

	fmt.Fprintln(w, "Raw response counts by endpoint:")
	for k, v := range r.RawResponseTotals {
		fmt.Fprintf(w, "  %-32s %d\n", k, v)
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "Activity counts by type:")
	for _, kv := range r.ActivityByType {
		fmt.Fprintf(w, "  %-20s %d\n", kv.Key, kv.Value)
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "Buy / sell / merge / redeem / ...:")
	for _, k := range []string{"buys", "sells", "merges", "redeems", "splits", "conversions", "maker_rebates", "rewards", "referral_rewards"} {
		fmt.Fprintf(w, "  %-18s %d\n", k, r.BuySellMergeRedeem[k])
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "May 2026 action counts (KST):")
	if len(r.May2026) == 0 {
		fmt.Fprintln(w, "  (none)")
	}
	for _, kv := range r.May2026 {
		fmt.Fprintf(w, "  %-20s %d\n", kv.Key, kv.Value)
	}
	fmt.Fprintln(w)

	if r.LatestKSTDay != nil {
		fmt.Fprintf(w, "Latest active KST day: %s (%d rows)\n", r.LatestKSTDay.Date.Format("2006-01-02"), r.LatestKSTDay.Count)
	} else {
		fmt.Fprintln(w, "Latest active KST day: (none)")
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "Latest activity rows (up to 50):")
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tTYPE\tSIDE\tPRICE\tSIZE\tUSDC\tTITLE\tTIMESTAMP_KST")
	for _, row := range r.LatestActivityRows {
		ts := ""
		if row.TimestampKST.Valid {
			ts = row.TimestampKST.Time.Format("2006-01-02 15:04:05")
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			row.ID, row.ActionType, nzs(row.Side),
			nfs(row.Price), nfs(row.Size), nfs(row.UsdcSize),
			truncate(nzs(row.Title), 40), ts)
	}
	tw.Flush()
	fmt.Fprintln(w)

	fmt.Fprintf(w, "Current positions: %d rows / %d distinct markets\n", r.CurrentPositionCount, r.CurrentDistinctMarkets)
	fmt.Fprintf(w, "Closed positions:  %d rows / %d distinct markets\n", r.ClosedPositionCount, r.ClosedDistinctMarkets)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "Distinct markets per UTC month:")
	for _, kv := range r.DistinctMarketsMonth {
		fmt.Fprintf(w, "  %-8s %d\n", kv.Key, kv.Value)
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "Distinct events per UTC month:")
	for _, kv := range r.DistinctEventsMonth {
		fmt.Fprintf(w, "  %-8s %d\n", kv.Key, kv.Value)
	}
}

func strDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func nzs(v sql.NullString) string {
	if v.Valid {
		return v.String
	}
	return ""
}

func nfs(v sql.NullFloat64) string {
	if !v.Valid {
		return ""
	}
	return fmt.Sprintf("%g", v.Float64)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
