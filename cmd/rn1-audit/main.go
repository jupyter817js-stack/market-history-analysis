// rn1-audit produces the Validation Pass 1 inputs:
//   - selector score for every event in markets_basic
//   - join with v_event_cluster_summary historical activity metrics
//   - replay sampling across positive-envelope / partial-hedge /
//     negative-both / merge-heavy / redeem-only / May / latest-day
//     groups
//
// The output is machine-readable text the markdown report consumes.
// No new strategy. Read-only against the DB.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"rn1-strategy-extractor/internal/config"
	"rn1-strategy-extractor/internal/replay"
	"rn1-strategy-extractor/internal/selection"
	"rn1-strategy-extractor/internal/store"
	"rn1-strategy-extractor/internal/util"
)

// EventRow combines selector score + historical activity for one event.
type EventRow struct {
	EventSlug                string
	Title                    string
	Sport                    string
	Families                 []string
	MarketCount              int
	BinaryMarketCount        int
	Score                    float64
	DistinctMarkets          int
	TwoSidedMarkets          int
	PositiveEnvelopeMarkets  int
	NegativeBothMarkets      int
	TotalActions             int64
	TradeBuys                int64
	TradeSells               int64
	Merges                   int64
	Redeems                  int64
	TotalBuyUsdc             float64
	TotalMergeUsdc           float64
	TotalRedeemUsdc          float64
	BundleSizeClass          string
	ClusterExitClass         string
	FirstActionTs            sql.NullTime
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	def := config.Default()
	var dbURL string
	flag.StringVar(&dbURL, "database-url", def.DatabaseURL, "PostgreSQL DSN")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := util.LoadEnvFile(".env"); err != nil {
		slog.Warn("could not load .env", "err", err)
	}
	st, err := store.Open(ctx, dbURL)
	if err != nil {
		slog.Error("open store", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	rows, err := loadAll(ctx, st)
	if err != nil {
		slog.Error("loadAll", "err", err)
		os.Exit(1)
	}
	fmt.Printf("# loaded %d events with score + activity metrics\n", len(rows))

	// ----- Selector validation -----
	emitTopRanked(rows, 20)
	emitGroupTable("LAYER 5 events", rows, byEventSet([]string{
		"atp-alcaraz-zverev-2026-01-30",
		"bun-bay-hei-2026-05-02",
		"nba-sas-min-2026-05-10",
		"sea-par-rom-2026-05-10",
		"cs2-mglz-g2-2026-05-11",
	}))
	emitMayTop(rows, 15)
	emitAnomalies(rows)
	emitCorrelations(rows)

	// ----- Replay sampling + state-machine frequency -----
	categories := buildSamples(rows)
	stateCounts := map[replay.State]int{}
	allReplaySummaries := map[string][]ReplayLine{}
	for catName, catSlugs := range categories {
		var lines []ReplayLine
		for _, slug := range catSlugs {
			actions, err := st.LoadActionsByEvent(ctx, slug)
			if err != nil || len(actions) == 0 {
				continue
			}
			c := replay.Replay(slug, actions)
			for _, s := range c.StatesVisited {
				stateCounts[s]++
			}
			pnl := c.TotalSellUsdc + c.TotalMergeUsdc + c.TotalRedeemUsdc - c.TotalBuyUsdc
			viewPnl := pnlEstimateView(ctx, st, slug)
			lines = append(lines, ReplayLine{
				EventSlug:    slug,
				StatePath:    formatStates(c.StatesVisited),
				Legs:         len(c.Legs),
				BuyUsdc:      c.TotalBuyUsdc,
				SellUsdc:     c.TotalSellUsdc,
				MergeUsdc:    c.TotalMergeUsdc,
				RedeemUsdc:   c.TotalRedeemUsdc,
				PnLReplay:    pnl,
				PnLView:      viewPnl,
				DiffPct:      diffPercent(pnl, viewPnl),
				EnvelopeLegs: len(c.PositiveEnvelopeLegs),
			})
		}
		allReplaySummaries[catName] = lines
	}
	for _, cat := range []string{
		"positive-envelope (top by margin)",
		"partial-hedge (random)",
		"negative-both (random)",
		"merge-heavy (top by merge/buy ratio)",
		"redeem-only (random)",
		"may top bundles",
		"latest active day",
	} {
		emitReplayCategory(cat, allReplaySummaries[cat])
	}
	emitStateFrequency(stateCounts, len(rows))
}

func loadAll(ctx context.Context, st *store.Store) ([]*EventRow, error) {
	byEvent, err := st.LoadAllEvents(ctx)
	if err != nil {
		return nil, err
	}
	cfg := selection.DefaultConfig()
	cfg.IgnoreResolutionState = true
	rows := make([]*EventRow, 0, len(byEvent))
	for slug, markets := range byEvent {
		feat := selection.ExtractFeatures(markets, time.Time{})
		dec := selection.Decide(feat, cfg, time.Time{})
		r := &EventRow{
			EventSlug:         slug,
			Title:             feat.EventTitle,
			Sport:             feat.SportPrefix,
			Families:          feat.Families,
			MarketCount:       feat.MarketCount,
			BinaryMarketCount: feat.BinaryMarketCount,
			Score:             dec.Score,
		}
		rows = append(rows, r)
	}
	if err := joinClusterMetrics(ctx, st, rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func joinClusterMetrics(ctx context.Context, st *store.Store, rows []*EventRow) error {
	idx := map[string]*EventRow{}
	for _, r := range rows {
		idx[r.EventSlug] = r
	}
	q := `select event_slug, distinct_markets, two_sided_markets, positive_envelope_markets,
                 negative_both_markets, total_actions, trade_buys, trade_sells, merges, redeems,
                 total_buy_usdc, total_merge_usdc, total_redeem_usdc,
                 bundle_size_class, cluster_exit_class, first_action_ts
          from v_event_cluster_summary`
	r, err := st.DB.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer r.Close()
	for r.Next() {
		var slug, bsz, cex string
		var dm, tsm, pem, nbm int
		var ta, tb, ts, mg, rd int64
		var bu, mu, ru float64
		var first sql.NullTime
		if err := r.Scan(&slug, &dm, &tsm, &pem, &nbm, &ta, &tb, &ts, &mg, &rd, &bu, &mu, &ru, &bsz, &cex, &first); err != nil {
			return err
		}
		row, ok := idx[slug]
		if !ok {
			continue
		}
		row.DistinctMarkets = dm
		row.TwoSidedMarkets = tsm
		row.PositiveEnvelopeMarkets = pem
		row.NegativeBothMarkets = nbm
		row.TotalActions = ta
		row.TradeBuys = tb
		row.TradeSells = ts
		row.Merges = mg
		row.Redeems = rd
		row.TotalBuyUsdc = bu
		row.TotalMergeUsdc = mu
		row.TotalRedeemUsdc = ru
		row.BundleSizeClass = bsz
		row.ClusterExitClass = cex
		row.FirstActionTs = first
	}
	return r.Err()
}

func emitTopRanked(rows []*EventRow, n int) {
	sort.Slice(rows, func(i, j int) bool { return rows[i].Score > rows[j].Score })
	fmt.Println("\n## TOP_SELECTOR_RANKED")
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SCORE\tSPORT\tMKTS\tBINARY\t2SIDED\tPOS_ENV\tBUY_USDC\tACTIONS\tEVENT_SLUG")
	limit := n
	for i, r := range rows {
		if i >= limit {
			break
		}
		fmt.Fprintf(tw, "%.1f\t%s\t%d\t%d\t%d\t%d\t%.0f\t%d\t%s\n",
			r.Score, r.Sport, r.MarketCount, r.BinaryMarketCount, r.TwoSidedMarkets,
			r.PositiveEnvelopeMarkets, r.TotalBuyUsdc, r.TotalActions, r.EventSlug)
	}
	tw.Flush()
}

func emitGroupTable(title string, rows []*EventRow, predicate func(*EventRow) bool) {
	fmt.Printf("\n## %s\n", title)
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SCORE\tSPORT\tMKTS\tFAMILIES\tBUY_USDC\t2SIDED\tPOS_ENV\tBUNDLE\tEXIT\tEVENT_SLUG")
	for _, r := range rows {
		if !predicate(r) {
			continue
		}
		fmt.Fprintf(tw, "%.1f\t%s\t%d\t%s\t%.0f\t%d\t%d\t%s\t%s\t%s\n",
			r.Score, r.Sport, r.MarketCount, strings.Join(r.Families, ","), r.TotalBuyUsdc,
			r.TwoSidedMarkets, r.PositiveEnvelopeMarkets, r.BundleSizeClass, r.ClusterExitClass, r.EventSlug)
	}
	tw.Flush()
}

func byEventSet(slugs []string) func(*EventRow) bool {
	set := map[string]bool{}
	for _, s := range slugs {
		set[s] = true
	}
	return func(r *EventRow) bool { return set[r.EventSlug] }
}

func emitMayTop(rows []*EventRow, n int) {
	may := []*EventRow{}
	for _, r := range rows {
		if r.FirstActionTs.Valid && r.FirstActionTs.Time.Year() == 2026 && r.FirstActionTs.Time.Month() == 5 {
			may = append(may, r)
		}
	}
	sort.Slice(may, func(i, j int) bool { return may[i].TotalBuyUsdc > may[j].TotalBuyUsdc })
	if len(may) > n {
		may = may[:n]
	}
	fmt.Println("\n## MAY_TOP_BUNDLES_BY_BUY_USDC")
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "BUY_USDC\tSCORE\tSPORT\tMKTS\tFAMILIES\t2SIDED\tPOS_ENV\tEVENT_SLUG")
	for _, r := range may {
		fmt.Fprintf(tw, "%.0f\t%.1f\t%s\t%d\t%s\t%d\t%d\t%s\n",
			r.TotalBuyUsdc, r.Score, r.Sport, r.MarketCount, strings.Join(r.Families, ","),
			r.TwoSidedMarkets, r.PositiveEnvelopeMarkets, r.EventSlug)
	}
	tw.Flush()
}

func emitAnomalies(rows []*EventRow) {
	// Low score (<= 1) AND high RN1 activity (buy > 20K)
	fmt.Println("\n## ANOMALY_LOW_SCORE_HIGH_ACTIVITY")
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SCORE\tBUY_USDC\tSPORT\tMKTS\tBINARY\tFAMILIES\tEVENT_SLUG")
	cnt := 0
	for _, r := range rows {
		if r.Score <= 1 && r.TotalBuyUsdc >= 20000 && cnt < 15 {
			fmt.Fprintf(tw, "%.1f\t%.0f\t%s\t%d\t%d\t%s\t%s\n",
				r.Score, r.TotalBuyUsdc, r.Sport, r.MarketCount, r.BinaryMarketCount,
				strings.Join(r.Families, ","), r.EventSlug)
			cnt++
		}
	}
	tw.Flush()
	fmt.Printf("# (showed %d / total)\n", cnt)

	// High score (>= 9) AND low RN1 activity (buy < 50)
	fmt.Println("\n## ANOMALY_HIGH_SCORE_LOW_ACTIVITY")
	tw = tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SCORE\tBUY_USDC\tSPORT\tMKTS\tBINARY\tFAMILIES\tEVENT_SLUG")
	cnt = 0
	for _, r := range rows {
		if r.Score >= 9 && r.TotalBuyUsdc < 50 && cnt < 15 {
			fmt.Fprintf(tw, "%.1f\t%.2f\t%s\t%d\t%d\t%s\t%s\n",
				r.Score, r.TotalBuyUsdc, r.Sport, r.MarketCount, r.BinaryMarketCount,
				strings.Join(r.Families, ","), r.EventSlug)
			cnt++
		}
	}
	tw.Flush()
	fmt.Printf("# (showed %d / total)\n", cnt)
}

func emitCorrelations(rows []*EventRow) {
	fmt.Println("\n## SCORE_BUCKET_VS_ACTIVITY")
	type bucket struct {
		min, max float64
		label    string
	}
	buckets := []bucket{
		{0, 1.5, "0-1"},
		{1.5, 3.5, "2-3"},
		{3.5, 5.5, "4-5"},
		{5.5, 7.5, "6-7"},
		{7.5, 9.5, "8-9"},
		{9.5, 99, "10+"},
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SCORE_BUCKET\tN\tAVG_BUY_USDC\tAVG_ACTIONS\tAVG_MKTS\tAVG_2SIDED\tAVG_POS_ENV\tEVENTS_WITH_BUY>0")
	for _, b := range buckets {
		var n int
		var sumBuy, sumActions, sumMkts, sum2s, sumPe float64
		var withBuy int
		for _, r := range rows {
			if r.Score >= b.min && r.Score < b.max {
				n++
				sumBuy += r.TotalBuyUsdc
				sumActions += float64(r.TotalActions)
				sumMkts += float64(r.MarketCount)
				sum2s += float64(r.TwoSidedMarkets)
				sumPe += float64(r.PositiveEnvelopeMarkets)
				if r.TotalBuyUsdc > 0 {
					withBuy++
				}
			}
		}
		if n == 0 {
			continue
		}
		fn := float64(n)
		fmt.Fprintf(tw, "%s\t%d\t%.0f\t%.0f\t%.1f\t%.1f\t%.2f\t%d\n",
			b.label, n, sumBuy/fn, sumActions/fn, sumMkts/fn, sum2s/fn, sumPe/fn, withBuy)
	}
	tw.Flush()

	// Spearman rank correlation between Score and TotalBuyUsdc (eligible events only)
	r := spearmanRank(rows, func(r *EventRow) float64 { return r.Score }, func(r *EventRow) float64 { return r.TotalBuyUsdc })
	fmt.Printf("\n# spearman_rank_corr(score, buy_usdc) = %.4f\n", r)
	r2 := spearmanRank(rows, func(r *EventRow) float64 { return r.Score }, func(r *EventRow) float64 { return float64(r.TwoSidedMarkets) })
	fmt.Printf("# spearman_rank_corr(score, two_sided_markets) = %.4f\n", r2)
	r3 := spearmanRank(rows, func(r *EventRow) float64 { return r.Score }, func(r *EventRow) float64 { return float64(r.TotalActions) })
	fmt.Printf("# spearman_rank_corr(score, total_actions) = %.4f\n", r3)
}

func spearmanRank(rows []*EventRow, fa, fb func(*EventRow) float64) float64 {
	rps := make([]rp, 0, len(rows))
	for _, r := range rows {
		rps = append(rps, rp{a: fa(r), b: fb(r)})
	}
	rank(rps, true)
	rank(rps, false)
	n := float64(len(rps))
	if n < 3 {
		return 0
	}
	var sum float64
	for _, p := range rps {
		d := p.ra - p.rb
		sum += d * d
	}
	return 1 - (6*sum)/(n*(n*n-1))
}
func rank(arr []rp, byA bool) {
	if byA {
		sort.Slice(arr, func(i, j int) bool { return arr[i].a < arr[j].a })
	} else {
		sort.Slice(arr, func(i, j int) bool { return arr[i].b < arr[j].b })
	}
	for i := range arr {
		if byA {
			arr[i].ra = float64(i + 1)
		} else {
			arr[i].rb = float64(i + 1)
		}
	}
}

type rp struct {
	a, b   float64
	ra, rb float64
}

func buildSamples(rows []*EventRow) map[string][]string {
	posEnv := []*EventRow{}
	partial := []*EventRow{}
	negBoth := []*EventRow{}
	merge := []*EventRow{}
	redeem := []*EventRow{}
	may := []*EventRow{}
	for _, r := range rows {
		if r.PositiveEnvelopeMarkets > 0 {
			posEnv = append(posEnv, r)
		} else if r.NegativeBothMarkets > 0 {
			negBoth = append(negBoth, r)
		} else if r.TwoSidedMarkets > 0 {
			partial = append(partial, r)
		}
		if r.TotalMergeUsdc > 0 && r.TotalBuyUsdc > 1000 && r.TotalMergeUsdc/r.TotalBuyUsdc > 0.5 {
			merge = append(merge, r)
		}
		if r.TotalMergeUsdc == 0 && r.TotalRedeemUsdc > 0 && r.TotalBuyUsdc > 1000 {
			redeem = append(redeem, r)
		}
		if r.FirstActionTs.Valid && r.FirstActionTs.Time.Year() == 2026 && r.FirstActionTs.Time.Month() == 5 {
			may = append(may, r)
		}
	}
	// Sort and slice
	sort.Slice(posEnv, func(i, j int) bool { return posEnv[i].TotalBuyUsdc > posEnv[j].TotalBuyUsdc })
	sort.Slice(partial, func(i, j int) bool { return partial[i].TotalBuyUsdc > partial[j].TotalBuyUsdc })
	sort.Slice(negBoth, func(i, j int) bool { return negBoth[i].TotalBuyUsdc > negBoth[j].TotalBuyUsdc })
	sort.Slice(merge, func(i, j int) bool { return merge[i].TotalMergeUsdc > merge[j].TotalMergeUsdc })
	sort.Slice(redeem, func(i, j int) bool { return redeem[i].TotalRedeemUsdc > redeem[j].TotalRedeemUsdc })
	sort.Slice(may, func(i, j int) bool { return may[i].TotalBuyUsdc > may[j].TotalBuyUsdc })

	latestDay := loadLatestDayTop(rows)

	return map[string][]string{
		"positive-envelope (top by margin)":   slugList(top(posEnv, 5)),
		"partial-hedge (random)":              slugList(top(partial, 5)),
		"negative-both (random)":              slugList(top(negBoth, 5)),
		"merge-heavy (top by merge/buy ratio)": slugList(top(merge, 5)),
		"redeem-only (random)":                slugList(top(redeem, 5)),
		"may top bundles":                     slugList(top(may, 5)),
		"latest active day":                   latestDay,
	}
}

func top(rs []*EventRow, n int) []*EventRow {
	if len(rs) > n {
		return rs[:n]
	}
	return rs
}

func slugList(rs []*EventRow) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.EventSlug)
	}
	return out
}

func loadLatestDayTop(rows []*EventRow) []string {
	// Use the latest-day-top5 view if available; otherwise fall back to
	// in-memory: events with max FirstActionTs grouped by date.
	var maxDate time.Time
	for _, r := range rows {
		if r.FirstActionTs.Valid && r.FirstActionTs.Time.After(maxDate) {
			maxDate = r.FirstActionTs.Time
		}
	}
	// Select events that started on the same UTC date as maxDate.
	day := maxDate.Format("2006-01-02")
	candidates := []*EventRow{}
	for _, r := range rows {
		if r.FirstActionTs.Valid && r.FirstActionTs.Time.Format("2006-01-02") == day {
			candidates = append(candidates, r)
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].TotalBuyUsdc > candidates[j].TotalBuyUsdc })
	if len(candidates) > 5 {
		candidates = candidates[:5]
	}
	return slugList(candidates)
}

type ReplayLine struct {
	EventSlug    string
	StatePath    string
	Legs         int
	BuyUsdc      float64
	SellUsdc     float64
	MergeUsdc    float64
	RedeemUsdc   float64
	PnLReplay    float64
	PnLView      float64
	DiffPct      float64
	EnvelopeLegs int
}

func emitReplayCategory(name string, lines []ReplayLine) {
	fmt.Printf("\n## REPLAY_SAMPLE: %s\n", name)
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "LEGS\tBUY\tMERGE\tREDEEM\tPNL_REPLAY\tPNL_VIEW\tDIFF%\tENV_LEGS\tSTATE_PATH\tEVENT_SLUG")
	for _, l := range lines {
		fmt.Fprintf(tw, "%d\t%.0f\t%.0f\t%.0f\t%.0f\t%.0f\t%.2f\t%d\t%s\t%s\n",
			l.Legs, l.BuyUsdc, l.MergeUsdc, l.RedeemUsdc, l.PnLReplay, l.PnLView, l.DiffPct,
			l.EnvelopeLegs, l.StatePath, l.EventSlug)
	}
	tw.Flush()
}

func emitStateFrequency(counts map[replay.State]int, totalEvents int) {
	fmt.Printf("\n## STATE_FREQUENCY (across %d sampled replays)\n", sumMapInt(counts))
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STATE\tVISITS")
	order := []replay.State{
		replay.StateScan, replay.StateProbe, replay.StateAccumulateA,
		replay.StateOpenB, replay.StateExpand, replay.StateLayer,
		replay.StateMergeHarvest, replay.StateRedeem, replay.StateIdle,
	}
	for _, s := range order {
		fmt.Fprintf(tw, "%s\t%d\n", s, counts[s])
	}
	tw.Flush()
}

func sumMapInt(m map[replay.State]int) int {
	s := 0
	for _, v := range m {
		s += v
	}
	return s
}

func formatStates(ss []replay.State) string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = string(s)
	}
	return strings.Join(out, "->")
}

func pnlEstimateView(ctx context.Context, st *store.Store, slug string) float64 {
	var p sql.NullFloat64
	row := st.DB.QueryRowContext(ctx,
		"select realized_pnl_estimate from v_event_realized_pnl_estimate where event_slug=$1", slug)
	if err := row.Scan(&p); err != nil {
		return 0
	}
	if p.Valid {
		return p.Float64
	}
	return 0
}

func diffPercent(a, b float64) float64 {
	if math.Abs(b) < 1e-9 {
		if math.Abs(a) < 1e-9 {
			return 0
		}
		return 100
	}
	return 100 * math.Abs(a-b) / math.Abs(b)
}
