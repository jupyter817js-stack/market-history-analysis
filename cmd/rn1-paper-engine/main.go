// Command rn1-paper-engine is the Phase F-1 paper strategy engine
// CLI. Per-cycle (`tick`) it runs the entry decision against the
// latest scanner output, appends paper fills + merges + redemptions
// to the .paper/ ledgers, and rebuilds pnl_summary.json.
//
// This tool is paper-only. It opens no network connections, no
// wallets, no on-chain transactions, no signed orders. Every
// monetary field uses the `paper_` prefix. The boundary to live
// execution (Phase F-2) lives in a separate project; this binary
// will never cross it.
//
// See docs/rn1-phase2-paper-engine-design.md and docs/rn1-phase-
// f0-cross-validation-design.md for the full design and the
// evaluation rubric that gates Phase F-2.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"rn1-strategy-extractor/internal/engine"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	sub := os.Args[1]
	args := os.Args[2:]

	switch sub {
	case "tick":
		runTick(args)
	case "status":
		runStatus(args)
	case "positions":
		runPositions(args)
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "rn1-paper-engine: unknown subcommand %q\n", sub)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `rn1-paper-engine -- Phase F-1 paper strategy engine CLI

Subcommands:
  tick       Run one engine cycle: read latest scan, append fills /
             merges / redemptions, rebuild pnl_summary.json.
  status     Print pnl_summary.json contents (table or json).
  positions  List current paper positions (one row per event).

Run "rn1-paper-engine <subcommand> -h" for subcommand flags.

This tool is paper-only. No wallets, no signed orders, no
on-chain transactions. All ledgers are append-only.`)
}

// ---------------------------------------------------------------------------
// tick
// ---------------------------------------------------------------------------

func runTick(args []string) {
	fs := flag.NewFlagSet("tick", flag.ExitOnError)
	var (
		cycleDir        string
		scanPath        string
		paperDir        string
		maxPerEventUsd  float64
		maxActiveEvents int
		maxDailyNew     int
		stopLossUsdc    float64
		dailyKillUsdc   float64
		minEntryHrs     float64
		maxEntryHrs     float64
	)
	fs.StringVar(&cycleDir, "cycle-dir", "",
		"cycle directory containing candidates.json (overrides --scan)")
	fs.StringVar(&scanPath, "scan", "",
		"path to candidates.json (required if no --cycle-dir)")
	fs.StringVar(&paperDir, "paper-dir", ".paper",
		"directory for the engine's append-only ledgers + pnl summary")
	fs.Float64Var(&maxPerEventUsd, "max-per-event-usdc",
		engine.DefaultSizingLimits().MaxPerEventUsdc,
		"cap on paper notional per event (USDC)")
	fs.IntVar(&maxActiveEvents, "max-active-events",
		engine.DefaultSizingLimits().MaxActivePaperEvents,
		"cap on concurrent active paper positions")
	fs.IntVar(&maxDailyNew, "max-daily-new",
		engine.DefaultSizingLimits().MaxDailyNewEntries,
		"cap on new event entries opened per rolling 24h")
	fs.Float64Var(&stopLossUsdc, "stop-loss-usdc",
		engine.DefaultRiskLimits().PerEventStopLossUsdc,
		"per-event paper PnL floor (more negative => stop fires sooner)")
	fs.Float64Var(&dailyKillUsdc, "daily-kill-usdc",
		engine.DefaultRiskLimits().DailyDrawdownKillUsdc,
		"rolling 24h paper PnL floor that pauses NEW entries")
	fs.Float64Var(&minEntryHrs, "entry-min-hours",
		engine.DefaultEntryWindow().MinHoursBeforeStart,
		"minimum hours-before-start for PROBE eligibility")
	fs.Float64Var(&maxEntryHrs, "entry-max-hours",
		engine.DefaultEntryWindow().MaxHoursBeforeStart,
		"maximum hours-before-start for PROBE eligibility")
	fs.Parse(args)

	cycleTs := ""
	if cycleDir != "" {
		scanPath = filepath.Join(cycleDir, "candidates.json")
		cycleTs = filepath.Base(cycleDir)
	}
	if scanPath == "" {
		fmt.Fprintln(os.Stderr, "rn1-paper-engine tick: --scan or --cycle-dir is required")
		fs.Usage()
		os.Exit(2)
	}
	if cycleTs == "" {
		cycleTs = filepath.Base(filepath.Dir(scanPath))
	}

	if err := engine.EnsurePaperDir(paperDir); err != nil {
		slog.Error("ensure paper dir", "err", err)
		os.Exit(1)
	}

	in := engine.ProcessCycleInput{
		ScanCandidatesPath: scanPath,
		CycleTs:            cycleTs,
		PaperDir:           paperDir,
		Limits: engine.SizingLimits{
			MaxPerEventUsdc:      maxPerEventUsd,
			MaxActivePaperEvents: maxActiveEvents,
			MaxDailyNewEntries:   maxDailyNew,
			MinTopDepthForFull:   engine.DefaultSizingLimits().MinTopDepthForFull,
			MaxSpreadAcceptable:  engine.DefaultSizingLimits().MaxSpreadAcceptable,
		},
		EntryWindow: engine.EntryWindow{
			MinHoursBeforeStart: minEntryHrs,
			MaxHoursBeforeStart: maxEntryHrs,
		},
		RiskLimits: engine.RiskLimits{
			PerEventStopLossUsdc:  stopLossUsdc,
			DailyDrawdownKillUsdc: dailyKillUsdc,
		},
	}
	res, err := engine.ProcessCycle(in)
	if err != nil {
		slog.Error("process cycle", "err", err)
		os.Exit(1)
	}
	fmt.Printf("tick summary: cycle_ts=%s scan_candidates=%d entries_opened=%d entries_expanded=%d entries_skipped=%d fills=%d merges=%d redemptions=%d stopped=%d daily_kill=%v paper_net_pnl_usdc=%.4f\n",
		res.CycleTs, res.ScanCandidates,
		res.EntriesOpened, res.EntriesExpanded, res.EntriesSkipped,
		res.FillsAppended, res.MergesTriggered, res.RedemptionsTriggered,
		res.PositionsStopped, res.DailyKillActive, res.PaperNetPnlUsdc)
}

// ---------------------------------------------------------------------------
// status
// ---------------------------------------------------------------------------

func runStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	var (
		paperDir string
		format   string
	)
	fs.StringVar(&paperDir, "paper-dir", ".paper", "directory containing the ledgers")
	fs.StringVar(&format, "format", "table", "table | json")
	fs.Parse(args)

	body, err := os.ReadFile(filepath.Join(paperDir, "pnl_summary.json"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "no pnl_summary.json yet; run `tick` first")
		os.Exit(0)
	}
	if strings.ToLower(format) == "json" {
		fmt.Println(string(body))
		return
	}
	var s engine.PaperPnlSummary
	if err := json.Unmarshal(body, &s); err != nil {
		slog.Error("decode summary", "err", err)
		os.Exit(1)
	}
	fmt.Printf("=== rn1-paper-engine status ===\n")
	fmt.Printf("paper-dir:               %s\n", paperDir)
	fmt.Printf("generated_at:            %s\n", s.GeneratedAt.Format(time.RFC3339))
	fmt.Printf("lifetime fills:          %d\n", s.LifetimeFillsCount)
	fmt.Printf("lifetime merges:         %d\n", s.LifetimeMergesCount)
	fmt.Printf("lifetime redemptions:    %d\n", s.LifetimeRedemptionsCount)
	fmt.Printf("lifetime events touched: %d\n", s.LifetimeEventsTouched)
	fmt.Printf("paper total cost (USDC): %.4f\n", s.PaperTotalCostUsdc)
	fmt.Printf("paper realized   (USDC): %.4f\n", s.PaperRealizedUsdc)
	fmt.Printf("paper merge PnL  (USDC): %.4f\n", s.PaperMergePnlUsdc)
	fmt.Printf("paper redeem PnL (USDC): %.4f\n", s.PaperRedeemPnlUsdc)
	fmt.Printf("paper NET PnL    (USDC): %+.4f  (%.2f%%)\n", s.PaperNetPnlUsdc, s.PaperReturnPct)
	fmt.Printf("positions: open=%d stopped=%d idle=%d\n",
		s.OpenPositionsCount, s.StoppedPositionsCount, s.IdlePositionsCount)
	if len(s.PaperPnlBySportPrefix) > 0 {
		fmt.Println()
		fmt.Println("=== paper PnL by sport prefix ===")
		type kv struct {
			k string
			v float64
		}
		var rows []kv
		for k, v := range s.PaperPnlBySportPrefix {
			rows = append(rows, kv{k, v})
		}
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].v > rows[j].v })
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "prefix\tpaper_realized_pnl_usdc")
		for _, r := range rows {
			fmt.Fprintf(tw, "%s\t%+.4f\n", r.k, r.v)
		}
		_ = tw.Flush()
	}
}

// ---------------------------------------------------------------------------
// positions
// ---------------------------------------------------------------------------

func runPositions(args []string) {
	fs := flag.NewFlagSet("positions", flag.ExitOnError)
	var (
		paperDir string
		phase    string
	)
	fs.StringVar(&paperDir, "paper-dir", ".paper", "directory containing the ledgers")
	fs.StringVar(&phase, "phase", "", "optional phase filter (paper_open_b, paper_stopped, etc.)")
	fs.Parse(args)

	fills, err := engine.ReadFills(filepath.Join(paperDir, "fills.jsonl"))
	if err != nil {
		slog.Error("read fills", "err", err)
		os.Exit(1)
	}
	merges, _ := engine.ReadMerges(filepath.Join(paperDir, "merges.jsonl"))
	redemptions, _ := engine.ReadRedemptions(filepath.Join(paperDir, "redemptions.jsonl"))
	positions := engine.BuildPaperPositions(fills)
	applyMergesAndRedemptions(positions, merges, redemptions)

	var rows []*engine.PaperPosition
	for _, p := range positions {
		if phase != "" && string(p.CurrentPhase) != phase {
			continue
		}
		rows = append(rows, p)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].PaperRealizedPnlUsdc > rows[j].PaperRealizedPnlUsdc
	})

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "event_slug\tsport\tphase\tlegs\tpaper_cost\tpaper_realized_pnl")
	for _, p := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%.4f\t%+.4f\n",
			p.EventSlug, p.SportPrefix, p.CurrentPhase, len(p.Legs),
			p.PaperTotalCostUsdc, p.PaperRealizedPnlUsdc)
	}
	_ = tw.Flush()
}

// applyMergesAndRedemptions is a tiny local helper mirroring the
// engine's applyRealizedPnl (which is unexported). Keeps the
// status / positions commands honest with what tick produces.
func applyMergesAndRedemptions(positions map[string]*engine.PaperPosition,
	merges []engine.PaperMerge, redemptions []engine.PaperRedemption) {
	for _, m := range merges {
		if p, ok := positions[m.EventSlug]; ok {
			p.PaperMergePnlUsdc += m.PaperMergePnlUsdc
			p.PaperRealizedPnlUsdc += m.PaperMergePnlUsdc
		}
	}
	for _, r := range redemptions {
		if p, ok := positions[r.EventSlug]; ok {
			p.PaperRedeemPnlUsdc += r.PaperRedeemPnlUsdc
			p.PaperRealizedPnlUsdc += r.PaperRedeemPnlUsdc
		}
	}
}
