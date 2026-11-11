// Command rn1-live-diff is the read-only two-way Cross-Scan
// Diff CLI (M5 Phase 1). It compares two rn1-live-scan JSON
// snapshots (and optional rn1-live-sim trace snapshots) and
// emits per-candidate observational diff records.
//
// The diff is offline: zero network calls, zero authentication,
// no signer, no wallet, no order placement, no buy/sell
// language, no PnL forecast, no arbitrage claim. Diff classes
// and review notes are observational — see
// docs/rn1-cross-scan-diff-design.md and
// docs/rn1-cross-scan-diff-phase1.md.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"rn1-strategy-extractor/internal/diff"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	var (
		scanA, scanB    string
		simA, simB      string
		outFormat       string
		outPath         string
		limit           int
		classFilter     string
		includeQuiet    bool
		includeNew      bool
		includeDropped  bool
		includeTerminal bool
		spreadDelta     float64
		coverageDelta   float64
		depthAbsDelta   float64
		depthPctDelta   float64
		maxAgeHours     int
	)
	flag.StringVar(&scanA, "scan-a", "", "scanner JSON path for snapshot A (required)")
	flag.StringVar(&scanB, "scan-b", "", "scanner JSON path for snapshot B (required, must be later than A)")
	flag.StringVar(&simA, "sim-a", "", "simulator trace JSON path for A (optional)")
	flag.StringVar(&simB, "sim-b", "", "simulator trace JSON path for B (optional)")
	flag.StringVar(&outFormat, "format", "table", "output format: table|json")
	flag.StringVar(&outPath, "out", "", "output path (default stdout)")
	flag.IntVar(&limit, "limit", 100, "max records to emit (0 = unlimited)")
	flag.StringVar(&classFilter, "diff-class", "", "comma-separated diff classes; only emit records containing ALL listed classes")
	flag.BoolVar(&includeQuiet, "include-quiet", false, "include records whose only class is NO_MATERIAL_CHANGE")
	flag.BoolVar(&includeNew, "include-new", true, "include NEW_CANDIDATE records")
	flag.BoolVar(&includeDropped, "include-dropped", true, "include DROPPED_CANDIDATE records")
	flag.BoolVar(&includeTerminal, "include-terminal", false, "include BECAME_TERMINAL / BECAME_CLOSED / BECAME_INACTIVE records")
	flag.Float64Var(&spreadDelta, "spread-threshold", 0.02, "min |avg_spread_B - avg_spread_A| to fire SPREAD_*")
	flag.Float64Var(&coverageDelta, "coverage-threshold", 0.10, "min |coverage_B - coverage_A| to fire QUOTE_COVERAGE_*")
	flag.Float64Var(&depthAbsDelta, "depth-abs-threshold", 100, "absolute MinTopDepth delta to fire DEPTH_*")
	flag.Float64Var(&depthPctDelta, "depth-pct-threshold", 0.25, "relative MinTopDepth delta (fraction of A) to fire DEPTH_*")
	flag.IntVar(&maxAgeHours, "max-age-hours", 168, "max hours between A and B before STALE_SNAPSHOT warning (0 disables)")
	flag.Parse()

	if scanA == "" || scanB == "" {
		fmt.Fprintln(os.Stderr, "rn1-live-diff: --scan-a and --scan-b are required.")
		flag.Usage()
		os.Exit(2)
	}

	a, err := diff.LoadSnapshot(scanA, simA, "A")
	if err != nil {
		slog.Error("load snapshot A", "err", err)
		os.Exit(1)
	}
	b, err := diff.LoadSnapshot(scanB, simB, "B")
	if err != nil {
		slog.Error("load snapshot B", "err", err)
		os.Exit(1)
	}

	cfg := diff.ThresholdConfig{
		SpreadDelta:   spreadDelta,
		CoverageDelta: coverageDelta,
		DepthAbsDelta: depthAbsDelta,
		DepthPctDelta: depthPctDelta,
	}
	fresh := diff.FreshnessOptions{
		MaxAge: time.Duration(maxAgeHours) * time.Hour,
	}

	result, err := diff.Compute(a, b, cfg, fresh)
	if err != nil {
		slog.Error("compute diff", "err", err)
		os.Exit(1)
	}
	for _, w := range result.Warnings {
		slog.Warn("diff warning", "msg", w)
	}

	filter := diff.FilterOptions{
		Limit:           limit,
		IncludeQuiet:    includeQuiet,
		IncludeNew:      includeNew,
		IncludeDropped:  includeDropped,
		IncludeTerminal: includeTerminal,
		RequiredClasses: parseClasses(classFilter),
	}
	records := filter.Apply(result.Records)

	out, err := openOutput(outPath)
	if err != nil {
		slog.Error("open output", "err", err)
		os.Exit(1)
	}
	defer out.Close()

	switch strings.ToLower(outFormat) {
	case "json":
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(records)
	default:
		printTable(out, records)
	}
}

func parseClasses(s string) []diff.DiffClass {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]diff.DiffClass, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, diff.DiffClass(p))
		}
	}
	return out
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

func printTable(w io.Writer, rs []diff.DiffRecord) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "event_slug\trank_a\trank_b\tdelta\tscore_a\tscore_b\tgate_a\tgate_b\tspread_a\tspread_b\tcov_a\tcov_b\tclasses\tnote")
	for _, r := range rs {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%.2f\t%.2f\t%s\t%s\n",
			r.EventSlug,
			fmtIntOrDash(r.RankA),
			fmtIntOrDash(r.RankB),
			fmtRankDelta(r.RankA, r.RankB),
			fmtFloatOrDash(r.ScoreA),
			fmtFloatOrDash(r.ScoreB),
			orDash(r.GateA),
			orDash(r.GateB),
			fmtFloatOrDash(r.AvgSpreadA),
			fmtFloatOrDash(r.AvgSpreadB),
			r.QuoteCoverageA,
			r.QuoteCoverageB,
			joinClasses(r.DiffClasses),
			r.ReviewNote,
		)
	}
	_ = tw.Flush()
}

func joinClasses(cs []diff.DiffClass) string {
	if len(cs) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		parts = append(parts, string(c))
	}
	return strings.Join(parts, "+")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func fmtIntOrDash(i int) string {
	if i == 0 {
		return "-"
	}
	return fmt.Sprintf("%d", i)
}

func fmtFloatOrDash(f float64) string {
	if f == 0 {
		return "-"
	}
	return fmt.Sprintf("%.3f", f)
}

func fmtRankDelta(a, b int) string {
	if a == 0 || b == 0 {
		return "-"
	}
	d := a - b
	if d > 0 {
		return fmt.Sprintf("+%d", d)
	}
	return fmt.Sprintf("%d", d)
}
