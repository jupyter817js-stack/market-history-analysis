// Command rn1-live-scan is a strictly read-only live candidate
// scanner. It calls only public Polymarket endpoints (Gamma /events
// and CLOB /book), never authenticates, never signs anything, never
// reads any wallet or private key, and never places or cancels
// orders. Output is human-review oriented and contains no trade
// recommendations, no sizing, no PnL, and no buy/sell language.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"rn1-strategy-extractor/internal/polymarket"
	"rn1-strategy-extractor/internal/scanner"
	"rn1-strategy-extractor/internal/selection"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	def := scanner.DefaultConfig()
	var (
		horizonHours       int
		limit              int
		maxEvents          int
		maxMarketsPerEvent int
		maxBookRequests    int
		concurrency        int
		bookCacheTTLSec    int
		includeSingle      bool
		includeNonSport    bool
		sportFilterStr     string
		minScore           float64
		liquidityFilter    string
		outFormat          string
		anomalyOut         string
		gammaBase          string
		clobBase           string
		gammaRPS           float64
		clobRPS            float64
		httpTimeoutSec     int
	)

	flag.IntVar(&horizonHours, "horizon-hours", int(def.Horizon/time.Hour), "max event end-date horizon (hours)")
	flag.IntVar(&limit, "limit", def.Limit, "max candidates to print (after ranking)")
	flag.IntVar(&maxEvents, "max-events", def.MaxEvents, "hard cap on fetched events")
	flag.IntVar(&maxMarketsPerEvent, "max-markets-per-event", def.MaxMarketsPerEvent, "hard cap on markets considered per event")
	flag.IntVar(&maxBookRequests, "max-book-requests", def.MaxBookRequests, "hard cap on /book fetches per scan")
	flag.IntVar(&concurrency, "concurrency", def.Concurrency, "(reserved) parallel book fetches")
	flag.IntVar(&bookCacheTTLSec, "book-cache-ttl-sec", int(def.BookCacheTTL/time.Second), "book cache TTL in seconds")
	flag.BoolVar(&includeSingle, "include-single-market", def.IncludeSingleMarket, "include 1-market events (otherwise filtered)")
	flag.BoolVar(&includeNonSport, "include-non-sport", def.IncludeNonSport, "include events with no detected sport prefix")
	flag.StringVar(&sportFilterStr, "sport", "", "comma-separated sport prefix filter (empty = all)")
	flag.Float64Var(&minScore, "min-score", def.MinScore, "minimum selector score for inclusion")
	flag.StringVar(&liquidityFilter, "liquidity", "any", "liquidity gate filter: any|warn|pass")
	flag.StringVar(&outFormat, "format", "table", "output format: table|json")
	flag.StringVar(&anomalyOut, "anomaly-out", "", "optional JSONL path for anomaly events")
	flag.StringVar(&gammaBase, "gamma-base", "https://gamma-api.polymarket.com", "Gamma API base URL")
	flag.StringVar(&clobBase, "clob-base", "https://clob.polymarket.com", "CLOB API base URL")
	flag.Float64Var(&gammaRPS, "gamma-rps", 5, "Gamma rate limit (requests/sec)")
	flag.Float64Var(&clobRPS, "clob-rps", 5, "CLOB rate limit (requests/sec)")
	flag.IntVar(&httpTimeoutSec, "http-timeout-sec", 30, "HTTP timeout per request (seconds)")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	cfg := scanner.DefaultConfig()
	cfg.Horizon = time.Duration(horizonHours) * time.Hour
	cfg.Limit = limit
	cfg.MaxEvents = maxEvents
	cfg.MaxMarketsPerEvent = maxMarketsPerEvent
	cfg.MaxBookRequests = maxBookRequests
	cfg.Concurrency = concurrency
	cfg.BookCacheTTL = time.Duration(bookCacheTTLSec) * time.Second
	cfg.IncludeSingleMarket = includeSingle
	cfg.IncludeNonSport = includeNonSport
	cfg.SportFilter = parseSportFilter(sportFilterStr)
	cfg.MinScore = minScore
	cfg.LiquidityFilter = liquidityFilter
	cfg.Selector = selection.DefaultConfig()
	cfg.Liquidity = scanner.DefaultLiquidityConfig()

	client := polymarket.NewClient(polymarket.ClientOptions{
		GammaBaseURL: gammaBase,
		ClobBaseURL:  clobBase,
		HTTPTimeout:  time.Duration(httpTimeoutSec) * time.Second,
		GammaRPS:     gammaRPS,
		ClobRPS:      clobRPS,
	})
	defer client.Close()

	s := scanner.New(client)
	candidates, anomalies, err := s.Run(ctx, cfg)
	if err != nil {
		slog.Error("scanner.Run", "err", err)
		os.Exit(1)
	}

	if anomalyOut != "" {
		if err := writeAnomalies(anomalyOut, anomalies); err != nil {
			slog.Warn("write anomaly log", "err", err, "path", anomalyOut)
		}
	}

	switch strings.ToLower(outFormat) {
	case "json":
		printJSON(candidates)
	default:
		printTable(candidates)
	}
}

func parseSportFilter(s string) map[string]bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	out := map[string]bool{}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(strings.ToLower(p))
		if p != "" {
			out[p] = true
		}
	}
	return out
}

func printTable(c []scanner.Candidate) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "rank\tevent_slug\tsport\tscore\tband\tliquidity\tmarkets\tfamilies\thorizon\treview\tflags\treason")
	for _, x := range c {
		family := strings.Join(x.Families, "+")
		horizon := formatHorizon(x.TimeToStart)
		review := x.ReviewStatus
		if review == "" {
			review = "-"
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%.2f\t%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\n",
			x.Rank,
			x.EventSlug,
			orDash(x.SportPrefix),
			x.Score,
			x.Band,
			x.LiquidityGate,
			x.MarketCount,
			orDash(family),
			horizon,
			review,
			scanner.FormatRiskFlags(x.RiskFlags),
			x.ReasonSummary,
		)
	}
	_ = w.Flush()
}

func printJSON(c []scanner.Candidate) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(c)
}

func writeAnomalies(path string, events []scanner.AnomalyEvent) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, e := range events {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func formatHorizon(d time.Duration) string {
	if d <= 0 {
		return "-"
	}
	if d >= 24*time.Hour {
		return fmt.Sprintf("%.1fd", float64(d)/float64(24*time.Hour))
	}
	if d >= time.Hour {
		return fmt.Sprintf("%.1fh", d.Hours())
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}
