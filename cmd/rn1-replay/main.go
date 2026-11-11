package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	"rn1-strategy-extractor/internal/config"
	"rn1-strategy-extractor/internal/replay"
	"rn1-strategy-extractor/internal/store"
	"rn1-strategy-extractor/internal/util"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	def := config.Default()

	var (
		eventSlug string
		dbURL     string
		outFormat string
		showLegs  bool
	)
	flag.StringVar(&eventSlug, "event-slug", "", "event to replay (required)")
	flag.StringVar(&dbURL, "database-url", def.DatabaseURL, "PostgreSQL DSN")
	flag.StringVar(&outFormat, "format", "summary", "summary|trace|json")
	flag.BoolVar(&showLegs, "show-legs", true, "in summary mode, also print per-leg lines")
	flag.Parse()

	if eventSlug == "" {
		fmt.Fprintln(os.Stderr, "usage: rn1-replay --event-slug <slug> [--format summary|trace|json]")
		os.Exit(2)
	}

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

	actions, err := st.LoadActionsByEvent(ctx, eventSlug)
	if err != nil {
		slog.Error("load actions", "err", err)
		os.Exit(1)
	}
	if len(actions) == 0 {
		fmt.Fprintf(os.Stderr, "no actions for event_slug=%s\n", eventSlug)
		os.Exit(3)
	}

	c := replay.Replay(eventSlug, actions)

	switch outFormat {
	case "json":
		_ = json.NewEncoder(os.Stdout).Encode(c)
	case "trace":
		printTrace(c)
	default:
		printSummary(c, showLegs)
	}
}

func printSummary(c *replay.Cluster, showLegs bool) {
	fmt.Println(c.Summary())
	fmt.Println()
	fmt.Printf("start: %s\nend:   %s\nspan:  %s\n",
		c.StartTs.Format(time.RFC3339), c.EndTs.Format(time.RFC3339),
		c.EndTs.Sub(c.StartTs))
	fmt.Println()
	fmt.Printf("buy_usdc:    $%.2f\n", c.TotalBuyUsdc)
	fmt.Printf("sell_usdc:   $%.2f\n", c.TotalSellUsdc)
	fmt.Printf("merge_usdc:  $%.2f (events=%d)\n", c.TotalMergeUsdc, c.MergeEvents)
	fmt.Printf("redeem_usdc: $%.2f (events=%d)\n", c.TotalRedeemUsdc, c.RedeemEvents)
	pnlEst := c.TotalSellUsdc + c.TotalMergeUsdc + c.TotalRedeemUsdc - c.TotalBuyUsdc
	fmt.Printf("pnl_estimate: $%.2f\n", pnlEst)
	fmt.Println()
	fmt.Print("states_visited: ")
	for i, s := range c.StatesVisited {
		if i > 0 {
			fmt.Print(" -> ")
		}
		fmt.Print(s)
	}
	fmt.Println()
	if len(c.PositiveEnvelopeLegs) > 0 {
		fmt.Printf("positive_envelope_legs (%d): %v\n", len(c.PositiveEnvelopeLegs), c.PositiveEnvelopeLegs)
	} else {
		fmt.Println("positive_envelope_legs: (none)")
	}
	if showLegs {
		fmt.Println()
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ORDER\tCOND_ID_SHORT\tMARKET_SLUG\tBUYS_A\tBUYS_B\tSHARES_A\tSHARES_B\tWAVG_A\tWAVG_B\tSUM_WAVG\tENVELOPE\tMERGE_UNITS\tREDEEM_USDC")
		for _, cid := range c.LegOrder {
			leg := c.Legs[cid]
			fmt.Fprintf(tw, "%d\t%s\t%s\t%d\t%d\t%.2f\t%.2f\t%.4f\t%.4f\t%.4f\t%v\t%.2f\t%.2f\n",
				leg.EntryOrder, shortID(leg.ConditionID), leg.MarketSlug,
				leg.BuysA, leg.BuysB,
				leg.SharesA, leg.SharesB,
				leg.WavgA(), leg.WavgB(), leg.SumWavg(),
				leg.EnvelopeAt(),
				leg.MergeUnits, leg.RedeemUsdc)
		}
		tw.Flush()
	}
}

func printTrace(c *replay.Cluster) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TS_UTC\tSTATE\tLEG\tSIDE\tCOND\tPRICE\tSIZE\tUSDC\tSHARES_A\tSHARES_B\tSUM_WAVG\tENV")
	for _, s := range c.Steps {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%.4f\t%.2f\t%.2f\t%.2f\t%.2f\t%.4f\t%v\n",
			s.TimestampUTC.Format("15:04:05"), s.State, s.LegOrder, s.Side,
			shortID(s.ConditionID),
			s.Price, s.Size, s.UsdcSize, s.SharesA, s.SharesB, s.SumWavg, s.EnvelopeAt)
	}
	tw.Flush()
}

func shortID(s string) string {
	if len(s) <= 10 {
		return s
	}
	return s[:10]
}
