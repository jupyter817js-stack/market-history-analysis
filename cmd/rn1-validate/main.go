// Command rn1-validate is the Phase F-0 Live <-> Historical
// Cross-Validation CLI. Per-cycle (`tick`) it scores every scan
// candidate against an RN1 historical-cohort profile, appends the
// likeness rows + envelope ticks for high-likeness candidates, and
// detects settlements as events drop from the scan past their start
// time. `report` prints a per-cycle summary; `evaluate` is reserved
// for a future PR that implements the full 8-criterion rubric.
//
// This tool is read-only and offline-only. It opens no network
// connections, no wallets, no signed transactions, no order
// placement endpoints. All paper-trade-like measurements use the
// `hypo_` prefix and live in append-only JSONL ledgers under the
// operator's chosen validation directory.
//
// See docs/rn1-phase-f0-cross-validation-design.md for the full
// design and the 8-criterion evaluation rubric that gates Phase F-1
// (paper engine) implementation.
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

	"rn1-strategy-extractor/internal/validation"
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
	case "report":
		runReport(args)
	case "evaluate":
		runEvaluate(args)
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "rn1-validate: unknown subcommand %q\n", sub)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `rn1-validate -- read-only Phase F-0 cross-validation CLI

Subcommands:
  tick      Score one cycle: append likeness rows + envelope ticks +
            detect settlements. Run once per scan cycle.
  report    Summarize the validation ledgers (band distribution,
            envelope coverage, settlement counts).
  evaluate  Emit the 8-criterion EvaluationSnapshot for the rubric
            (placeholder in PR-5; full implementation pending).

Run "rn1-validate <subcommand> -h" for subcommand flags.

This tool is read-only and offline-only. No network calls, no
wallets, no signed orders. All ledgers are append-only.`)
}

// ---------------------------------------------------------------------------
// tick
// ---------------------------------------------------------------------------

func runTick(args []string) {
	fs := flag.NewFlagSet("tick", flag.ExitOnError)
	var (
		profilePath          string
		cycleDir             string
		scanPath             string
		validationDir        string
		minLikenessTracking  float64
	)
	fs.StringVar(&profilePath, "historical-profile", "",
		"path to historical_profile.json (required)")
	fs.StringVar(&cycleDir, "cycle-dir", "",
		"cycle directory containing candidates.json "+
			"(if set, overrides --scan)")
	fs.StringVar(&scanPath, "scan", "",
		"path to candidates.json (required if no --cycle-dir)")
	fs.StringVar(&validationDir, "validation-dir", ".validation",
		"directory for the three append-only ledgers")
	fs.Float64Var(&minLikenessTracking, "min-likeness-tracking",
		validation.DefaultMinLikenessForTracking,
		"likeness threshold below which envelope ticks are NOT recorded")
	fs.Parse(args)

	if profilePath == "" {
		fmt.Fprintln(os.Stderr, "rn1-validate tick: --historical-profile is required")
		fs.Usage()
		os.Exit(2)
	}

	cycleTs := ""
	if cycleDir != "" {
		scanPath = filepath.Join(cycleDir, "candidates.json")
		cycleTs = filepath.Base(cycleDir)
	}
	if scanPath == "" {
		fmt.Fprintln(os.Stderr, "rn1-validate tick: --scan or --cycle-dir is required")
		fs.Usage()
		os.Exit(2)
	}
	if cycleTs == "" {
		// Derive cycle_ts from the scan path's parent directory.
		cycleTs = filepath.Base(filepath.Dir(scanPath))
	}

	profile, err := validation.LoadProfile(profilePath)
	if err != nil {
		slog.Error("load historical profile", "err", err)
		os.Exit(1)
	}

	in := validation.ProcessCycleInput{
		HistoricalProfile:      profile,
		ScanCandidatesPath:     scanPath,
		CycleTs:                cycleTs,
		LikenessLedgerPath:     filepath.Join(validationDir, "likeness_per_candidate.jsonl"),
		EnvelopeLedgerPath:     filepath.Join(validationDir, "envelope_tracking.jsonl"),
		SettlementLedgerPath:   filepath.Join(validationDir, "settlements.jsonl"),
		MinLikenessForTracking: minLikenessTracking,
	}
	if err := validation.EnsureValidationDir(in); err != nil {
		slog.Error("ensure validation dir", "err", err)
		os.Exit(1)
	}
	res, err := validation.ProcessCycle(in)
	if err != nil {
		slog.Error("process cycle", "err", err)
		os.Exit(1)
	}
	fmt.Printf("tick summary: cycle_ts=%s scan_candidates=%d likeness_recorded=%d prime=%d plausible=%d adjacent=%d non_rn1=%d envelope_ticks=%d settlements=%d\n",
		res.CycleTs, res.ScanCandidates, res.LikenessRecorded,
		res.PrimeCount, res.PlausibleCount, res.AdjacentCount, res.NonRn1Count,
		res.EnvelopeTicksAppended, res.SettlementsDetected)
}

// ---------------------------------------------------------------------------
// report
// ---------------------------------------------------------------------------

func runReport(args []string) {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	var (
		validationDir string
		format        string
		topN          int
	)
	fs.StringVar(&validationDir, "validation-dir", ".validation", "directory containing the ledgers")
	fs.StringVar(&format, "format", "table", "table | json")
	fs.IntVar(&topN, "top-n", 30, "number of top-likeness latest-cycle rows to show")
	fs.Parse(args)

	likeness, err := validation.ReadLikenesses(filepath.Join(validationDir, "likeness_per_candidate.jsonl"))
	if err != nil {
		slog.Error("read likeness ledger", "err", err)
		os.Exit(1)
	}
	ticks, err := validation.ReadTicks(filepath.Join(validationDir, "envelope_tracking.jsonl"))
	if err != nil {
		slog.Error("read envelope ledger", "err", err)
		os.Exit(1)
	}
	settle, err := validation.ReadSettlements(filepath.Join(validationDir, "settlements.jsonl"))
	if err != nil {
		slog.Error("read settlements ledger", "err", err)
		os.Exit(1)
	}

	// Band distribution from likeness ledger.
	bandCount := map[validation.LikenessBand]int{}
	for _, r := range likeness {
		bandCount[r.Score.Band]++
	}
	// Latest cycle: pick the most recent CycleTs in likeness.
	var latestCycle string
	for _, r := range likeness {
		if r.CycleTs > latestCycle {
			latestCycle = r.CycleTs
		}
	}
	var latestRows []validation.LikenessRecord
	for _, r := range likeness {
		if r.CycleTs == latestCycle {
			latestRows = append(latestRows, r)
		}
	}
	sort.SliceStable(latestRows, func(i, j int) bool {
		return latestRows[i].Score.Total > latestRows[j].Score.Total
	})
	if topN > 0 && len(latestRows) > topN {
		latestRows = latestRows[:topN]
	}

	// Envelope: count unique (event, condition) and depth stats.
	legs := validation.BuildLegStates(ticks)
	envelopeCount := len(legs)
	envelopeBelow1 := 0
	for _, leg := range legs {
		if leg.MinHypoSumWavg < 1.0 && leg.TickCount > 0 {
			envelopeBelow1++
		}
	}

	// Settlements: latest per event.
	latestSettlements := validation.LatestSettlementByEvent(settle)

	if strings.ToLower(format) == "json" {
		out := map[string]any{
			"latest_cycle":         latestCycle,
			"likeness_records":     len(likeness),
			"band_distribution":    bandCount,
			"envelope_legs":        envelopeCount,
			"envelope_below_1_0":   envelopeBelow1,
			"settlement_records":   len(settle),
			"settlement_events":    len(latestSettlements),
			"latest_cycle_top":     latestRows,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
		return
	}

	fmt.Printf("=== rn1-validate report ===\n")
	fmt.Printf("validation-dir:    %s\n", validationDir)
	fmt.Printf("latest cycle:      %s\n", latestCycle)
	fmt.Printf("likeness records:  %d\n", len(likeness))
	fmt.Printf("  band PRIME:      %d\n", bandCount[validation.BandRn1Prime])
	fmt.Printf("  band PLAUSIBLE:  %d\n", bandCount[validation.BandRn1Plausible])
	fmt.Printf("  band ADJACENT:   %d\n", bandCount[validation.BandRn1Adjacent])
	fmt.Printf("  band NON_RN1:    %d\n", bandCount[validation.BandNonRn1])
	fmt.Printf("envelope legs:     %d (below 1.0 SumWavg: %d)\n", envelopeCount, envelopeBelow1)
	fmt.Printf("settlement events: %d (across %d ledger rows)\n", len(latestSettlements), len(settle))
	fmt.Println()
	fmt.Printf("=== top %d candidates in latest cycle (by likeness) ===\n", len(latestRows))
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "event_slug\tsport\tlikeness\tband\treason")
	for _, r := range latestRows {
		reason := "-"
		if len(r.Score.Reasons) > 0 {
			reason = r.Score.Reasons[0]
		}
		if len(reason) > 60 {
			reason = reason[:57] + "..."
		}
		fmt.Fprintf(tw, "%s\t%s\t%.3f\t%s\t%s\n",
			r.EventSlug, orDash(r.SportPrefix), r.Score.Total, r.Score.Band, reason)
	}
	_ = tw.Flush()
}

// ---------------------------------------------------------------------------
// evaluate (placeholder)
// ---------------------------------------------------------------------------

func runEvaluate(args []string) {
	fs := flag.NewFlagSet("evaluate", flag.ExitOnError)
	var validationDir string
	fs.StringVar(&validationDir, "validation-dir", ".validation", "directory containing the ledgers")
	fs.Parse(args)

	// PR-5 emits a stub snapshot. The full 8-criterion rubric
	// computation lives in a future PR (PR-6) once the operator
	// has accumulated 4-6 weeks of ledger data.
	likeness, _ := validation.ReadLikenesses(filepath.Join(validationDir, "likeness_per_candidate.jsonl"))
	ticks, _ := validation.ReadTicks(filepath.Join(validationDir, "envelope_tracking.jsonl"))
	settle, _ := validation.ReadSettlements(filepath.Join(validationDir, "settlements.jsonl"))

	legs := validation.BuildLegStates(ticks)
	below1 := 0
	for _, leg := range legs {
		if leg.MinHypoSumWavg < 1.0 && leg.TickCount > 0 {
			below1++
		}
	}
	rate := 0.0
	if len(legs) > 0 {
		rate = float64(below1) / float64(len(legs))
	}

	snap := validation.EvaluationSnapshot{
		GeneratedAt:            time.Now().UTC(),
		TotalCandidatesScored:  len(likeness),
		TotalEnvelopeWindows:   len(legs),
		TotalSettlements:       len(settle),
		EnvelopeOccurrenceRate: rate,
		CiInvariantViolations:  0,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(snap)

	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Note: this is a PR-5 placeholder. Full 8-criterion rubric")
	fmt.Fprintln(os.Stderr, "(median SumWavg, hypo_net_pnl, family/prefix diversification,")
	fmt.Fprintln(os.Stderr, "KS-test, etc.) is pending PR-6.")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
