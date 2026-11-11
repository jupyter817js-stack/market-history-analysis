package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"text/tabwriter"
	"time"

	"rn1-strategy-extractor/internal/config"
	"rn1-strategy-extractor/internal/selection"
	"rn1-strategy-extractor/internal/store"
	"rn1-strategy-extractor/internal/util"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	def := config.Default()

	var (
		eventSlug   string
		all         bool
		dbURL       string
		outFormat   string
		nowOverride string
		topN        int
		eligibleOnly bool
	)
	flag.StringVar(&eventSlug, "event-slug", "", "single event to evaluate (mutually exclusive with --all)")
	flag.BoolVar(&all, "all", false, "evaluate every event in markets_basic")
	flag.StringVar(&dbURL, "database-url", def.DatabaseURL, "PostgreSQL DSN")
	flag.StringVar(&outFormat, "format", "table", "output format: table|json")
	flag.StringVar(&nowOverride, "now", "", "ISO timestamp to use as 'now' (default: real now)")
	flag.IntVar(&topN, "top", 25, "with --all, print top-N by score (0 = all eligible)")
	flag.BoolVar(&eligibleOnly, "eligible-only", true, "with --all, only print eligible events")
	flag.Parse()

	if eventSlug == "" && !all {
		fmt.Fprintln(os.Stderr, "usage: rn1-select --event-slug <slug>")
		fmt.Fprintln(os.Stderr, "       rn1-select --all [--top N] [--eligible-only]")
		os.Exit(2)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := util.LoadEnvFile(".env"); err != nil {
		slog.Warn("could not load .env", "err", err)
	}

	now := time.Now().UTC()
	if nowOverride != "" {
		t, err := util.ParseFlexibleTime(nowOverride)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid --now: %v\n", err)
			os.Exit(2)
		}
		now = t.UTC()
	}

	st, err := store.Open(ctx, dbURL)
	if err != nil {
		slog.Error("open store", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	cfg := selection.DefaultConfig()

	if eventSlug != "" {
		markets, err := st.LoadMarketsByEvent(ctx, eventSlug)
		if err != nil {
			slog.Error("load markets", "err", err)
			os.Exit(1)
		}
		if len(markets) == 0 {
			fmt.Fprintf(os.Stderr, "no markets found for event_slug=%s\n", eventSlug)
			os.Exit(3)
		}
		feat := selection.ExtractFeatures(markets, now)
		dec := selection.Decide(feat, cfg, now)
		printDecisions([]selection.Decision{dec}, outFormat)
		return
	}

	byEvent, err := st.LoadAllEvents(ctx)
	if err != nil {
		slog.Error("load all events", "err", err)
		os.Exit(1)
	}

	decisions := make([]selection.Decision, 0, len(byEvent))
	for _, markets := range byEvent {
		feat := selection.ExtractFeatures(markets, now)
		dec := selection.Decide(feat, cfg, now)
		decisions = append(decisions, dec)
	}
	sort.Slice(decisions, func(i, j int) bool {
		if decisions[i].Eligible != decisions[j].Eligible {
			return decisions[i].Eligible && !decisions[j].Eligible
		}
		return decisions[i].Score > decisions[j].Score
	})

	if eligibleOnly {
		filtered := decisions[:0]
		for _, d := range decisions {
			if d.Eligible {
				filtered = append(filtered, d)
			}
		}
		decisions = filtered
	}
	if topN > 0 && len(decisions) > topN {
		decisions = decisions[:topN]
	}
	printDecisions(decisions, outFormat)
}

func printDecisions(ds []selection.Decision, format string) {
	switch format {
	case "json":
		_ = json.NewEncoder(os.Stdout).Encode(ds)
	default:
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ELIGIBLE\tSCORE\tSPORT\tMARKETS\tBINARY\tFAMILIES\tHORIZON_H\tEVENT_SLUG\tEVENT_TITLE")
		for _, d := range ds {
			f := d.Features
			hourHorizon := 0
			if f.HorizonSeconds > 0 {
				hourHorizon = int(f.HorizonSeconds / 3600)
			}
			fams := ""
			for i, x := range f.Families {
				if i > 0 {
					fams += ","
				}
				fams += x
			}
			fmt.Fprintf(tw, "%v\t%.1f\t%s\t%d\t%d\t%s\t%d\t%s\t%s\n",
				d.Eligible, d.Score, f.SportPrefix, f.MarketCount, f.BinaryMarketCount, fams, hourHorizon,
				f.EventSlug, truncate(f.EventTitle, 50))
		}
		tw.Flush()
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
