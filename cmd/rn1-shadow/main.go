// Command rn1-shadow is the read-only Paper / Shadow Mode CLI
// (Phase 1). It maintains an append-only JSONL log tracking
// operator-selected candidates (WATCH /
// NEEDS_LIQUIDITY_REVIEW / NEEDS_RESCAN / DISCOVERED / REJECTED
// / etc.) over multiple rn1-live-scan / rn1-live-sim /
// rn1-live-diff observation ticks.
//
// This is NOT a trading bot. It does not place orders, does not
// sign anything, does not read or write a wallet, does not call
// any authenticated endpoint, does not maintain real or
// simulated positions, does not compute real or simulated PnL,
// and does not recommend buying or selling. See
// docs/rn1-paper-shadow-mode-phase1.md for the full safety
// statement and lifecycle.
//
// Subcommands: add, update, report. Each subcommand is
// offline-only.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"rn1-strategy-extractor/internal/scanner"
	"rn1-strategy-extractor/internal/shadow"
	"rn1-strategy-extractor/internal/simulator"
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
	case "add":
		runAdd(args)
	case "update":
		runUpdate(args)
	case "report":
		runReport(args)
	case "cycle":
		runCycle(args)
	case "review-queue":
		runReviewQueue(args)
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "rn1-shadow: unknown subcommand %q\n", sub)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `rn1-shadow — read-only paper/shadow tracker (Phase 1)

Per-slug subcommands:
  add           Add an initial shadow record for a new event_slug.
  update        Append a new observation tick to an existing record.

Per-cycle subcommands (bulk):
  cycle         Walk an entire cycle's scan/sim/diff and bulk-apply
                Add (for new slugs that pass the discover filter) +
                Update (for already-tracked slugs).
  review-queue  Print slugs currently waiting on operator action
                (DISCOVERED / NEEDS_RESCAN / NEEDS_LIQUIDITY_REVIEW
                by default).

Read-only:
  report        Print a per-shadow_id summary.

Run "rn1-shadow <subcommand> -h" for subcommand flags.`)
}

// ---------------------------------------------------------------------------
// subcommand: add
// ---------------------------------------------------------------------------

func runAdd(args []string) {
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	var (
		logPath      string
		scanPath     string
		simPath      string
		diffPath     string
		eventSlug    string
		decision     string
		reviewerNote string
	)
	fs.StringVar(&logPath, "shadow-log", "", "JSONL shadow log path (required)")
	fs.StringVar(&scanPath, "scan", "", "rn1-live-scan JSON path (required)")
	fs.StringVar(&simPath, "sim", "", "rn1-live-sim JSON path (optional)")
	fs.StringVar(&diffPath, "diff", "", "rn1-live-diff JSON path (optional)")
	fs.StringVar(&eventSlug, "event-slug", "", "event_slug to track (required)")
	fs.StringVar(&decision, "decision", "", "initial closed-list decision (required): "+joinDecisions())
	fs.StringVar(&reviewerNote, "note", "", "free-form reviewer note (optional)")
	fs.Parse(args)

	if logPath == "" || scanPath == "" || eventSlug == "" || decision == "" {
		fmt.Fprintln(os.Stderr, "rn1-shadow add: --shadow-log, --scan, --event-slug, --decision are required")
		fs.Usage()
		os.Exit(2)
	}

	obs, err := loadObservation(scanPath, simPath, diffPath, eventSlug, sub_addSource(args))
	if err != nil {
		slog.Error("load observation", "err", err)
		os.Exit(1)
	}
	rec, err := shadow.Add(shadow.AddInput{
		LogPath:      logPath,
		EventSlug:    eventSlug,
		Decision:     shadow.ShadowDecision(decision),
		ReviewerNote: reviewerNote,
		Obs:          obs,
	})
	if err != nil {
		slog.Error("shadow add", "err", err)
		os.Exit(1)
	}
	fmt.Printf("added shadow_id=%s decision=%s gate=%s\n", rec.ShadowID, rec.ShadowDecision, rec.LiquidityGate)
}

// ---------------------------------------------------------------------------
// subcommand: update
// ---------------------------------------------------------------------------

func runUpdate(args []string) {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	var (
		logPath      string
		scanPath     string
		simPath      string
		diffPath     string
		eventSlug    string
		decision     string
		reviewerNote string
	)
	fs.StringVar(&logPath, "shadow-log", "", "JSONL shadow log path (required)")
	fs.StringVar(&scanPath, "scan", "", "rn1-live-scan JSON path (required)")
	fs.StringVar(&simPath, "sim", "", "rn1-live-sim JSON path (optional)")
	fs.StringVar(&diffPath, "diff", "", "rn1-live-diff JSON path (optional)")
	fs.StringVar(&eventSlug, "event-slug", "", "event_slug to update (required)")
	fs.StringVar(&decision, "decision", "", "optional new closed-list decision: "+joinDecisions()+
		" (empty = keep prior unless CLOSED_EVENT auto-rule fires)")
	fs.StringVar(&reviewerNote, "note", "", "free-form reviewer note (optional)")
	fs.Parse(args)

	if logPath == "" || scanPath == "" || eventSlug == "" {
		fmt.Fprintln(os.Stderr, "rn1-shadow update: --shadow-log, --scan, --event-slug are required")
		fs.Usage()
		os.Exit(2)
	}

	obs, err := loadObservation(scanPath, simPath, diffPath, eventSlug, sub_updateSource(args))
	if err != nil {
		slog.Error("load observation", "err", err)
		os.Exit(1)
	}
	rec, err := shadow.Update(shadow.UpdateInput{
		LogPath:      logPath,
		EventSlug:    eventSlug,
		Decision:     shadow.ShadowDecision(decision),
		ReviewerNote: reviewerNote,
		Obs:          obs,
	})
	if err != nil {
		slog.Error("shadow update", "err", err)
		os.Exit(1)
	}
	fmt.Printf("updated shadow_id=%s decision=%s gate=%s\n", rec.ShadowID, rec.ShadowDecision, rec.LiquidityGate)
}

// ---------------------------------------------------------------------------
// subcommand: report
// ---------------------------------------------------------------------------

func runReport(args []string) {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	var (
		logPath   string
		format    string
		outPath   string
		decision  string
		eventSlug string
	)
	fs.StringVar(&logPath, "shadow-log", "", "JSONL shadow log path (required)")
	fs.StringVar(&format, "format", "table", "output format: table|json")
	fs.StringVar(&outPath, "out", "", "output path (default stdout)")
	fs.StringVar(&decision, "decision", "", "optional filter by closed-list decision")
	fs.StringVar(&eventSlug, "event-slug", "", "optional filter by event_slug")
	fs.Parse(args)

	if logPath == "" {
		fmt.Fprintln(os.Stderr, "rn1-shadow report: --shadow-log is required")
		fs.Usage()
		os.Exit(2)
	}
	records, err := shadow.ReadLog(logPath)
	if err != nil {
		slog.Error("read log", "err", err)
		os.Exit(1)
	}
	filt := shadow.ReportFilter{EventSlug: eventSlug}
	if decision != "" {
		if !shadow.IsValidDecision(shadow.ShadowDecision(decision)) {
			slog.Error("invalid --decision filter", "value", decision)
			os.Exit(2)
		}
		filt.Decision = shadow.ShadowDecision(decision)
	}
	summaries := shadow.Report(records, filt)

	out, err := openOutput(outPath)
	if err != nil {
		slog.Error("open output", "err", err)
		os.Exit(1)
	}
	defer out.Close()

	switch strings.ToLower(format) {
	case "json":
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(summaries)
	default:
		printReportTable(out, summaries)
	}
}

func printReportTable(w io.Writer, ss []shadow.ShadowSummary) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "shadow_id\tevent_slug\tsport\tdecision\tticks\tlatest_score\tlatest_gate\tlast_seen_at\tnote")
	for _, s := range ss {
		note := s.LatestReviewerNote
		if len(note) > 60 {
			note = note[:57] + "..."
		}
		if note == "" {
			note = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%.2f\t%s\t%s\t%s\n",
			s.ShadowID,
			s.EventSlug,
			orDash(s.SportPrefix),
			s.CurrentDecision,
			s.TickCount,
			s.LatestSelectorScore,
			orDash(s.LatestLiquidityGate),
			s.LastSeenAt.UTC().Format(time.RFC3339),
			note,
		)
	}
	_ = tw.Flush()
}

// ---------------------------------------------------------------------------
// subcommand: cycle
// ---------------------------------------------------------------------------

func runCycle(args []string) {
	fs := flag.NewFlagSet("cycle", flag.ExitOnError)
	var (
		logPath       string
		cycleDir      string
		scanPath      string
		simPath       string
		diffPath      string
		discoverFlag  bool
		newDecision   string
		minScore      float64
		bandsStr      string
		gatesStr      string
		maxDiscover   int
	)
	fs.StringVar(&logPath, "shadow-log", "", "JSONL shadow log path (required)")
	fs.StringVar(&cycleDir, "cycle-dir", "",
		"cycle directory containing candidates.json/traces.json/diff_vs_latest.json "+
			"(if set, overrides --scan/--sim/--diff with the standard file names)")
	fs.StringVar(&scanPath, "scan", "", "rn1-live-scan JSON path (required if no --cycle-dir)")
	fs.StringVar(&simPath, "sim", "", "rn1-live-sim JSON path (optional)")
	fs.StringVar(&diffPath, "diff", "", "rn1-live-diff JSON path (optional)")
	fs.BoolVar(&discoverFlag, "discover", false,
		"auto-add new candidates that pass the filter (default off — only tick existing)")
	fs.StringVar(&newDecision, "new-decision", string(shadow.DecisionDiscovered),
		"closed-list decision to assign to auto-added candidates: "+joinDecisions())
	fs.Float64Var(&minScore, "min-score", 0,
		"auto-discover floor on Candidate.Score (0 = no floor)")
	fs.StringVar(&bandsStr, "bands", "",
		"comma-separated band whitelist for auto-discover (e.g. very_strong,strong); empty = any")
	fs.StringVar(&gatesStr, "gates", "",
		"comma-separated gate whitelist for auto-discover (e.g. PASS,WARN); empty = any")
	fs.IntVar(&maxDiscover, "max-discover", 0,
		"cap auto-discoveries per call (0 = no cap)")
	fs.Parse(args)

	if logPath == "" {
		fmt.Fprintln(os.Stderr, "rn1-shadow cycle: --shadow-log is required")
		fs.Usage()
		os.Exit(2)
	}
	// Resolve cycle-dir vs explicit file paths.
	if cycleDir != "" {
		scanPath = filepath.Join(cycleDir, "candidates.json")
		// Only attach optional files if they exist.
		if simCand := filepath.Join(cycleDir, "traces.json"); fileExists(simCand) {
			simPath = simCand
		}
		if diffCand := filepath.Join(cycleDir, "diff_vs_latest.json"); fileExists(diffCand) {
			diffPath = diffCand
		}
	}
	if scanPath == "" {
		fmt.Fprintln(os.Stderr, "rn1-shadow cycle: --scan (or --cycle-dir) is required")
		fs.Usage()
		os.Exit(2)
	}

	filter := shadow.DiscoverFilter{
		Enabled:       discoverFlag,
		MinScore:      minScore,
		Bands:         parseLowerSet(bandsStr),
		Gates:         parseUpperSet(gatesStr),
		MaxCandidates: maxDiscover,
	}
	if newDecision != "" {
		if !shadow.IsValidDecision(shadow.ShadowDecision(newDecision)) {
			fmt.Fprintf(os.Stderr, "rn1-shadow cycle: invalid --new-decision %q\n", newDecision)
			os.Exit(2)
		}
		filter.NewDecision = shadow.ShadowDecision(newDecision)
	}

	res, err := shadow.Cycle(shadow.CycleInput{
		LogPath:        logPath,
		ScanPath:       scanPath,
		SimPath:        simPath,
		DiffPath:       diffPath,
		DiscoverFilter: filter,
		AppendSource:   "rn1-shadow cycle",
	})
	if err != nil {
		slog.Error("shadow cycle", "err", err)
		os.Exit(1)
	}

	fmt.Printf("cycle summary: scan_candidates=%d already_tracked=%d discovered=%d ticked=%d auto_closed=%d skipped_filter=%d skipped_cap=%d\n",
		res.Counts.ScanCandidates,
		res.Counts.AlreadyTracked,
		res.Counts.Discovered,
		res.Counts.Ticked,
		res.Counts.AutoClosed,
		res.Counts.SkippedFilter,
		res.Counts.SkippedCap,
	)
	if len(res.AutoClosed) > 0 {
		fmt.Println("auto-closed (event past start / Closed=true):")
		for _, r := range res.AutoClosed {
			fmt.Printf("  %s  (decision=%s)\n", r.EventSlug, r.ShadowDecision)
		}
	}
}

// ---------------------------------------------------------------------------
// subcommand: review-queue
// ---------------------------------------------------------------------------

func runReviewQueue(args []string) {
	fs := flag.NewFlagSet("review-queue", flag.ExitOnError)
	var (
		logPath          string
		format           string
		outPath          string
		decisionsStr     string
		startingWithinH  int
		minScore         float64
	)
	fs.StringVar(&logPath, "shadow-log", "", "JSONL shadow log path (required)")
	fs.StringVar(&format, "format", "table", "output format: table|json")
	fs.StringVar(&outPath, "out", "", "output path (default stdout)")
	fs.StringVar(&decisionsStr, "decisions", "",
		"comma-separated decision filter; empty = DISCOVERED,NEEDS_RESCAN,NEEDS_LIQUIDITY_REVIEW")
	fs.IntVar(&startingWithinH, "starting-within-hours", 0,
		"only events whose StartTs is within N hours of now (0 = no time filter)")
	fs.Float64Var(&minScore, "min-score", 0, "minimum latest selector score (0 = no floor)")
	fs.Parse(args)

	if logPath == "" {
		fmt.Fprintln(os.Stderr, "rn1-shadow review-queue: --shadow-log is required")
		fs.Usage()
		os.Exit(2)
	}

	records, err := shadow.ReadLog(logPath)
	if err != nil {
		slog.Error("read log", "err", err)
		os.Exit(1)
	}

	filter := shadow.ReviewQueueFilter{
		MinScore: minScore,
	}
	if startingWithinH > 0 {
		filter.StartingWithin = time.Duration(startingWithinH) * time.Hour
	}
	if decisionsStr != "" {
		for _, d := range strings.Split(decisionsStr, ",") {
			d = strings.TrimSpace(d)
			if d == "" {
				continue
			}
			if !shadow.IsValidDecision(shadow.ShadowDecision(d)) {
				fmt.Fprintf(os.Stderr, "rn1-shadow review-queue: invalid decision %q\n", d)
				os.Exit(2)
			}
			filter.Decisions = append(filter.Decisions, shadow.ShadowDecision(d))
		}
	}

	queue := shadow.ReviewQueue(records, filter)

	out, err := openOutput(outPath)
	if err != nil {
		slog.Error("open output", "err", err)
		os.Exit(1)
	}
	defer out.Close()

	switch strings.ToLower(format) {
	case "json":
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(queue)
	default:
		printReviewQueueTable(out, queue)
	}
}

func printReviewQueueTable(w io.Writer, ss []shadow.ShadowSummary) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "event_slug\tsport\tdecision\tscore\tgate\tticks\tnote")
	for _, s := range ss {
		note := s.LatestReviewerNote
		if len(note) > 50 {
			note = note[:47] + "..."
		}
		if note == "" {
			note = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%.2f\t%s\t%d\t%s\n",
			s.EventSlug,
			orDash(s.SportPrefix),
			s.CurrentDecision,
			s.LatestSelectorScore,
			orDash(s.LatestLiquidityGate),
			s.TickCount,
			note,
		)
	}
	_ = tw.Flush()
}

func parseLowerSet(s string) map[string]bool {
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

func parseUpperSet(s string) map[string]bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	out := map[string]bool{}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(strings.ToUpper(p))
		if p != "" {
			out[p] = true
		}
	}
	return out
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// loadObservation reads the scan JSON (required), simulator JSON
// (optional), and diff JSON (optional), and extracts the matching
// candidate / trace / diff classes for one event_slug.
func loadObservation(scanPath, simPath, diffPath, eventSlug, appendSrc string) (shadow.Observation, error) {
	var obs shadow.Observation
	obs.Now = time.Now().UTC()
	obs.AppendSource = appendSrc

	scanBody, err := os.ReadFile(scanPath)
	if err != nil {
		return obs, fmt.Errorf("read scan %q: %w", scanPath, err)
	}
	var cands []scanner.Candidate
	if err := json.Unmarshal(scanBody, &cands); err != nil {
		return obs, fmt.Errorf("decode scan JSON %q: %w", scanPath, err)
	}
	for i := range cands {
		if cands[i].EventSlug == eventSlug {
			obs.Candidate = &cands[i]
			break
		}
	}
	if obs.Candidate == nil {
		return obs, fmt.Errorf("event_slug %q not found in scan %q", eventSlug, scanPath)
	}

	if simPath != "" {
		simBody, err := os.ReadFile(simPath)
		if err != nil {
			return obs, fmt.Errorf("read sim %q: %w", simPath, err)
		}
		var traces []simulator.Trace
		if err := json.Unmarshal(simBody, &traces); err != nil {
			return obs, fmt.Errorf("decode sim JSON %q: %w", simPath, err)
		}
		for i := range traces {
			if traces[i].EventSlug == eventSlug {
				obs.Trace = &traces[i]
				break
			}
		}
	}

	if diffPath != "" {
		diffBody, err := os.ReadFile(diffPath)
		if err != nil {
			return obs, fmt.Errorf("read diff %q: %w", diffPath, err)
		}
		// Diff JSON is an array of records each with EventSlug
		// and DiffClasses. We only need the classes for our slug.
		var diffRecs []struct {
			EventSlug   string   `json:"EventSlug"`
			DiffClasses []string `json:"DiffClasses"`
		}
		if err := json.Unmarshal(diffBody, &diffRecs); err != nil {
			return obs, fmt.Errorf("decode diff JSON %q: %w", diffPath, err)
		}
		for _, d := range diffRecs {
			if d.EventSlug == eventSlug {
				obs.DiffClassesSince = append([]string(nil), d.DiffClasses...)
				break
			}
		}
	}
	return obs, nil
}

func joinDecisions() string {
	parts := make([]string, 0, len(shadow.AllShadowDecisions))
	for _, d := range shadow.AllShadowDecisions {
		parts = append(parts, string(d))
	}
	return strings.Join(parts, " | ")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

type writeCloser struct {
	io.Writer
	c io.Closer
}

func (w writeCloser) Close() error {
	if w.c == nil {
		return nil
	}
	return w.c.Close()
}

func openOutput(path string) (io.WriteCloser, error) {
	if path == "" {
		return writeCloser{Writer: os.Stdout}, nil
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// sub_addSource / sub_updateSource build an audit string for the
// AppendSource field. We deliberately do NOT capture full
// os.Args (which could leak paths); just the subcommand label.
func sub_addSource(args []string) string {
	_ = args
	return "rn1-shadow add"
}
func sub_updateSource(args []string) string {
	_ = args
	return "rn1-shadow update"
}
